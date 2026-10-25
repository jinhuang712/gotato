// Package bash runs a read-only subset of bash over an io/fs.FS, in process.
//
// It is the engine behind Gotato's bash tool (FEATURES.md, G-F36): the model
// queries the data of its own Session with the pipelines it already knows, and
// nothing runs outside the process. The package does not import Gotato. It
// takes a command string, an fs.FS and a context, and returns stdout, stderr
// and an exit code.
//
// The script is parsed by mvdan.cc/sh in bash mode and checked before anything
// runs: loops, functions, command substitution, writing redirects and the
// other syntax listed in Check are refused with an error that suggests a
// pipeline. What passes is run by mvdan's interpreter with every handler that
// would touch the machine taken over: commands are Go functions of this
// package (never a program), files are opened only from the fs.FS, the working
// directory is "/", and the environment holds no value of the process. The
// same fs.FS and command give the same output on every instance.
//
// Supported means identical: each command and flag behaves as GNU's does, and
// an unsupported flag or command fails with an error that lists what is
// supported.
package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

const (
	// DefaultMaxOutput is the most bytes of stdout, and of stderr, that Run
	// keeps when WithMaxOutput is not given.
	DefaultMaxOutput = 16 << 20
	// MaxCommandBytes is the longest command Run accepts. A command is
	// short; the bound keeps what its expansions can grow to small.
	MaxCommandBytes = 32 << 10
	// MaxLineBytes is the longest line grep reads into memory.
	MaxLineBytes = 16 << 20
)

// Result is what a command wrote and how it ended.
type Result struct {
	Stdout string
	Stderr string
	// ExitCode is the exit status: 0 on success, the command's own status
	// otherwise, 127 for a command that is not supported, and 2 for a script
	// that does not parse or that Check refuses.
	ExitCode int
	// Refused is true when the script was not run because it did not parse
	// or uses syntax or a command that is not supported. Stderr says why.
	Refused bool
	// Truncated is true when stdout went over the bound of WithMaxOutput:
	// the pipeline was stopped and Stdout holds the first bytes.
	Truncated bool
}

// Option changes how Run works.
type Option func(*config)

type config struct {
	maxOutput int
}

// WithMaxOutput bounds stdout, and stderr, to n bytes. When stdout goes over,
// the pipeline stops and Result.Truncated is set. n <= 0 means
// DefaultMaxOutput.
func WithMaxOutput(n int) Option { return func(c *config) { c.maxOutput = n } }

// Run parses and checks command, and runs it with fsys as the only file system
// it can see. The root of fsys is "/" and the working directory is "/".
// Standard input is empty at the start of the script.
//
// The error is nil when the script ran, whatever its exit code, and when it
// was refused (Result.Refused). It is ctx's error when ctx ended the run; the
// Result then holds the output so far.
func Run(ctx context.Context, command string, fsys fs.FS, opts ...Option) (Result, error) {
	cfg := config{maxOutput: DefaultMaxOutput}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxOutput <= 0 {
		cfg.maxOutput = DefaultMaxOutput
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	file, refusal := Check(command)
	if refusal != nil {
		return Result{Stderr: refusal.Error() + "\n", ExitCode: refusal.ExitCode(), Refused: true}, nil
	}

	var stdout, stderr limitBuffer
	stdout.limit, stderr.limit = cfg.maxOutput, cfg.maxOutput
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(fixedEnv()...)),
		withDir("/"),
		interp.StdIO(nil, &stdout, &stderr),
		interp.CallHandler(callHandler),
		interp.ExecHandlers(execMiddleware(fsys)),
		interp.OpenHandler(openHandler(fsys)),
		interp.ReadDirHandler2(readDirHandler(fsys)),
		interp.StatHandler(statHandler(fsys)),
		interp.AccessHandler(accessHandler),
	)
	if err != nil {
		return Result{}, fmt.Errorf("bash: %w", err)
	}
	runErr := runner.Run(ctx, file)

	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), Truncated: stdout.over}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if runErr != nil {
		if status, ok := errors.AsType[interp.ExitStatus](runErr); ok {
			result.ExitCode = int(status)
		} else {
			result.ExitCode = 1
			result.Stderr += "bash: " + runErr.Error() + "\n"
		}
	}
	return result, nil
}

// Commands returns the names of the commands Run supports, sorted.
func Commands() []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// fixedEnv is the whole environment of a script. The interpreter fills in a
// value of the process for HOME, UID, EUID and GID when none is given, so each
// gets a constant here; nothing else is set.
func fixedEnv() []string {
	return []string{"HOME=/", "UID=0", "EUID=0", "GID=0"}
}

// withDir sets the working directory without asking the machine: interp.Dir
// would stat the path on the real file system.
func withDir(dir string) interp.RunnerOption {
	return func(r *interp.Runner) error {
		r.Dir = dir
		return nil
	}
}

// commandPrefix marks a command name after callHandler has looked at it. A
// name with it is neither a function nor a builtin of the interpreter, so the
// call reaches the exec handler, which is the only place a command runs. A
// word of the script cannot hold a NUL, so the prefix cannot be forged.
const commandPrefix = "gotato\x00"

// callHandler runs on every simple command, builtins included, and sends all
// of them to the exec handler under a name the interpreter does not know.
func callHandler(_ context.Context, args []string) ([]string, error) {
	out := append([]string(nil), args...)
	out[0] = commandPrefix + args[0]
	return out, nil
}

// execMiddleware is the only exec handler: it never calls next, which is the
// handler that starts a program.
func execMiddleware(fsys fs.FS) func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			name := strings.TrimPrefix(args[0], commandPrefix)
			cmd, ok := commands[name]
			if !ok {
				_, _ = fmt.Fprintln(hc.Stderr, unknownCommand(name))
				return interp.ExitStatus(127)
			}
			var stdin io.Reader = emptyReader{}
			if f, isFile := hc.Stdin.(*os.File); (isFile && f != nil) || (!isFile && hc.Stdin != nil) {
				stdin = hc.Stdin
			}
			call := &Call{Args: args[1:], Stdin: &ctxReader{ctx: ctx, r: stdin}, Stdout: hc.Stdout, Stderr: hc.Stderr, FS: fsys, Dir: hc.Dir}
			if code := cmd.run(ctx, call); code != 0 {
				return interp.ExitStatus(code)
			}
			return nil
		}
	}
}

// emptyReader is standard input when there is none.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

// ctxReader stops a read once ctx has ended.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// errOutputLimit is what a write returns once the output bound is passed, so
// the command writing it stops, as a command whose reader closed does.
var errOutputLimit = errors.New("output limit reached")

// limitBuffer keeps at most limit bytes, and fails the writes after that.
type limitBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - b.buf.Len()
	if len(p) > room {
		b.buf.Write(p[:max(room, 0)])
		b.over = true
		return max(room, 0), errOutputLimit
	}
	return b.buf.Write(p)
}

func (b *limitBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
