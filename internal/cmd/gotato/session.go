package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/store"
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

func (c *cli) sessionCreate(args []string) int {
	fs := c.newFlagSet("session create")
	var flags modelFlags
	bindModel(fs, &flags)
	id := fs.String("id", "", "explicit session id (default: random)")
	meta := fs.String("meta", "", "initial metadata as key=value,key=value")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compaction budget (estimated tokens): the session is made with a variant of the agent that has it")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	flags.compactCeiling = *compactCeiling
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	agent, err := rt.agentFor(ctx, flags, c.getenv, "")
	if err != nil {
		return c.failErr(err)
	}
	metadata, err := parseMeta(*meta)
	if err != nil {
		return c.usageError(err.Error())
	}
	info, err := rt.runner.CreateSession(ctx, host.CreateSessionRequest{Agent: agent, ID: *id, Metadata: metadata})
	if errors.Is(err, host.ErrSessionExists) {
		return c.fail(ExitUsage, "session "+*id+" already exists")
	}
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(info, info.ID)
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
	list, err := listAll(ctx, store)
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

func (c *cli) loadSession(args []string, name string) (*record.Record, context.Context, context.CancelFunc, int) {
	fs := c.newFlagSet(name)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return nil, nil, nil, c.parseError(err)
	}
	if len(positionals) != 1 {
		return nil, nil, nil, c.usageError(name + " needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	st, _, err := c.store()
	if err != nil {
		cancel()
		return nil, nil, nil, c.failErr(err)
	}
	s, err := store.View(ctx, st.Records, positionals[0])
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
	return c.emit(child, child.ID)
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

func parseMeta(spec string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(spec) == "" {
		return out, nil
	}
	for pair := range strings.SplitSeq(spec, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, errors.New("metadata must be key=value pairs")
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, nil
}
