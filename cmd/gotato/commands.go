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

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
)

// ---- run ------------------------------------------------------------------

func (c *cli) cmdRun(args []string) int {
	fs := c.newFlagSet("run")
	var flags modelFlags
	c.bindModel(fs, &flags)
	sessionID := fs.String("session", "", "continue an existing session (default: a new session is created)")
	panel := fs.String("panel", "", "dynamic panel appended to the tail of each request: time,cwd (stored in the session)")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compact the session when its history exceeds this many estimated tokens (stored in the session)")
	events := fs.String("events", "", "stream runtime events to stdout: jsonl")
	continueRun := fs.Bool("continue", false, "continue the session without a new prompt")
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
	settings := sessionSettings{instruction: flags.instruction, panel: *panel, compactCeiling: *compactCeiling}
	request := service.RunRequest{SessionID: *sessionID, Agent: agent, Prompt: prompt, Continue: *continueRun, Timeout: c.timeout}
	if *sessionID == "" {
		request.Metadata, err = settings.metadata()
		if err != nil {
			return c.failErr(err)
		}
	} else {
		s, err := rt.store.Get(ctx, *sessionID)
		if err != nil {
			return c.failErr(err)
		}
		if err := settings.apply(s.Set); err != nil {
			return c.failErr(err)
		}
		if err := rt.store.Save(ctx, s); err != nil {
			return c.failErr(err)
		}
	}
	var result service.RunResult
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
		// The Run completed but the Session was not saved.
		code = ExitError
	case outcome.Status != gotato.RunCompleted:
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
		if len(rest) == 0 {
			return c.usageError("session resume needs a session id")
		}
		return c.cmdRun(append([]string{"--session", rest[0]}, rest[1:]...))
	case "delete", "rm":
		return c.sessionDelete(rest)
	default:
		return c.usageError("unknown session subcommand " + sub)
	}
}

