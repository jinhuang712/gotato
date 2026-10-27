package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
)

// ---- run ------------------------------------------------------------------

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
		request.Metadata, err = settings.metadata()
	} else {
		// The settings change the Session as the Run's first write, under
		// its Claim; with --no-save they apply to this Run only.
		request.Update, err = settings.metadata()
	}
	if err != nil {
		return c.failErr(err)
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

// ---- session --------------------------------------------------------------

func (c *cli) cmdSession(args []string) int {
	args, code, done := c.splitLeadingCommonFlags("session", args)
	if done {
		return code
	}
	if len(args) == 0 {
		return c.usageError("session needs a subcommand: create, list, show, fork, events, resume, message, notice, cancel, delete")
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
	if err := settings.apply(func(key, value string) { metadata[key] = value }); err != nil {
		return c.failErr(err)
	}
	if *id != "" {
		s, err := rt.runner.CreateSessionExclusive(ctx, agent, *id, metadata)
		if errors.Is(err, service.ErrSessionExists) {
			return c.fail(ExitUsage, "session "+*id+" already exists")
		}
		if err != nil {
			return c.failErr(err)
		}
		return c.emit(session.InfoOf(s), s.ID())
	}
	s, err := rt.runner.CreateSession(ctx, agent, metadata)
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(session.InfoOf(s), s.ID())
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
		fmt.Fprintf(c.stdout, "%s\t%d msgs\t%d runs\t%s\n", item.ID, item.Messages, item.Runs, item.UpdatedAt)
	}
	return ExitOK
}

func (c *cli) loadSession(args []string, name string) (*session.Record, session.Store, context.Context, context.CancelFunc, int) {
	fs := c.newFlagSet(name)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return nil, nil, nil, nil, c.parseError(err)
	}
	if len(positionals) != 1 {
		return nil, nil, nil, nil, c.usageError(name + " needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	store, _, err := c.store()
	if err != nil {
		cancel()
		return nil, nil, nil, nil, c.failErr(err)
	}
	s, err := session.View(ctx, store, positionals[0])
	if err != nil {
		cancel()
		return nil, nil, nil, nil, c.failErr(err)
	}
	return s, store, ctx, cancel, -1
}

func (c *cli) sessionShow(args []string) int {
	s, _, _, cancel, code := c.loadSession(args, "session show")
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
	return c.emit(session.InfoOf(child), child.ID())
}

func (c *cli) sessionDelete(args []string) int {
	s, _, ctx, cancel, code := c.loadSession(args, "session delete")
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

// ---- context --------------------------------------------------------------

func (c *cli) cmdContext(args []string) int {
	args, code, done := c.splitLeadingCommonFlags("context", args)
	if done {
		return code
	}
	if len(args) == 0 {
		return c.usageError("context needs a subcommand: inspect, build, compact")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "inspect", "build":
		return c.contextInspect(sub, rest)
	case "compact":
		return c.contextCompact(rest)
	default:
		return c.usageError("unknown context subcommand " + sub)
	}
}

func (c *cli) contextInspect(mode string, args []string) int {
	fs := c.newFlagSet("context " + mode)
	var flags modelFlags
	bindModel(fs, &flags)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) != 1 {
		return c.usageError("context " + mode + " needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	report, err := rt.runner.Inspect(ctx, positionals[0], service.InspectConfig{Instruction: flags.instruction})
	if err != nil {
		return c.failErr(err)
	}
	if mode == "build" {
		if c.machine() {
			return c.writeJSON(report.Request)
		}
		fmt.Fprintf(c.stdout, "system:\n%s\n\n", report.Request.SystemInstructions)
		for _, message := range report.Request.Messages {
			fmt.Fprintf(c.stdout, "%-11s %s\n", message.Role, strings.TrimSpace(gotato.TextOf(message)))
		}
		return ExitOK
	}
	if c.machine() {
		return c.writeJSON(report)
	}
	fmt.Fprintf(c.stdout, "session %s\nstrategy %s (append-only)\nmessages %d · ~%d tokens · system %d bytes · tools %d\nprefix_hash %s\n",
		report.SessionID, report.Strategy, report.SelectedMessages, report.ApproxTokens, report.SystemBytes, len(report.Request.Tools), report.PrefixHash)
	if len(report.Compactions) > 0 {
		fmt.Fprintf(c.stdout, "compactions %d (last replaced %d messages)\n", len(report.Compactions), report.Compactions[len(report.Compactions)-1].ReplacedMessages)
	}
	return ExitOK
}

func (c *cli) contextCompact(args []string) int {
	fs := c.newFlagSet("context compact")
	var flags modelFlags
	bindModel(fs, &flags)
	keep := fs.Int("keep-messages", 0, "number of recent messages to keep as they are (default: the latest run's turns)")
	minTokens := fs.Int("min-tokens", 0, "skip a stretch since the last checkpoint estimated below this many tokens (default 0: always compact)")
	recapFlag := fs.String("recap", "none", "none (the default: the segment has no recap), truncate (deterministic) or model (the session's agent writes it on the cached prefix; use only once the payments check passes on your gateway)")
	recapMax := fs.Uint("recap-max-tokens", modelctx.DefaultRecapMaxTokens, "output limit of the model call that writes the recap (--recap model)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if len(positionals) != 1 {
		return c.usageError("context compact needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	if *keep < 0 || *minTokens < 0 {
		return c.usageError("--keep-messages and --min-tokens cannot be negative")
	}
	var writer modelctx.RecapWriter
	switch *recapFlag {
	case "none", "":
	case "truncate":
		writer = modelctx.TruncateRecap{}
	case "model":
		agent, err := rt.resolveAgent(flags.model, c.getenv)
		if err != nil {
			return c.failErr(err)
		}
		if agent == "" {
			s, err := session.Open(ctx, rt.store, positionals[0])
			if err != nil {
				return c.failErr(err)
			}
			agent, _ = s.Get(service.MetaAgent)
		}
		spec, ok := rt.runner.Agent(agent)
		if !ok {
			return c.failErr(service.ErrUnknownAgent)
		}
		writer = modelctx.ModelRecap{Model: spec.Model, MaxTokens: uint32(min(*recapMax, 1<<31)), Label: "model:" + spec.Name}
	default:
		return c.usageError("unknown recap writer " + *recapFlag)
	}
	result, err := rt.runner.Compact(ctx, positionals[0], modelctx.CompactConfig{KeepMessages: *keep, MinTokens: *minTokens, Recap: writer})
	if err != nil {
		return c.failErr(err)
	}
	human := fmt.Sprintf("session %s: %d → %d messages, ~%d → ~%d tokens", result.SessionID, result.MessagesBefore, result.MessagesAfter, result.TokensBefore, result.TokensAfter)
	if !result.Replaced {
		human = fmt.Sprintf("session %s: nothing to compact (%s)", result.SessionID, result.Reason)
	}
	return c.emit(result, human)
}

// ---- tools ----------------------------------------------------------------

// toolView renders a Tool for the CLI with the schema as JSON, not bytes.
type toolView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Sequential  bool            `json:"sequential,omitempty"`
	Effect      string          `json:"effect,omitempty"`
	Active      bool            `json:"active"`
	// ToolSet names the ToolSet of a tool that comes from one; Loaded says
	// whether the model has loaded it; Unavailable marks a ToolSet that did
	// not resolve (its entry has the ToolSet's name as id).
	ToolSet     string `json:"toolset,omitempty"`
	Loaded      bool   `json:"loaded,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

func viewOf(entry service.ToolEntry) toolView {
	view := toolView{ID: entry.Spec.ID, Name: entry.Spec.Name, Description: entry.Spec.Description, Sequential: entry.Spec.Sequential,
		Effect: string(entry.Spec.Effect), Active: entry.Active, ToolSet: entry.ToolSet, Loaded: entry.Loaded, Unavailable: entry.Unavailable}
	if json.Valid(entry.Spec.InputSchema) {
		view.InputSchema = json.RawMessage(entry.Spec.InputSchema)
	}
	return view
}

type toolListing struct {
	SessionID string `json:"session_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	// MaxEffect is the agent's Effect cap; tools above it are not listed.
	MaxEffect string     `json:"max_effect,omitempty"`
	Tools     []toolView `json:"tools"`
}

func (c *cli) cmdTools(args []string) int {
	args, code, done := c.splitLeadingCommonFlags("tools", args)
	if done {
		return code
	}
	if len(args) == 0 {
		return c.usageError("tools needs a subcommand: list, describe, active, activate, deactivate")
	}
	sub, rest := args[0], args[1:]
	fs := c.newFlagSet("tools " + sub)
	var flags modelFlags
	bindModel(fs, &flags)
	sessionID := fs.String("session", "", "session whose tool activation applies")
	positionals, err := parseInterspersed(fs, rest)
	if err != nil {
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
	// Runner.Tools resolves an empty agent to the session's stored agent, or
	// the first registered agent when there is no session. Mirror that here so
	// the listing reports the agent that actually selected the surface.
	if agent == "" && *sessionID != "" {
		s, err := session.Open(ctx, rt.store, *sessionID)
		if err != nil {
			return c.failErr(err)
		}
		agent, _ = s.Get(service.MetaAgent)
	}
	if agent == "" {
		if names := rt.runner.Agents(); len(names) > 0 {
			agent = names[0]
		}
	}
	entries, err := rt.runner.Tools(ctx, agent, *sessionID)
	if err != nil {
		return c.failErr(err)
	}
	spec, _ := rt.runner.Agent(agent)
	listing := toolListing{SessionID: *sessionID, Agent: agent, MaxEffect: string(spec.MaxEffect), Tools: []toolView{}}
	for _, entry := range entries {
		listing.Tools = append(listing.Tools, viewOf(entry))
	}
	switch sub {
	case "list", "ls":
		if c.machine() {
			return c.writeJSON(listing)
		}
		if listing.MaxEffect != "" {
			fmt.Fprintf(c.stdout, "max effect: %s\n", listing.MaxEffect)
		}
		for _, entry := range listing.Tools {
			state := "active"
			if !entry.Active {
				state = "inactive"
			} else if entry.Unavailable {
				state = "unavailable"
			}
			fmt.Fprintf(c.stdout, "%-12s %-8s %s\n", entry.ID, state, entry.Description)
		}
		return ExitOK
	case "active":
		active := toolListing{SessionID: *sessionID, Agent: agent, MaxEffect: listing.MaxEffect, Tools: []toolView{}}
		for _, view := range listing.Tools {
			if view.Active {
				active.Tools = append(active.Tools, view)
			}
		}
		if c.machine() {
			return c.writeJSON(active)
		}
		for _, view := range active.Tools {
			fmt.Fprintln(c.stdout, view.ID)
		}
		return ExitOK
	case "describe":
		if len(positionals) != 1 {
			return c.usageError("tools describe needs exactly one tool id")
		}
		for _, view := range listing.Tools {
			if view.ID == positionals[0] {
				if c.machine() {
					return c.writeJSON(view)
				}
				fmt.Fprintf(c.stdout, "%s\n%s\nactive: %v\nschema: %s\n", view.ID, view.Description, view.Active, string(view.InputSchema))
				return ExitOK
			}
		}
		return c.fail(ExitNotFound, "unknown tool "+positionals[0])
	case "activate", "deactivate":
		if len(positionals) != 1 {
			return c.usageError("tools " + sub + " needs exactly one tool id")
		}
		if *sessionID == "" {
			return c.usageError("tools " + sub + " needs --session ID: activation is session state")
		}
		entry, err := rt.runner.SetToolActive(ctx, *sessionID, positionals[0], sub == "activate")
		if err != nil {
			if gotato.IsCode(err, gotato.CodeInvalidArgument) {
				return c.fail(ExitNotFound, err.Error())
			}
			return c.failErr(err)
		}
		return c.emit(map[string]any{"session_id": *sessionID, "tool": viewOf(entry)}, fmt.Sprintf("%s %sd for session %s", positionals[0], sub, *sessionID))
	default:
		return c.usageError("unknown tools subcommand " + sub)
	}
}

// ---- events ---------------------------------------------------------------

func (c *cli) cmdEvents(args []string) int {
	fs := c.newFlagSet("events")
	sessionID := fs.String("session", "", "session id")
	kind := fs.String("kind", "", "only events of this kind")
	positionalSession := false
	if len(args) > 0 && args[0] == "--session-positional" {
		positionalSession = true
		args = args[1:]
	}
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.parseError(err)
	}
	if positionalSession && len(positionals) > 0 && *sessionID == "" {
		*sessionID = positionals[0]
	}
	if *sessionID == "" {
		return c.usageError("events needs --session ID")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	events, err := session.StoredEvents(ctx, store, *sessionID, 0)
	if err != nil {
		return c.failErr(err)
	}
	if *kind != "" {
		filtered := events[:0]
		for _, event := range events {
			if string(event.Kind()) == *kind {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	if c.json {
		if events == nil {
			events = []gotato.Event{}
		}
		return c.writeJSON(events)
	}
	for _, event := range events {
		if err := c.writeJSONL(event); err != nil {
			return c.fail(ExitError, err.Error())
		}
	}
	return ExitOK
}

// ---- serve ----------------------------------------------------------------

// cmdServe runs the same Runner behind the HTTP API.
func (c *cli) cmdServe(args []string) int {
	fs := c.newFlagSet("serve")
	var flags modelFlags
	bindModel(fs, &flags)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	maxRuns := fs.Int("max-runs", 0, "maximum concurrent runs; 0 disables the bound")
	maxSessions := fs.Int("max-sessions", 0, "hard limit on active sessions; past it a new session is refused with a 503 marked not run; 0 disables the limit")
	queue := fs.String("queue", "reject", "policy for a busy session: reject or wait")
	warmTTL := fs.Duration("warm-ttl", 0, "how long an idle session stays in memory after its run; 0 is 60m, a negative value keeps none")
	noIdleCompaction := fs.Bool("no-idle-compaction", false, "do not compact an idle session shortly before its prompt cache expires or it leaves memory")
	drain := fs.Duration("drain-timeout", 10*time.Second, "time to wait for active runs on shutdown before canceling them")
	advertise := fs.String("advertise", "", "URL other callers reach this instance at, such as http://10.0.0.7:8787; a message for a run this instance writes, sent to another instance, is answered not_writer with it")
	forward := fs.Bool("forward", false, "send a message or cancel for a run another instance writes to that instance's advertised address, instead of answering not_writer")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	switch *queue {
	case string(service.QueuePolicyReject), string(service.QueuePolicyWait):
	default:
		return c.usageError("--queue accepts reject or wait")
	}
	if *maxRuns < 0 {
		return c.usageError("--max-runs cannot be negative")
	}
	if *maxSessions < 0 {
		return c.usageError("--max-sessions cannot be negative")
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	// Rebuild with admission settings: the Runner is cheap to construct.
	specs := make([]service.Agent, 0)
	for _, name := range rt.runner.Agents() {
		spec, _ := rt.runner.Agent(name)
		spec.Warm = service.WarmPolicy{TTL: *warmTTL, NoIdleCompaction: *noIdleCompaction}
		specs = append(specs, spec)
	}
	runner, err := service.New(service.Config{Store: rt.store, Agents: specs, Address: *advertise, Admission: service.Admission{MaxActiveRuns: *maxRuns, MaxActiveSessions: *maxSessions, Queue: service.QueuePolicy(*queue)}})
	if err != nil {
		return c.failErr(err)
	}
	// closeRunners removes its Liveness row when serve returns, also after a
	// failed listen.
	c.runners = append(c.runners, runner)
	var handlerOptions []httpapi.Option
	if *forward {
		handlerOptions = append(handlerOptions, httpapi.WithForwarding(nil))
	}
	server := &http.Server{Addr: *addr, Handler: httpapi.New(runner, handlerOptions...), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// --timeout bounds the process lifetime: when it elapses the server drains
	// and shuts down like a signal. A signal still works when --timeout is 0.
	if c.timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, c.timeout)
		defer timeoutCancel()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	c.info("gotato serving on http://%s (store=%s agents=%s contract=%s)", *addr, rt.storeDir, strings.Join(runner.Agents(), ","), httpapi.ContractVersion)
	if c.machine() {
		_ = c.writeJSONL(map[string]any{"kind": "serving", "addr": *addr, "store": rt.storeDir, "agents": runner.Agents(), "contract": httpapi.ContractVersion})
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return c.fail(ExitError, err.Error())
		}
		return ExitOK
	case <-ctx.Done():
	}
	// Drain in order: the Liveness row says draining, /readyz answers 503, no
	// new Run starts, and every Run finishes, saves and releases its Session.
	// Then the streams close, and Close removes the row.
	c.info("draining (up to %s)", *drain)
	drainCtx, cancel := context.WithTimeout(context.Background(), *drain)
	defer cancel()
	if err := runner.Drain(drainCtx); err != nil {
		c.info("drain: %v", err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdownCtx)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if err := runner.Close(closeCtx); err != nil {
		c.info("close: %v", err)
	}
	c.info("stopped")
	return ExitOK
}

// ---- doctor ---------------------------------------------------------------

type doctorReport struct {
	OK      bool          `json:"ok"`
	Version string        `json:"version"`
	Go      string        `json:"go"`
	Checks  []doctorCheck `json:"checks"`
}

type doctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Warning bool   `json:"warning,omitempty"`
}

func (c *cli) cmdDoctor(args []string) int {
	fs := c.newFlagSet("doctor")
	var flags modelFlags
	bindModel(fs, &flags)
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	report := doctorReport{OK: true, Version: Version, Go: runtime.Version()}
	add := func(check doctorCheck) {
		if !check.OK && !check.Warning {
			report.OK = false
		}
		report.Checks = append(report.Checks, check)
	}

	rt, err := c.newRuntime(flags)
	if err != nil {
		add(doctorCheck{Name: "store", OK: false, Detail: err.Error()})
	} else {
		probe := session.New()
		if _, err := session.CreateWriter(ctx, rt.store, probe, rt.runner.Instance()); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": not writable: " + err.Error()})
		} else if err := rt.store.Delete(ctx, probe.ID()); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": probe cleanup failed: " + err.Error()})
		} else if list, err := rt.store.List(ctx); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": cannot list sessions: " + err.Error()})
		} else {
			add(doctorCheck{Name: "store", OK: true, Detail: fmt.Sprintf("%s (%d sessions)", rt.storeDir, len(list))})
		}
		add(doctorCheck{Name: "model.echo", OK: true, Detail: "deterministic, no credentials"})
		add(doctorCheck{Name: "model.demo", OK: true, Detail: "deterministic tool loop, no credentials"})
		if rt.gatewayErr == nil {
			spec, _ := rt.runner.Agent("gateway")
			add(doctorCheck{Name: "model.gateway", OK: true, Detail: rt.gatewayCfg + " → " + spec.ModelName})
		} else if fileExists(rt.gatewayCfg) {
			add(doctorCheck{Name: "model.gateway", OK: false, Detail: rt.gatewayErr.Error()})
		} else {
			add(doctorCheck{Name: "model.gateway", OK: false, Warning: true, Detail: rt.gatewayCfg + " not found; --model gateway unavailable"})
		}
		names := make([]string, 0)
		for _, tool := range builtinTools() {
			names = append(names, tool.Spec().ID)
		}
		add(doctorCheck{Name: "tools", OK: true, Detail: strings.Join(names, ", ")})
	}
	if _, err := os.UserHomeDir(); err != nil {
		add(doctorCheck{Name: "home", OK: false, Warning: true, Detail: err.Error()})
	}

	if c.machine() {
		c.writeJSON(report)
	} else {
		for _, check := range report.Checks {
			mark := "ok  "
			if !check.OK {
				mark = "FAIL"
				if check.Warning {
					mark = "warn"
				}
			}
			fmt.Fprintf(c.stdout, "%s %-14s %s\n", mark, check.Name, check.Detail)
		}
	}
	if !report.OK {
		return ExitError
	}
	return ExitOK
}

func parseMeta(spec string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(spec) == "" {
		return out, nil
	}
	for _, pair := range strings.Split(spec, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, errors.New("metadata must be key=value pairs")
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, nil
}
