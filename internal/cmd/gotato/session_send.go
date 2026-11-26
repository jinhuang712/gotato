package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
)

// sessionMessage sends a message to a Session (Runner.Send). This process runs
// nothing in the background, so no Run of its own can take the message: it
// either starts a new Run, as `session resume` does, or fails with not_writer
// when a Run is going in another process, naming the writer's address when it
// advertises one.
func (c *cli) sessionMessage(args []string) int {
	fs := c.newFlagSet("session message")
	var flags modelFlags
	bindModel(fs, &flags)
	kind := fs.String("kind", string(host.MessageKindSteer), "steer, follow_up or interrupt")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) == 0 {
		return c.usageError("session message needs a session id")
	}
	text, err := c.readPrompt(positionals[1:])
	if err != nil {
		return c.usageError(err.Error())
	}
	if !host.MessageKind(*kind).Valid() {
		return c.usageError("--kind accepts steer, follow_up or interrupt")
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := rt.runner.Send(ctx, host.MessageRequest{SessionID: positionals[0], Kind: host.MessageKind(*kind), Text: text})
	if result.Run == nil || result.Run.SessionID == "" {
		if err != nil {
			return c.failErr(err)
		}
		return c.emit(map[string]any{"session_id": result.SessionID, "message_id": result.MessageID, "queued": result.Queued}, "queued "+result.MessageID)
	}
	return c.reportMessageRun(*result.Run, err)
}

// reportMessageRun prints the Run that a message started and returns the exit
// code of its outcome.
func (c *cli) reportMessageRun(run host.RunResponse, err error) int {
	outcome := outcomeOf(run)
	code := ExitOK
	switch {
	case errors.Is(err, host.ErrNotPersisted):
		code = ExitError
	case outcome.Status != gotato.RunStatusCompleted:
		code = ExitRunIncomplete
	}
	if c.machine() {
		c.writeJSON(outcome)
	} else {
		if outcome.FinalText != "" {
			fmt.Fprintln(c.stdout, outcome.FinalText)
		}
		c.info("session %s · run %s · %s", outcome.SessionID, outcome.RunID, outcome.Status)
	}
	return code
}

// sessionNotice queues a notice for a Session's model (Runner.Notice). It
// starts no Run: the notice_queued is stored with the Session, and the next
// Run, from this CLI or any process, puts the notice in after its prompt. A
// Run going in another process fails with not_writer, as a message does.
func (c *cli) sessionNotice(args []string) int {
	fs := c.newFlagSet("session notice")
	var flags modelFlags
	bindModel(fs, &flags)
	kind := fs.String("kind", "", "your own short label for the notice (required)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) == 0 {
		return c.usageError("session notice needs a session id")
	}
	if strings.TrimSpace(*kind) == "" {
		return c.usageError("session notice needs --kind")
	}
	if gotato.ReservedNoticeKind(*kind) {
		return c.usageError("--kind " + *kind + " is reserved for Gotato's own notices")
	}
	text, err := c.readPrompt(positionals[1:])
	if err != nil {
		return c.usageError(err.Error())
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := rt.runner.Notice(ctx, host.NoticeRequest{SessionID: positionals[0], Kind: *kind, Text: text})
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]any{"session_id": result.SessionID, "number": result.Number, "message_id": result.MessageID}, fmt.Sprintf("queued notice %d", result.Number))
}

// sessionAbort stops the Run of a Session (Runner.AbortSession). A Run in
// another process is not stopped from here: the failure is not_writer, with the
// writer's address when it advertises one.
func (c *cli) sessionAbort(args []string) int {
	fs := c.newFlagSet("session abort")
	var flags modelFlags
	bindModel(fs, &flags)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) != 1 {
		return c.usageError("session abort needs a session id")
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	if err := rt.runner.AbortSession(context.Background(), positionals[0]); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]any{"session_id": positionals[0], "status": "abort_requested"}, "abort requested")
}
