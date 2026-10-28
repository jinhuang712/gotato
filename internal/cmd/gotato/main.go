// Command gotato is the test tool of this repository: a thin client over the runtime
// packages. It is not shipped and promises no contract.
//
//	gotato run [--session ID] [--model echo|demo|gateway] [--json|--events jsonl] "prompt"
//	gotato session create|list|show|fork|events|resume|message|notice|abort|delete
//	gotato context inspect|build|compact <session>
//	gotato tools list|describe|active|activate|deactivate
//	gotato events --session <id> [--jsonl|--json]
//	gotato doctor [--json]
//	gotato serve [--addr HOST:PORT]
//
// Stdout carries requested data; diagnostics go to stderr. Exit codes:
// 0 ok, 1 runtime error, 2 usage error, 3 not found, 4 run did not complete.
// See README.md next to this file.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// Version is the version reported by doctor.
const Version = "1"

func main() {
	os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// cli holds the per-invocation environment so the command can be tested
// in-process.
type cli struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string

	// common flags
	storeDir string
	json     bool
	jsonl    bool
	quiet    bool
	timeout  time.Duration

	// runners are the Runners this invocation built. They are closed when it
	// ends, so the next process takes their Sessions over at once.
	runners []*service.Runner
}

// closeRunners removes the Liveness rows of the Runners this invocation built.
func (c *cli) closeRunners() {
	for _, runner := range c.runners {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = runner.Close(ctx)
		cancel()
	}
}

// Main runs the CLI and returns its exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv}
	defer c.closeRunners()
	if getenv == nil {
		c.getenv = func(string) string { return "" }
	}
	// Scan the raw arguments for machine-output flags before dispatch. Flag
	// parsing stops at the command token or a bad flag, so without this a
	// usage error after `--json` would print no JSON object.
	c.prescanMachineFlags(args)
	// Leading global flags are accepted before the command.
	global := flag.NewFlagSet("gotato", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	c.bindCommon(global)
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.usage()
			return ExitOK
		}
		return c.usageError(err.Error())
	}
	rest := global.Args()
	if len(rest) == 0 {
		c.usageTo(c.stderr)
		return c.usageError("a command is required")
	}
	command, rest := rest[0], rest[1:]
	switch command {
	case "run":
		return c.cmdRun(rest)
	case "session":
		return c.cmdSession(rest)
	case "context":
		return c.cmdContext(rest)
	case "tools":
		return c.cmdTools(rest)
	case "events":
		return c.cmdEvents(rest)
	case "doctor":
		return c.cmdDoctor(rest)
	case "serve":
		return c.cmdServe(rest)
	case "version":
		fs := c.newFlagSet("version")
		if _, err := parseInterspersed(fs, rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				c.usage()
				return ExitOK
			}
			return c.parseError(err)
		}
		return c.emit(map[string]any{"version": Version}, Version)
	case "help", "-h", "--help":
		c.usage()
		return ExitOK
	default:
		return c.usageError("unknown command " + command)
	}
}

func (c *cli) bindCommon(fs *flag.FlagSet) {
	fs.StringVar(&c.storeDir, "store", c.storeDir, "session store directory (default $GOTATO_HOME/sessions or ~/.gotato/sessions)")
	fs.BoolVar(&c.json, "json", c.json, "machine-readable JSON output on stdout")
	fs.BoolVar(&c.jsonl, "jsonl", c.jsonl, "machine-readable JSON Lines output on stdout")
	fs.BoolVar(&c.quiet, "quiet", c.quiet, "suppress human-oriented output and diagnostics that are not errors")
	fs.DurationVar(&c.timeout, "timeout", c.timeout, "overall deadline for the command (0 = none)")
}

// newFlagSet creates a subcommand flag set with the common flags bound.
func (c *cli) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.bindCommon(fs)
	return fs
}

// parseInterspersed parses flags that may appear before or after positional
// arguments and returns the positionals in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
		if len(args) == 0 {
			return positionals, nil
		}
	}
}

// splitLeadingCommonFlags consumes the common flags that appear before a
// group's subcommand (for example `gotato session --json list`) and reports
// --help. It returns the remaining arguments, and a non-zero exit code with
// done=true when the caller should return immediately.
func (c *cli) splitLeadingCommonFlags(name string, args []string) ([]string, int, bool) {
	if len(args) == 0 {
		return args, ExitOK, false
	}
	if args[0] == "-h" || args[0] == "--help" {
		c.usage()
		return nil, ExitOK, true
	}
	if args[0] == "" || args[0] == "-" || args[0][0] != '-' {
		return args, ExitOK, false
	}
	fs := c.newFlagSet(name)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.usage()
			return nil, ExitOK, true
		}
		return nil, c.parseError(err), true
	}
	return fs.Args(), ExitOK, false
}

func (c *cli) ctx() (context.Context, context.CancelFunc) {
	if c.timeout > 0 {
		return context.WithTimeout(context.Background(), c.timeout)
	}
	return context.WithCancel(context.Background())
}

// prescanMachineFlags sets --json/--jsonl from the raw arguments regardless of
// where they appear, so a usage error after an unknown command or an invalid
// flag still emits the documented error object. Flag parsing still runs later
// and can override the value.
func (c *cli) prescanMachineFlags(args []string) {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		boolValue := true
		if hasValue {
			if parsed, err := strconv.ParseBool(value); err == nil {
				boolValue = parsed
			}
		}
		switch strings.TrimLeft(name, "-") {
		case "json":
			c.json = boolValue
		case "jsonl":
			c.jsonl = boolValue
		}
	}
}

// store opens the configured Session store.
func (c *cli) store() (session.Store, string, error) {
	dir := c.storeDir
	if dir == "" {
		if home := c.getenv("GOTATO_HOME"); home != "" {
			dir = filepath.Join(home, "sessions")
		} else {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return nil, "", err
			}
			dir = filepath.Join(userHome, ".gotato", "sessions")
		}
	}
	store, err := session.NewFileStore(dir)
	if err != nil {
		return nil, dir, err
	}
	return store, dir, nil
}
