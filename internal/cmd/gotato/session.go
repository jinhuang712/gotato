package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

func (c *cli) cmdSession(args []string) int {
	args, code, done := c.splitLeadingCommonFlags("session", args)
	if done {
		return code
	}
	if len(args) == 0 {
		return c.usageError("session needs a subcommand: create, list, show, fork, events, resume, message, notice, abort, delete")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return c.sessionCreate(rest)
	case "list", "ls":
		return c.sessionList(rest)
	case "show", "get":
		return c.sessionShow(rest)
	case "fork":
		return c.sessionFork(rest)
	case "events":
		return c.cmdEvents(append([]string{"--session-positional"}, rest...))
	case "resume":
		return c.cmdRunArgs(rest, true)
	case "message":
		return c.sessionMessage(rest)
	case "notice":
		return c.sessionNotice(rest)
	case "abort", "stop":
		return c.sessionAbort(rest)
	case "delete", "rm":
		return c.sessionDelete(rest)
	default:
		return c.usageError("unknown session subcommand " + sub)
	}
}

// sessionMessage sends a message to a Session (Runner.Send). This process runs
// nothing in the background, so no Run of its own can take the message: it
// either starts a new Run, as `session resume` does, or fails with not_writer
// when a Run is going in another process, naming the writer's address when it
// advertises one.
func (c *cli) sessionMessage(args []string) int {
	fs := c.newFlagSet("session message")
	var flags modelFlags
	bindModel(fs, &flags)
	kind := fs.String("kind", string(service.MessageKindSteer), "steer, follow_up or interrupt")
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
	if !service.MessageKind(*kind).Valid() {
		return c.usageError("--kind accepts steer, follow_up or interrupt")
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := rt.runner.Send(ctx, service.MessageRequest{SessionID: positionals[0], Kind: service.MessageKind(*kind), Text: text})
	if result.Run == nil || result.Run.SessionID == "" {
		if err != nil {
			return c.failErr(err)
		}
		return c.emit(map[string]any{"session_id": result.SessionID, "message_id": result.MessageID, "queued": result.Queued}, "queued "+result.MessageID)
	}
	outcome := outcomeOf(*result.Run)
	code := ExitOK
	switch {
	case errors.Is(err, service.ErrNotPersisted):
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
	result, err := rt.runner.Notice(ctx, service.NoticeRequest{SessionID: positionals[0], Kind: *kind, Text: text})
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

func (c *cli) sessionCreate(args []string) int {
	fs := c.newFlagSet("session create")
	var flags modelFlags
	bindModel(fs, &flags)
	id := fs.String("id", "", "explicit session id (default: random)")
	meta := fs.String("meta", "", "initial metadata as key=value,key=value")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compaction budget (estimated tokens) stored in the session")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	agent, err := rt.resolveAgent(flags.model, c.getenv)
	if err != nil {
		return c.failErr(err)
	}
	metadata, err := parseMeta(*meta)
	if err != nil {
		return c.usageError(err.Error())
	}
	settings := sessionSettings{instruction: flags.instruction, compactCeiling: *compactCeiling}
	settings.apply(func(key, value string) { metadata[key] = value })
	if *id != "" {
		s, err := rt.runner.CreateSessionExclusive(ctx, agent, *id, metadata)
		if errors.Is(err, service.ErrSessionExists) {
			return c.fail(ExitUsage, "session "+*id+" already exists")
		}
		if err != nil {
			return c.failErr(err)
		}
		return c.emit(s.Info(), s.ID())
	}
	s, err := rt.runner.CreateSession(ctx, agent, metadata)
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(s.Info(), s.ID())
}

func (c *cli) sessionList(args []string) int {
	fs := c.newFlagSet("session list")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	list, err := store.List(ctx)
	if err != nil {
		return c.failErr(err)
	}
	if c.jsonl {
		for _, item := range list {
			if err := c.writeJSONL(item); err != nil {
				return c.fail(ExitError, err.Error())
			}
		}
		return ExitOK
	}
	if c.json {
		return c.writeJSON(list)
	}
	for _, item := range list {
		fmt.Fprintf(c.stdout, "%s\t%d msgs\t%d runs\t%s\n", item.ID, item.Messages, item.Runs, item.UpdatedAt.Format(time.RFC3339))
	}
	return ExitOK
}

func (c *cli) loadSession(args []string, name string) (*session.Record, context.Context, context.CancelFunc, int) {
	fs := c.newFlagSet(name)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return nil, nil, nil, c.parseError(err)
	}
	if len(positionals) != 1 {
		return nil, nil, nil, c.usageError(name + " needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	store, _, err := c.store()
	if err != nil {
		cancel()
		return nil, nil, nil, c.failErr(err)
	}
	s, err := session.View(ctx, store, positionals[0])
	if err != nil {
		cancel()
		return nil, nil, nil, c.failErr(err)
	}
	return s, ctx, cancel, -1
}

func (c *cli) sessionShow(args []string) int {
	s, _, cancel, code := c.loadSession(args, "session show")
	if code >= 0 {
		return code
	}
	defer cancel()
	doc := s.Snapshot()
	if c.machine() {
		return c.writeJSON(doc)
	}
	fmt.Fprintf(c.stdout, "session %s (%d messages, %d runs, %d events)\n", doc.ID, len(doc.Messages), len(doc.Runs), len(doc.Events))
	if doc.ParentID != "" {
		fmt.Fprintf(c.stdout, "forked from %s\n", doc.ParentID)
	}
	for _, message := range doc.Messages {
		text := strings.TrimSpace(gotato.TextOf(message))
		if len(message.ToolCalls) > 0 {
			text += fmt.Sprintf(" [%d tool call(s)]", len(message.ToolCalls))
		}
		fmt.Fprintf(c.stdout, "  %-11s %s\n", message.Role, text)
	}
	return ExitOK
}

func (c *cli) sessionFork(args []string) int {
	fs := c.newFlagSet("session fork")
	id := fs.String("id", "", "explicit id for the fork")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) != 1 {
		return c.usageError("session fork needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(modelFlags{})
	if err != nil {
		return c.failErr(err)
	}
	child, err := rt.runner.Fork(ctx, positionals[0], *id)
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(child.Info(), child.ID())
}

func (c *cli) sessionDelete(args []string) int {
	s, ctx, cancel, code := c.loadSession(args, "session delete")
	if code >= 0 {
		return code
	}
	defer cancel()
	rt, err := c.newRuntime(modelFlags{})
	if err != nil {
		return c.failErr(err)
	}
	if err := rt.runner.DeleteSession(ctx, s.ID()); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]any{"id": s.ID(), "deleted": true}, "deleted "+s.ID())
}
