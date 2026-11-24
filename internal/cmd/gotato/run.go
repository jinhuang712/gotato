package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
)

// ---- run ----------------------------------------------------------------

func (c *cli) cmdRun(args []string) int { return c.cmdRunArgs(args, false) }

// cmdRunArgs runs the run command. sessionFromPositional is the
// `session resume <id> "prompt"` form: flags may appear anywhere and the
// first positional is the session ID.
func (c *cli) cmdRunArgs(args []string, sessionFromPositional bool) int {
	ra := runArgs{sessionFromPositional: sessionFromPositional}
	if code, ok := c.parseRunArgs(&ra, args); !ok {
		return code
	}
	// --timeout bounds the run itself (it settles as deadline_exceeded and
	// exits 4); store operations around it are not cut short.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := c.newRuntime(ra.flags)
	if err != nil {
		return c.failErr(err)
	}
	// The instruction and the ceiling belong to an Agent: a flag selects a
	// variant of it. The Run switches the Session to that Agent as its first
	// write; with --no-save the Session stays as it was.
	agent, err := rt.agentFor(ctx, ra.flags, c.getenv, ra.sessionID)
	if err != nil {
		return c.failErr(err)
	}
	result, err := c.startRun(ctx, rt, ra, agent)
	if err != nil && result.SessionID == "" {
		return c.failErr(err)
	}
	outcome := outcomeOf(result)
	code := runExitCode(outcome, err)
	if err := c.reportRun(ra, outcome); err != nil {
		return c.fail(ExitError, err.Error())
	}
	return code
}

// runArgs is what the run command's flags and positionals ask for.
// sessionFromPositional is the `session resume` form, as in cmdRunArgs.
type runArgs struct {
	sessionFromPositional bool
	flags                 modelFlags
	sessionID             string
	events                string
	continueRun           bool
	noSave                bool
	prompt                string
}

// parseRunArgs reads the run command's flags, session ID and prompt into ra.
// When ok is false the command exits with code.
func (c *cli) parseRunArgs(ra *runArgs, args []string) (code int, ok bool) {
	fs := c.newFlagSet("run")
	bindModel(fs, &ra.flags)
	fs.StringVar(&ra.sessionID, "session", "", "continue an existing session (default: a new session is created)")
	fs.IntVar(&ra.flags.compactCeiling, "compact-ceiling", 0, "auto-compact the session when its history exceeds this many estimated tokens (the run uses a variant of the agent with it)")
	fs.StringVar(&ra.events, "events", "", "stream runtime events to stdout: jsonl")
	fs.BoolVar(&ra.continueRun, "continue", false, "continue the session without a new prompt")
	fs.BoolVar(&ra.noSave, "no-save", false, "do not write the run's messages to the store")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err), false
	}
	if ra.events != "" && ra.events != "jsonl" {
		return c.usageError("--events accepts only jsonl"), false
	}
	if ra.sessionFromPositional {
		if len(positionals) == 0 {
			return c.usageError("session resume needs a session id"), false
		}
		ra.sessionID = positionals[0]
		positionals = positionals[1:]
	}
	if !ra.continueRun {
		ra.prompt, err = c.readPrompt(positionals)
		if err != nil {
			return c.usageError(err.Error()), false
		}
	}
	return ExitOK, true
}

// jsonl reports whether --events asks for JSON lines.
func (ra runArgs) jsonl() bool { return ra.events == "jsonl" }

// startRun runs ra's request on agent, streaming its Events as JSON lines
// when --events jsonl asks for them.
func (c *cli) startRun(ctx context.Context, rt *cliRuntime, ra runArgs, agent string) (host.RunResponse, error) {
	request := host.RunRequest{SessionID: ra.sessionID, Agent: agent, Prompt: ra.prompt, Continue: ra.continueRun, Timeout: c.timeout, SkipSave: ra.noSave}
	if ra.jsonl() {
		return rt.runner.StreamRun(ctx, request, func(event gotato.Event) error { return c.writeJSONL(event) })
	}
	return rt.runner.Run(ctx, request)
}

// runExitCode is the exit code of a Run that settled as outcome with err.
func runExitCode(outcome runOutcome, err error) int {
	switch {
	case errors.Is(err, host.ErrNotPersisted):
		// A write of the Run failed, so the Record does not hold it: exit 1, not 4.
		return ExitError
	case outcome.Status != gotato.RunStatusCompleted:
		return ExitRunIncomplete
	}
	return ExitOK
}

// reportRun prints the Run's outcome: as a run_result JSON line with
// --events jsonl, as JSON in machine mode, or as its final text and a status
// line.
func (c *cli) reportRun(ra runArgs, outcome runOutcome) error {
	switch {
	case ra.jsonl():
		return c.writeJSONL(map[string]any{"kind": "run_result", "result": outcome})
	case c.machine():
		c.writeJSON(outcome)
	default:
		if outcome.FinalText != "" {
			fmt.Fprintln(c.stdout, outcome.FinalText)
		}
		c.info("session %s · run %s · %s", outcome.SessionID, outcome.RunID, outcome.Status)
		if outcome.Error != nil {
			fmt.Fprintln(c.stderr, "gotato: "+outcome.Error.Error())
		}
	}
	return nil
}
