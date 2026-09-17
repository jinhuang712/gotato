package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/toolregistry"
)

// ---- run ------------------------------------------------------------------

func (c *cli) cmdRun(args []string) int {
	fs := c.newFlagSet("run")
	var flags modelFlags
	c.bindModel(fs, &flags)
	sessionID := fs.String("session", "", "continue an existing session (default: a new session is created)")
	panel := fs.String("panel", "", "dynamic panel appended to the tail of each request: time,cwd (default: session setting or none)")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compact the session when its history exceeds this many estimated tokens (default: session setting or off)")
	events := fs.String("events", "", "stream runtime events to stdout: jsonl")
	continueRun := fs.Bool("continue", false, "continue the session without a new prompt")
	noSave := fs.Bool("no-save", false, "do not persist the session")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
	}
	if *events != "" && *events != "jsonl" {
		return c.usageError("--events accepts only jsonl")
	}
	var prompt string
	if !*continueRun {
		prompt, err = c.readPrompt(positionals)
		if err != nil {
			return c.usageError(err.Error())
		}
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	var s *session.Session
	if *sessionID != "" {
		s, err = store.Get(ctx, *sessionID)
		if err != nil {
			return c.failErr(err)
		}
	} else {
		s = session.New()
	}
	opts := runOptions{prompt: prompt, continueRun: *continueRun, panel: *panel, compactCeiling: *compactCeiling, model: flags}
	if *events == "jsonl" {
		opts.eventSink = func(event gotato.Event) error { return c.writeJSONL(event) }
	}
	outcome, err := c.executeRun(ctx, s, opts)
	if !*noSave {
		if saveErr := store.Save(context.Background(), s); saveErr != nil {
			c.info("warning: session not saved: %v", saveErr)
		}
	}
	if err != nil {
		return c.failErr(err)
	}
	code := ExitOK
	if outcome.Status != gotato.RunCompleted {
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
	if len(args) == 0 {
		return c.usageError("session needs a subcommand: create, list, show, fork, events, resume, delete")
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
		return c.sessionResume(rest)
	case "delete", "rm":
		return c.sessionDelete(rest)
	default:
		return c.usageError("unknown session subcommand " + sub)
	}
}

func (c *cli) sessionCreate(args []string) int {
	fs := c.newFlagSet("session create")
	id := fs.String("id", "", "explicit session id (default: random)")
	meta := fs.String("meta", "", "initial metadata as key=value,key=value")
	instruction := fs.String("instruction", "", "system instruction stored in the session")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compaction budget (estimated tokens) stored in the session")
	panel := fs.String("panel", "", "dynamic panel items stored in the session: time,cwd")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.usageError(err.Error())
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	options := []session.Option{}
	if *id != "" {
		if _, err := store.Get(ctx, *id); err == nil {
			return c.fail(ExitUsage, "session "+*id+" already exists")
		}
		options = append(options, session.WithID(*id))
	}
	metadata, err := parseMeta(*meta)
	if err != nil {
		return c.usageError(err.Error())
	}
	options = append(options, session.WithMetadata(metadata))
	s := session.New(options...)
	if *instruction != "" {
		s.Set(metaInstruction, *instruction)
	}
	if *compactCeiling > 0 {
		s.Set(metaCompactCeiling, strconv.Itoa(*compactCeiling))
	}
	if *panel != "" {
		if _, err := contextBuilderFor(*panel); err != nil {
			return c.failErr(err)
		}
		s.Set(metaPanel, *panel)
	}
	if err := store.Save(ctx, s); err != nil {
		return c.failErr(err)
	}
	return c.emit(session.SummaryOf(s), s.ID())
}

func (c *cli) sessionList(args []string) int {
	fs := c.newFlagSet("session list")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.usageError(err.Error())
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

func (c *cli) sessionShow(args []string) int {
	fs := c.newFlagSet("session show")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
	}
	if len(positionals) != 1 {
		return c.usageError("session show needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	s, err := store.Get(ctx, positionals[0])
	if err != nil {
		return c.failErr(err)
	}
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
		return c.usageError(err.Error())
	}
	if len(positionals) != 1 {
		return c.usageError("session fork needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	parent, err := store.Get(ctx, positionals[0])
	if err != nil {
		return c.failErr(err)
	}
	options := []session.Option{}
	if *id != "" {
		options = append(options, session.WithID(*id))
	}
	child := session.Fork(parent, options...)
	if err := store.Save(ctx, child); err != nil {
		return c.failErr(err)
	}
	return c.emit(session.SummaryOf(child), child.ID())
}

func (c *cli) sessionResume(args []string) int {
	if len(args) == 0 {
		return c.usageError("session resume needs a session id")
	}
	return c.cmdRun(append([]string{"--session", args[0]}, args[1:]...))
}

func (c *cli) sessionDelete(args []string) int {
	fs := c.newFlagSet("session delete")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
	}
	if len(positionals) != 1 {
		return c.usageError("session delete needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	store, _, err := c.store()
	if err != nil {
		return c.failErr(err)
	}
	if err := store.Delete(ctx, positionals[0]); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]any{"id": positionals[0], "deleted": true}, "deleted "+positionals[0])
}

// ---- context --------------------------------------------------------------

func (c *cli) cmdContext(args []string) int {
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

func (c *cli) loadSessionArg(fs interface{ Arg(int) string }, positionals []string) (session.Store, *session.Session, context.Context, context.CancelFunc, error) {
	if len(positionals) != 1 {
		return nil, nil, nil, nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "exactly one session id is required")
	}
	ctx, cancel := c.ctx()
	store, _, err := c.store()
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	s, err := store.Get(ctx, positionals[0])
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	return store, s, ctx, cancel, nil
}

func (c *cli) contextInspect(mode string, args []string) int {
	fs := c.newFlagSet("context " + mode)
	panel := fs.String("panel", "", "dynamic panel items to include: time,cwd (default: session setting)")
	instruction := fs.String("instruction", "", "system instruction (default: session setting)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
	}
	_, s, ctx, cancel, err := c.loadSessionArg(fs, positionals)
	if err != nil {
		return c.failErr(err)
	}
	defer cancel()
	panelSpec := *panel
	if panelSpec == "" {
		panelSpec, _ = s.Get(metaPanel)
	}
	builder, err := contextBuilderFor(panelSpec)
	if err != nil {
		return c.failErr(err)
	}
	system := *instruction
	if system == "" {
		system, _ = s.Get(metaInstruction)
	}
	report, err := modelctx.InspectSession(ctx, builder, s, system, registryFor(s).Active())
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
	fmt.Fprintf(c.stdout, "session %s\nstrategy %s (append-only)\nmessages %d · ~%d tokens · system %d bytes · panel %d bytes · tools %d\nprefix_hash %s\n",
		s.ID(), report.Strategy, report.SelectedMessages, report.ApproxTokens, report.SystemBytes, report.PanelBytes, len(report.Request.Tools), report.PrefixHash)
	if len(report.Compactions) > 0 {
		fmt.Fprintf(c.stdout, "compactions %d (last replaced %d messages)\n", len(report.Compactions), report.Compactions[len(report.Compactions)-1].ReplacedMessages)
	}
	return ExitOK
}

func (c *cli) contextCompact(args []string) int {
	fs := c.newFlagSet("context compact")
	var flags modelFlags
	c.bindModel(fs, &flags)
	keep := fs.Int("keep", 4, "number of recent messages to keep verbatim")
	summarizer := fs.String("summarizer", "truncate", "truncate (deterministic) or model (uses --model)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
	}
	store, s, ctx, cancel, err := c.loadSessionArg(fs, positionals)
	if err != nil {
		return c.failErr(err)
	}
	defer cancel()
	var summ modelctx.Summarizer
	switch *summarizer {
	case "truncate", "":
		summ = modelctx.TruncateSummarizer{}
	case "model":
		model, name, err := c.buildModel(flags, s)
		if err != nil {
			return c.failErr(err)
		}
		summ = modelctx.ModelSummarizer{Model: model, Label: "model:" + name}
	default:
		return c.usageError("unknown summarizer " + *summarizer)
	}
	result, err := modelctx.Compact(ctx, s, modelctx.CompactOptions{Keep: *keep, Summarizer: summ})
	if err != nil {
		return c.failErr(err)
	}
	if result.Replaced {
		if err := store.Save(ctx, s); err != nil {
			return c.failErr(err)
		}
	}
	human := fmt.Sprintf("session %s: %d → %d messages, ~%d → ~%d tokens", result.SessionID, result.MessagesBefore, result.MessagesAfter, result.TokensBefore, result.TokensAfter)
	if !result.Replaced {
		human = fmt.Sprintf("session %s: nothing to compact", result.SessionID)
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
	Active      bool            `json:"active"`
}

func viewOf(entry toolregistry.Entry) toolView {
	view := toolView{ID: entry.Spec.ID, Name: entry.Spec.Name, Description: entry.Spec.Description, Sequential: entry.Spec.Sequential, Active: entry.Active}
	if json.Valid(entry.Spec.InputSchema) {
		view.InputSchema = json.RawMessage(entry.Spec.InputSchema)
	}
	return view
}

func viewsOf(entries []toolregistry.Entry) []toolView {
	out := make([]toolView, 0, len(entries))
	for _, entry := range entries {
		out = append(out, viewOf(entry))
	}
	return out
}

type toolListing struct {
	SessionID string     `json:"session_id,omitempty"`
	Tools     []toolView `json:"tools"`
}

func (c *cli) cmdTools(args []string) int {
	if len(args) == 0 {
		return c.usageError("tools needs a subcommand: list, describe, active, activate, deactivate")
	}
	sub, rest := args[0], args[1:]
	fs := c.newFlagSet("tools " + sub)
	sessionID := fs.String("session", "", "session whose tool activation applies")
	positionals, err := parseInterspersed(fs, rest)
	if err != nil {
		return c.usageError(err.Error())
	}
	ctx, cancel := c.ctx()
	defer cancel()
	var store session.Store
	var s *session.Session
	if *sessionID != "" {
		store, _, err = c.store()
		if err != nil {
			return c.failErr(err)
		}
		s, err = store.Get(ctx, *sessionID)
		if err != nil {
			return c.failErr(err)
		}
	}
	reg := registryFor(s)
	switch sub {
	case "list", "ls":
		listing := toolListing{SessionID: *sessionID, Tools: viewsOf(reg.List())}
		if c.machine() {
			return c.writeJSON(listing)
		}
		for _, entry := range listing.Tools {
			state := "active"
			if !entry.Active {
				state = "inactive"
			}
			fmt.Fprintf(c.stdout, "%-12s %-8s %s\n", entry.ID, state, entry.Description)
		}
		return ExitOK
	case "active":
		var active []toolView
		for _, entry := range reg.List() {
			if entry.Active {
				active = append(active, viewOf(entry))
			}
		}
		if c.machine() {
			return c.writeJSON(toolListing{SessionID: *sessionID, Tools: active})
		}
		for _, view := range active {
			fmt.Fprintln(c.stdout, view.ID)
		}
		return ExitOK
	case "describe":
		if len(positionals) != 1 {
			return c.usageError("tools describe needs exactly one tool id")
		}
		entry, ok := reg.Describe(positionals[0])
		if !ok {
			return c.fail(ExitNotFound, "unknown tool "+positionals[0])
		}
		view := viewOf(entry)
		if c.machine() {
			return c.writeJSON(view)
		}
		fmt.Fprintf(c.stdout, "%s\n%s\nactive: %v\nschema: %s\n", view.ID, view.Description, view.Active, string(view.InputSchema))
		return ExitOK
	case "activate", "deactivate":
		if len(positionals) != 1 {
			return c.usageError("tools " + sub + " needs exactly one tool id")
		}
		if s == nil {
			return c.usageError("tools " + sub + " needs --session ID: activation is session state")
		}
		if _, ok := reg.Describe(positionals[0]); !ok {
			return c.fail(ExitNotFound, "unknown tool "+positionals[0])
		}
		if sub == "activate" {
			s.Set(metaToolPrefix+positionals[0], "")
		} else {
			s.Set(metaToolPrefix+positionals[0], "inactive")
		}
		if err := store.Save(ctx, s); err != nil {
			return c.failErr(err)
		}
		entry, _ := registryFor(s).Describe(positionals[0])
		return c.emit(map[string]any{"session_id": s.ID(), "tool": viewOf(entry)}, fmt.Sprintf("%s %sd for session %s", positionals[0], sub, s.ID()))
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
		return c.usageError(err.Error())
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
	s, err := store.Get(ctx, *sessionID)
	if err != nil {
		return c.failErr(err)
	}
	events := s.Events()
	if *kind != "" {
		filtered := events[:0]
		for _, event := range events {
			if string(event.Kind) == *kind {
				filtered = append(filtered, event)
			}
		}
		events = filtered
	}
	if c.json {
		return c.writeJSON(events)
	}
	// JSONL is the default for events: one Event per line.
	for _, event := range events {
		if err := c.writeJSONL(event); err != nil {
			return c.fail(ExitError, err.Error())
		}
	}
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
	c.bindModel(fs, &flags)
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.usageError(err.Error())
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

	store, dir, err := c.store()
	if err != nil {
		add(doctorCheck{Name: "store", OK: false, Detail: dir + ": " + err.Error()})
	} else {
		probe := session.New(session.WithID(".doctor-probe"))
		if err := store.Save(ctx, probe); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: dir + ": not writable: " + err.Error()})
		} else {
			_ = store.Delete(ctx, probe.ID())
			list, _ := store.List(ctx)
			add(doctorCheck{Name: "store", OK: true, Detail: fmt.Sprintf("%s (%d sessions)", dir, len(list))})
		}
	}
	add(doctorCheck{Name: "model.echo", OK: true, Detail: "deterministic, no credentials"})
	add(doctorCheck{Name: "model.demo", OK: true, Detail: "deterministic tool loop, no credentials"})
	gatewayPath := c.gatewayConfigPath(flags)
	if fileExists(gatewayPath) {
		if _, err := gateway.LoadYAML(gatewayPath); err != nil {
			add(doctorCheck{Name: "model.gateway", OK: false, Detail: gatewayPath + ": " + err.Error()})
		} else {
			add(doctorCheck{Name: "model.gateway", OK: true, Detail: gatewayPath + " parsed"})
		}
	} else {
		add(doctorCheck{Name: "model.gateway", OK: false, Warning: true, Detail: gatewayPath + " not found; --model gateway unavailable"})
	}
	reg := builtinRegistry()
	names := make([]string, 0)
	for _, entry := range reg.List() {
		names = append(names, entry.Spec.ID)
	}
	add(doctorCheck{Name: "tools", OK: true, Detail: strings.Join(names, ", ")})
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