func (c *cli) sessionCreate(args []string) int {
	fs := c.newFlagSet("session create")
	var flags modelFlags
	c.bindModel(fs, &flags)
	id := fs.String("id", "", "explicit session id (default: random)")
	meta := fs.String("meta", "", "initial metadata as key=value,key=value")
	compactCeiling := fs.Int("compact-ceiling", 0, "auto-compaction budget (estimated tokens) stored in the session")
	panel := fs.String("panel", "", "dynamic panel items stored in the session: time,cwd")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.usageError(err.Error())
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
	settings := sessionSettings{instruction: flags.instruction, panel: *panel, compactCeiling: *compactCeiling}
	if err := settings.apply(func(key, value string) { metadata[key] = value }); err != nil {
		return c.failErr(err)
	}
	var options []session.Option
	if *id != "" {
		if _, err := rt.store.Get(ctx, *id); err == nil {
			return c.fail(ExitUsage, "session "+*id+" already exists")
		}
		options = append(options, session.WithID(*id))
	}
	s, err := rt.runner.CreateSession(ctx, agent, metadata, options...)
	if err != nil {
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

func (c *cli) loadSession(args []string, name string) (*session.Session, session.Store, context.Context, context.CancelFunc, int) {
	fs := c.newFlagSet(name)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return nil, nil, nil, nil, c.usageError(err.Error())
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
	s, err := store.Get(ctx, positionals[0])
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
		return c.usageError(err.Error())
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
	var options []session.Option
	if *id != "" {
		options = append(options, session.WithID(*id))
	}
	child, err := rt.runner.Fork(ctx, positionals[0], options...)
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(session.SummaryOf(child), child.ID())
}

func (c *cli) sessionDelete(args []string) int {
	s, store, ctx, cancel, code := c.loadSession(args, "session delete")
	if code >= 0 {
		return code
	}
	defer cancel()
	if err := store.Delete(ctx, s.ID()); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]any{"id": s.ID(), "deleted": true}, "deleted "+s.ID())
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

func (c *cli) contextInspect(mode string, args []string) int {
	fs := c.newFlagSet("context " + mode)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
	}
	if len(positionals) != 1 {
		return c.usageError("context " + mode + " needs exactly one session id")
	}
	ctx, cancel := c.ctx()
	defer cancel()
	rt, err := c.newRuntime(modelFlags{})
	if err != nil {
		return c.failErr(err)
	}
	report, err := rt.runner.Inspect(ctx, positionals[0])
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
		report.SessionID, report.Strategy, report.SelectedMessages, report.ApproxTokens, report.SystemBytes, report.PanelBytes, len(report.Request.Tools), report.PrefixHash)
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
	summarizer := fs.String("summarizer", "truncate", "truncate (deterministic) or model (uses the session's agent, or --model)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return c.usageError(err.Error())
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
	var summ modelctx.Summarizer
	switch *summarizer {
	case "truncate", "":
		summ = modelctx.TruncateSummarizer{}
	case "model":
		agent, err := rt.resolveAgent(flags.model, c.getenv)
		if err != nil {
			return c.failErr(err)
		}
		if agent == "" {
			s, err := rt.store.Get(ctx, positionals[0])
			if err != nil {
				return c.failErr(err)
			}
			agent, _ = s.Get(service.MetaAgent)
		}
		spec, ok := rt.runner.Spec(agent)
		if !ok {
			return c.failErr(service.ErrUnknownAgent)
		}
		summ = modelctx.ModelSummarizer{Model: spec.Model, Label: "model:" + spec.Name}
	default:
		return c.usageError("unknown summarizer " + *summarizer)
	}
	result, err := rt.runner.Compact(ctx, positionals[0], modelctx.CompactOptions{Keep: *keep, Summarizer: summ})
	if err != nil {
		return c.failErr(err)
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

func viewOf(entry service.ToolEntry) toolView {
	view := toolView{ID: entry.Spec.ID, Name: entry.Spec.Name, Description: entry.Spec.Description, Sequential: entry.Spec.Sequential, Active: entry.Active}
	if json.Valid(entry.Spec.InputSchema) {
		view.InputSchema = json.RawMessage(entry.Spec.InputSchema)
	}
	return view
}

type toolListing struct {
	SessionID string     `json:"session_id,omitempty"`
	Agent     string     `json:"agent,omitempty"`
	Tools     []toolView `json:"tools"`
}

func (c *cli) cmdTools(args []string) int {
	if len(args) == 0 {
		return c.usageError("tools needs a subcommand: list, describe, active, activate, deactivate")
	}
	sub, rest := args[0], args[1:]
	fs := c.newFlagSet("tools " + sub)
	var flags modelFlags
	c.bindModel(fs, &flags)
	sessionID := fs.String("session", "", "session whose tool activation applies")
	positionals, err := parseInterspersed(fs, rest)
	if err != nil {
		return c.usageError(err.Error())
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
	entries, err := rt.runner.Tools(ctx, agent, *sessionID)
	if err != nil {
		return c.failErr(err)
	}
	listing := toolListing{SessionID: *sessionID, Agent: agent}
	for _, entry := range entries {
		listing.Tools = append(listing.Tools, viewOf(entry))
	}
	switch sub {
	case "list", "ls":
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
		active := toolListing{SessionID: *sessionID, Agent: agent, Tools: []toolView{}}
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
			if gotato.IsCode(err, gotato.ErrInvalidArgument) {
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
	for _, event := range events {
		if err := c.writeJSONL(event); err != nil {
			return c.fail(ExitError, err.Error())
		}
	}
	return ExitOK
}

// ---- serve ----------------------------------------------------------------

// cmdServe runs the same Runner behind the HTTP API. gRPC lives in the
// adapter/grpc module (gotato-grpc).
func (c *cli) cmdServe(args []string) int {
	fs := c.newFlagSet("serve")
	var flags modelFlags
	c.bindModel(fs, &flags)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	maxRuns := fs.Int("max-runs", 0, "maximum concurrent runs; 0 disables the bound")
	queue := fs.String("queue", "reject", "policy for a busy session: reject or wait")
	drain := fs.Duration("drain-timeout", 10*time.Second, "time to wait for active runs on shutdown before cancelling them")
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.usageError(err.Error())
	}
	rt, err := c.newRuntime(flags)
	if err != nil {
		return c.failErr(err)
	}
	// Rebuild with admission settings: the Runner is cheap to construct.
	specs := make([]service.AgentSpec, 0)
	for _, name := range rt.runner.Agents() {
		spec, _ := rt.runner.Spec(name)
		specs = append(specs, spec)
	}
	runner, err := service.New(service.Config{Store: rt.store, Specs: specs, Admission: service.Admission{MaxActiveRuns: *maxRuns, Queue: service.QueuePolicy(*queue)}})
	if err != nil {
		return c.failErr(err)
	}
	server := &http.Server{Addr: *addr, Handler: httpapi.New(runner), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	c.info("gotato serving on http://%s (store=%s agents=%s contract=%s)", *addr, rt.storeDir, strings.Join(runner.Agents(), ","), httpapi.ContractVersion)
	if c.machine() {
		c.writeJSONL(map[string]any{"kind": "serving", "addr": *addr, "store": rt.storeDir, "agents": runner.Agents(), "contract": httpapi.ContractVersion})
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return c.fail(ExitError, err.Error())
		}
		return ExitOK
	case <-ctx.Done():
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), *drain)
	defer cancel()
	if err := runner.Drain(drainCtx); err != nil {
		c.info("drain: %v", err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdownCtx)
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

	rt, err := c.newRuntime(flags)
	if err != nil {
		add(doctorCheck{Name: "store", OK: false, Detail: err.Error()})
	} else {
		probe := session.New(session.WithID(".doctor-probe"))
		if err := rt.store.Save(ctx, probe); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": not writable: " + err.Error()})
		} else {
			_ = rt.store.Delete(ctx, probe.ID())
			list, _ := rt.store.List(ctx)
			add(doctorCheck{Name: "store", OK: true, Detail: fmt.Sprintf("%s (%d sessions)", rt.storeDir, len(list))})
		}
		add(doctorCheck{Name: "agent.echo", OK: true, Detail: "deterministic, no credentials"})
		add(doctorCheck{Name: "agent.demo", OK: true, Detail: "deterministic tool loop, no credentials"})
		if rt.gatewayErr == nil {
			spec, _ := rt.runner.Spec("gateway")
			add(doctorCheck{Name: "agent.gateway", OK: true, Detail: rt.gatewayCfg + " → " + spec.ModelName})
		} else if fileExists(rt.gatewayCfg) {
			add(doctorCheck{Name: "agent.gateway", OK: false, Detail: rt.gatewayErr.Error()})
		} else {
			add(doctorCheck{Name: "agent.gateway", OK: false, Warning: true, Detail: rt.gatewayCfg + " not found; --model gateway unavailable"})
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
