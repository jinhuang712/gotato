// Command gotato is the official Gotato CLI: a thin client over the runtime
// packages for humans, shell automation, and coding agents.
//
//	gotato run [--session ID] [--model echo|demo|gateway] [--json|--events jsonl] "prompt"
//	gotato session create|list|show|fork|events|resume
//	gotato context inspect|build|compact <session>
//	gotato tools list|describe|active|activate|deactivate
//	gotato events --session <id> [--jsonl|--json]
//	gotato doctor [--json]
//	gotato serve [--addr HOST:PORT]
//
// Stdout carries requested data; diagnostics go to stderr. Exit codes:
// 0 ok, 1 runtime error, 2 usage error, 3 not found, 4 run did not complete.
// See README.md next to this file for the contract.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Exit codes are part of the CLI contract.
const (
	ExitOK            = 0
	ExitError         = 1
	ExitUsage         = 2
	ExitNotFound      = 3
	ExitRunIncomplete = 4
)

// Version is the CLI contract version reported by doctor.
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
	noColor  bool
	timeout  time.Duration
}

// Main runs the CLI and returns its exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv}
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
	fs.BoolVar(&c.noColor, "no-color", c.noColor, "accepted for compatibility; the CLI never emits ANSI color")
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

func (c *cli) machine() bool { return c.json || c.jsonl }

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

// emit prints value as JSON in machine mode, or human text otherwise.
func (c *cli) emit(value any, human string) int {
	if c.machine() {
		return c.writeJSON(value)
	}
	if !c.quiet && human != "" {
		fmt.Fprintln(c.stdout, human)
	}
	return ExitOK
}

func (c *cli) writeJSON(value any) int {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	if !c.jsonl {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(value); err != nil {
		return c.fail(ExitError, "encode: "+err.Error())
	}
	return ExitOK
}

func (c *cli) writeJSONL(value any) error {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

// fail reports an error on stderr (and as JSON on stdout in machine mode so
// scripts see one shape) and returns the code.
func (c *cli) fail(code int, message string) int {
	fmt.Fprintln(c.stderr, "gotato: "+message)
	if c.machine() {
		c.writeErrorObject(code, message)
	}
	return code
}

// writeErrorObject mirrors writeJSON: pretty in --json mode, compact in
// --jsonl mode, so error and data shapes look the same.
func (c *cli) writeErrorObject(code int, message string) {
	enc := json.NewEncoder(c.stdout)
	enc.SetEscapeHTML(false)
	if !c.jsonl {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(map[string]any{"error": message, "exit_code": code})
}

// usageError reports a usage mistake. In machine mode it also writes the
// documented error object to stdout, so a script never gets empty output.
func (c *cli) usageError(message string) int {
	fmt.Fprintln(c.stderr, "gotato: "+message)
	fmt.Fprintln(c.stderr, "run 'gotato help' for usage")
	if c.machine() {
		c.writeErrorObject(ExitUsage, message)
	}
	return ExitUsage
}

// parseError turns a flag parse failure into the right exit: --help asks for
// usage, anything else is a usage mistake.
func (c *cli) parseError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		c.usage()
		return ExitOK
	}
	return c.usageError(err.Error())
}

// failErr classifies an error into an exit code.
func (c *cli) failErr(err error) int {
	switch {
	case errors.Is(err, session.ErrNotFound):
		return c.fail(ExitNotFound, err.Error())
	case gotato.IsCode(err, gotato.ErrInvalidArgument):
		return c.fail(ExitUsage, err.Error())
	default:
		return c.fail(ExitError, err.Error())
	}
}

func (c *cli) info(format string, args ...any) {
	if !c.quiet {
		fmt.Fprintf(c.stderr, format+"\n", args...)
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

func (c *cli) usage() { c.usageTo(c.stdout) }

// usageTo writes the human usage text to w. stdout for an explicit help
// request, stderr for a usage mistake.
func (c *cli) usageTo(w io.Writer) {
	fmt.Fprint(w, strings.TrimLeft(`
gotato — minimalistic, composable Go agent runtime

Usage:
  gotato [global flags] <command> [flags] [args]

Commands:
  run        [--session ID] [--model echo|demo|gateway] [--panel time,cwd] [--compact-ceiling N] [--json|--events jsonl] "prompt"
  session    create | list | show <id> | fork <id> | events <id> | resume <id> "prompt" | delete <id>
  context    inspect <id> | build <id> | compact <id> [--keep N] [--summarizer truncate|model]
  tools      list | describe <id> | active | activate <id> --session ID | deactivate <id> --session ID
  events     --session <id> [--jsonl|--json]
  serve      [--addr HOST:PORT] [--max-runs N] [--queue reject|wait]   HTTP session service
  doctor     [--json]
  version

Global flags (also accepted after the command):
  --store DIR   session store directory (default $GOTATO_HOME/sessions or ~/.gotato/sessions)
  --json        JSON output on stdout      --jsonl   JSON Lines output on stdout
  --quiet       suppress non-error output  --no-color  accepted; output is never colored
  --timeout D   overall deadline, e.g. 30s

Exit codes: 0 ok · 1 runtime error · 2 usage error · 3 not found · 4 run did not complete
`, "\n"))
}
