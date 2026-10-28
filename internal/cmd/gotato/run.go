package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
)

// ---- run ----------------------------------------------------------------

func (c *cli) cmdRun(args []string) int { return c.cmdRunArgs(args, false) }

// cmdRunArgs runs the run command. sessionFromPositional is the
// `session resume <id> "prompt"` form: flags may appear anywhere and the
// first positional is the session ID.
func (c *cli) cmdRunArgs(args []string, sessionFromPositional bool) int {
	fs := c.newFlagSet("run")
	var flags modelFlags
	bindModel(fs, &flags)
	sessionID := fs.String("session", "", "continue an existing session (default: a new session is created)")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compact the session when its history exceeds this many estimated tokens (stored in the session)")
	events := fs.String("events", "", "stream runtime events to stdout: jsonl")
	continueRun := fs.Bool("continue", false, "continue the session without a new prompt")
	noSave := fs.Bool("no-save", false, "do not write the run's messages to the store")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if *events != "" && *events != "jsonl" {
		return c.usageError("--events accepts only jsonl")
	}
	if sessionFromPositional {
		if len(positionals) == 0 {
			return c.usageError("session resume needs a session id")
		}
		*sessionID = positionals[0]
		positionals = positionals[1:]
	}
	var prompt string
	if !*continueRun {
		prompt, err = c.readPrompt(positionals)
		if err != nil {
			return c.usageError(err.Error())
		}
	}
	// --timeout bounds the run itself (it settles as deadline_exceeded and
	// exits 4); store operations around it are not cut short.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	agent, err := rt.resolveAgent(flags.model, c.getenv)
	if err != nil {
		return c.failErr(err)
	}
	settings := sessionSettings{instruction: flags.instruction, compactCeiling: *compactCeiling}
	request := service.RunRequest{SessionID: *sessionID, Agent: agent, Prompt: prompt, Continue: *continueRun, Timeout: c.timeout, SkipSave: *noSave}
	if *sessionID == "" {
		request.Metadata = settings.metadata()
	} else {
		// The settings change the Session as the Run's first write, under
		// its Claim; with --no-save they apply to this Run only.
		request.Update = settings.metadata()
	}
	var result service.RunResponse
	if *events == "jsonl" {
		result, err = rt.runner.StreamRun(ctx, request, func(event gotato.Event) error { return c.writeJSONL(event) })
	} else {
		result, err = rt.runner.Run(ctx, request)
	}
	if err != nil && result.SessionID == "" {
		return c.failErr(err)
	}
	outcome := outcomeOf(result)
	code := ExitOK
	switch {
	case errors.Is(err, service.ErrNotPersisted):
		// A write of the Run failed, so the Record does not hold it: exit 1, not 4.
		code = ExitError
	case outcome.Status != gotato.RunStatusCompleted:
		code = ExitRunIncomplete
	}
	switch {
	case *events == "jsonl":
		if err := c.writeJSONL(map[string]any{"kind": "run_result", "result": outcome}); err != nil {
			return c.fail(ExitError, err.Error())
		}
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
	return code
}
