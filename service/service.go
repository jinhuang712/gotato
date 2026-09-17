// Package service turns the runtime into a service: a store of Sessions and
// Agents that are created for one Run and discarded afterwards.
//
// The service owns no Agent semantics. For every Run it loads the Session,
// takes the Session's lock, builds an Agent from an AgentSpec exactly as an
// application would (WithTranscript, WithContextBuilder, WithToolSource,
// session.Record, modelctx.AutoCompact), runs it, closes it, and saves the
// Session. Continuity lives in the Store, so any process holding the Store
// can serve any Session; an Agent is never the unit of identity.
//
//	runner, _ := service.New(service.Config{
//	    Store: store,
//	    Specs: []service.AgentSpec{{Name: "default", Model: model, Tools: tools}},
//	})
//	result, _ := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hello"})
//
// Package httpapi exposes a Runner over HTTP; the gRPC adapter exposes the
// same Runner; the gotato CLI drives one in-process. All three are thin.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// Session metadata keys the service reads. They are ordinary application
// metadata from the runtime's point of view and may be set by any client.
const (
	MetaAgent          = "gotato.agent"           // AgentSpec name used for this Session
	MetaInstruction    = "gotato.instruction"     // overrides AgentSpec.Instruction
	MetaPanel          = "gotato.panel"           // comma list: time,cwd
	MetaCompactCeiling = "gotato.compact_ceiling" // estimated-token budget for auto compaction
	MetaModel          = "gotato.model"           // informational: model name last used
	// MetaToolPrefix + <tool id> = "inactive" hides one of the Spec's Tools
	// from the Model for this Session. Tool activation is Session state.
	MetaToolPrefix = "gotato.tool."
)

// ToolEntry describes one Tool of a Spec as seen by a Session.
type ToolEntry struct {
	Spec   gotato.ToolSpec `json:"spec"`
	Active bool            `json:"active"`
}

// AgentSpec is the reusable configuration the service instantiates per Run.
// Sharing a Model client between Specs and Runs is expected; nothing here is
// mutable Run state.
type AgentSpec struct {
	Name        string
	Model       gotato.Model
	ModelName   string
	Instruction string
	Tools       []gotato.Tool
	ToolSources []gotato.ToolSource
	// ContextBuilder defaults to modelctx.FullHistory(); a Session panel is
	// layered on top of it.
	ContextBuilder gotato.ContextBuilder
	Extensions     []any
	Limits         *gotato.CoreLimits
	// Compact is the default auto-compaction budget; a Session may override
	// the ceiling through MetaCompactCeiling. Zero disables.
	Compact modelctx.CompactPolicy
	// Options are appended verbatim for anything the fields above do not
	// cover.
	Options []gotato.Option
}

// QueuePolicy decides what happens when a Session is busy.
type QueuePolicy string

const (
	// RejectWhileBusy fails a second concurrent Run on the same Session with
	// ErrBusy (HTTP 409).
	RejectWhileBusy QueuePolicy = "reject"
	// WaitWhileBusy queues the Run until the Session is free or the caller's
	// context ends.
	WaitWhileBusy QueuePolicy = "wait"
)

// Admission bounds concurrent work. Zero values disable a bound.
type Admission struct {
	MaxActiveRuns int
	Queue         QueuePolicy
}

// Config configures a Runner.
type Config struct {
	Store     session.Store
	Specs     []AgentSpec
	Admission Admission
	Now       func() time.Time
}

// ErrBusy is returned when a Session already has a Run in flight and the
// policy is RejectWhileBusy.
var ErrBusy = gotato.ErrorOf(gotato.ErrBusy, "service: session has a run in flight")

// ErrCapacity is returned when MaxActiveRuns is reached.
var ErrCapacity = gotato.ErrorOf(gotato.ErrLimitExceeded, "service: maximum active runs reached")

// ErrUnknownAgent is returned for an unregistered AgentSpec name.
var ErrUnknownAgent = gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown agent")

// ErrDraining is returned once Drain has started.
var ErrDraining = gotato.ErrorOf(gotato.ErrInvalidState, "service: draining")

// Runner serves Runs against Sessions.
type Runner struct {
	store     session.Store
	specs     map[string]AgentSpec
	order     []string
	admission Admission
	now       func() time.Time

	locks *sessionLocks

	mu        sync.Mutex
	active    map[gotato.RunID]activeRun
	bySession map[string]gotato.RunID
	inflight  int
	draining  bool
	idle      *sync.Cond
}

type activeRun struct {
	sessionID string
	cancel    func()
}

// New validates the configuration and creates a Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Store == nil {
		return nil, errors.New("service: Store is required")
	}
	if len(cfg.Specs) == 0 {
		return nil, errors.New("service: at least one AgentSpec is required")
	}
	r := &Runner{
		store:     cfg.Store,
		specs:     map[string]AgentSpec{},
		admission: cfg.Admission,
		now:       cfg.Now,
		locks:     newSessionLocks(),
		active:    map[gotato.RunID]activeRun{},
		bySession: map[string]gotato.RunID{},
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.admission.Queue == "" {
		r.admission.Queue = RejectWhileBusy
	}
	if r.admission.Queue != RejectWhileBusy && r.admission.Queue != WaitWhileBusy {
		return nil, fmt.Errorf("service: unknown queue policy %q", r.admission.Queue)
	}
	r.idle = sync.NewCond(&r.mu)
	for _, spec := range cfg.Specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return nil, errors.New("service: AgentSpec.Name is empty")
		}
		if spec.Model == nil {
			return nil, fmt.Errorf("service: AgentSpec %q has no Model", name)
		}
		if _, dup := r.specs[name]; dup {
			return nil, fmt.Errorf("service: duplicate AgentSpec %q", name)
		}
		spec.Name = name
		r.specs[name] = spec
		r.order = append(r.order, name)
	}
	return r, nil
}

// Store returns the Session store.
func (r *Runner) Store() session.Store { return r.store }

// Agents lists the registered AgentSpec names; the first is the default.
func (r *Runner) Agents() []string { return append([]string(nil), r.order...) }

// Spec returns one AgentSpec.
func (r *Runner) Spec(name string) (AgentSpec, bool) {
	if name == "" {
		name = r.order[0]
	}
	spec, ok := r.specs[name]
	return spec, ok
}

// ActiveRuns reports Runs in flight.
func (r *Runner) ActiveRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflight
}

// CreateSession creates and saves a Session bound to an AgentSpec.
func (r *Runner) CreateSession(ctx context.Context, agent string, metadata map[string]string, options ...session.Option) (*session.Session, error) {
	spec, ok := r.Spec(agent)
	if !ok {
		return nil, ErrUnknownAgent
	}
	s := session.New(options...)
	for key, value := range metadata {
		s.Set(key, value)
	}
	s.Set(MetaAgent, spec.Name)
	if err := r.store.Save(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// RunRequest is one unit of work.
type RunRequest struct {
	// SessionID names the Session. Empty creates a new Session.
	SessionID string
	// Agent selects the AgentSpec for a new Session; an existing Session
	// keeps the Spec it was created with unless Agent is set.
	Agent string
	// Prompt is the user input. Empty with Continue=true resumes the loop.
	Prompt   string
	Continue bool
	// Metadata is applied to a new Session.
	Metadata map[string]string
	// Timeout bounds this Run; the Run settles as deadline_exceeded. Zero
	// keeps the AgentSpec's RunDeadline.
	Timeout time.Duration
}

// RunResult is the outcome of one Run plus the Session state it left.
type RunResult struct {
	SessionID string           `json:"session_id"`
	Agent     string           `json:"agent"`
	Model     string           `json:"model,omitempty"`
	Result    gotato.RunResult `json:"result"`
	FinalText string           `json:"final_text,omitempty"`
	Compacted bool             `json:"compacted"`
	Messages  int              `json:"messages"`
	Events    int              `json:"events"`
}

// Run executes one Run synchronously.
func (r *Runner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	return r.run(ctx, request, nil)
}

// StreamRun executes one Run and delivers every runtime Event to sink as it
// is produced. A sink error is advisory: it stops delivery, not the Run.
func (r *Runner) StreamRun(ctx context.Context, request RunRequest, sink func(gotato.Event) error) (RunResult, error) {
	return r.run(ctx, request, sink)
}

func (r *Runner) run(ctx context.Context, request RunRequest, sink func(gotato.Event) error) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !request.Continue && strings.TrimSpace(request.Prompt) == "" {
		return RunResult{}, gotato.ErrorOf(gotato.ErrInvalidArgument, "service: prompt is required")
	}
	if err := r.admit(); err != nil {
		return RunResult{}, err
	}
	defer r.release()

	// Load or create the Session.
	var s *session.Session
	var err error
	if request.SessionID == "" {
		s, err = r.CreateSession(ctx, request.Agent, request.Metadata)
	} else {
		s, err = r.store.Get(ctx, request.SessionID)
	}
	if err != nil {
		return RunResult{}, err
	}

	// One Run per Session at a time.
	if err := r.locks.acquire(ctx, s.ID(), r.admission.Queue == WaitWhileBusy); err != nil {
		return RunResult{}, err
	}
	defer r.locks.release(s.ID())
	if request.SessionID != "" {
		// Re-read under the lock: another Run may have saved meanwhile.
		if fresh, err := r.store.Get(ctx, s.ID()); err == nil {
			s = fresh
		}
	}

	agentName := request.Agent
	if agentName == "" {
		agentName, _ = s.Get(MetaAgent)
	}
	spec, ok := r.Spec(agentName)
	if !ok {
		return RunResult{}, ErrUnknownAgent
	}
	s.Set(MetaAgent, spec.Name)
	if spec.ModelName != "" {
		s.Set(MetaModel, spec.ModelName)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tracker := &runTracker{runner: r, sessionID: s.ID()}
	if request.Timeout > 0 {
		limits := gotato.DefaultLimits()
		if spec.Limits != nil {
			limits = *spec.Limits
		}
		limits.RunDeadline = request.Timeout
		spec.Limits = &limits
	}
	agent, auto, err := r.buildAgent(spec, s, sink, tracker)
	if err != nil {
		return RunResult{}, err
	}
	// Cancellation aborts the Run inside the Agent so Prompt still returns
	// the settled (cancelled) result; cancelling the caller's context would
	// abandon the result instead.
	tracker.cancel = func() {
		if controllable, ok := agent.(gotato.ControllableAgent); ok {
			controllable.Abort()
			return
		}
		cancel()
	}

	var result gotato.RunResult
	var runErr error
	if request.Continue {
		controllable, ok := agent.(gotato.ControllableAgent)
		if !ok {
			runErr = gotato.ErrorOf(gotato.ErrNotSupported, "service: agent does not support continue")
		} else {
			result, runErr = controllable.Continue(runCtx)
		}
	} else {
		result, runErr = agent.Prompt(runCtx, gotato.UserMessage(request.Prompt))
	}
	tracker.untrack()
	_ = agent.Close(context.Background())

	if saveErr := r.store.Save(context.Background(), s); saveErr != nil && runErr == nil {
		runErr = saveErr
	}
	out := RunResult{
		SessionID: s.ID(),
		Agent:     spec.Name,
		Model:     spec.ModelName,
		Result:    result,
		Compacted: auto != nil && compacted(auto),
		Messages:  s.Len(),
		Events:    len(s.Events()),
	}
	if result.FinalMessage != nil {
		out.FinalText = gotato.TextOf(*result.FinalMessage)
	}
	if runErr != nil && out.Result.Error == nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(runErr, &runtimeErr) {
			out.Result.Error = runtimeErr
		} else {
			out.Result.Error = gotato.ErrorOf(gotato.ErrInternalInvariant, runErr.Error())
		}
		if out.Result.Status == "" {
			out.Result.Status = gotato.RunFailed
		}
	}
	return out, runErr
}

func compacted(auto *modelctx.AutoCompactor) bool {
	_, runs := auto.Last()
	return runs > 0
}

// buildAgent composes the runtime for one Run from the Spec and the Session's
// own settings.
func (r *Runner) buildAgent(spec AgentSpec, s *session.Session, sink func(gotato.Event) error, tracker *runTracker) (gotato.Agent, *modelctx.AutoCompactor, error) {
	instruction := spec.Instruction
	if override, ok := s.Get(MetaInstruction); ok && override != "" {
		instruction = override
	}
	builder := spec.ContextBuilder
	if builder == nil {
		builder = modelctx.FullHistory()
	}
	if panelSpec, _ := s.Get(MetaPanel); panelSpec != "" {
		panel, err := PanelFromSpec(panelSpec, r.now)
		if err != nil {
			return nil, nil, err
		}
		builder = modelctx.WithPanel(builder, panel)
	}
	policy := spec.Compact
	if raw, ok := s.Get(MetaCompactCeiling); ok {
		if ceiling, err := strconv.Atoi(raw); err == nil && ceiling > 0 {
			policy.Ceiling = ceiling
			policy.Floor = 0
		}
	}
	var auto *modelctx.AutoCompactor
	extensions := []any{session.Record(s)}
	if tracker != nil {
		extensions = append(extensions, tracker)
	}
	if policy.Ceiling > 0 {
		auto = modelctx.AutoCompact(s, policy)
		extensions = append(extensions, auto)
	}
	if sink != nil {
		extensions = append(extensions, sinkObserver{fn: sink})
	}
	extensions = append(extensions, spec.Extensions...)

	options := []gotato.Option{
		gotato.WithModel(spec.Model),
		gotato.WithInstruction(instruction),
		gotato.WithTranscript(s),
		gotato.WithContextBuilder(builder),
		gotato.WithExtensions(extensions...),
	}
	options = append(options, gotato.WithToolSource(&sessionTools{spec: spec, session: s}))
	if spec.Limits != nil {
		options = append(options, gotato.WithLimits(*spec.Limits))
	}
	options = append(options, spec.Options...)
	agent, err := gotato.NewAgent(options...)
	if err != nil {
		return nil, nil, err
	}
	return agent, auto, nil
}

type sinkObserver struct{ fn func(gotato.Event) error }

func (o sinkObserver) Observe(_ context.Context, event gotato.Event) error { return o.fn(event) }
func (o sinkObserver) Advisory() bool                                      { return true }

// PanelFromSpec builds the dynamic panel for a comma-separated list of items:
// "time" (RFC 3339 UTC) and "cwd" (the service process's working directory).
func PanelFromSpec(spec string, now func() time.Time) (modelctx.PanelFunc, error) {
	if now == nil {
		now = time.Now
	}
	var items []string
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item != "time" && item != "cwd" {
			return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown panel item "+item+" (use time, cwd)")
		}
		items = append(items, item)
	}
	return func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error) {
		blocks := make([]gotato.Block, 0, len(items))
		for _, item := range items {
			switch item {
			case "time":
				blocks = append(blocks, modelctx.Time(now()))
			case "cwd":
				if wd, err := os.Getwd(); err == nil {
					blocks = append(blocks, modelctx.Text("cwd", wd))
				}
			}
		}
		return blocks, nil
	}, nil
}

// Inspect reports the Context a Run against the Session would send now.
func (r *Runner) Inspect(ctx context.Context, sessionID string) (modelctx.Report, error) {
	s, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return modelctx.Report{}, err
	}
	agentName, _ := s.Get(MetaAgent)
	spec, ok := r.Spec(agentName)
	if !ok {
		return modelctx.Report{}, ErrUnknownAgent
	}
	instruction := spec.Instruction
	if override, ok := s.Get(MetaInstruction); ok && override != "" {
		instruction = override
	}
	builder := spec.ContextBuilder
	if builder == nil {
		builder = modelctx.FullHistory()
	}
	if panelSpec, _ := s.Get(MetaPanel); panelSpec != "" {
		panel, err := PanelFromSpec(panelSpec, r.now)
		if err != nil {
			return modelctx.Report{}, err
		}
		builder = modelctx.WithPanel(builder, panel)
	}
	return modelctx.InspectSession(ctx, builder, s, instruction, r.visibleTools(spec, s))
}

func (r *Runner) visibleTools(spec AgentSpec, s *session.Session) []gotato.ToolSpec {
	var specs []gotato.ToolSpec
	for _, tool := range (&sessionTools{spec: spec, session: s}).Tools() {
		specs = append(specs, tool.Spec())
	}
	return specs
}

// sessionTools is the ToolSource an Agent sees for one Session: the Spec's
// static Tools and ToolSources minus the ones the Session deactivated. It is
// re-read at every Turn boundary like any ToolSource.
type sessionTools struct {
	spec    AgentSpec
	session *session.Session
}

func (t *sessionTools) Tools() []gotato.Tool {
	var tools []gotato.Tool
	for _, tool := range t.spec.Tools {
		if t.active(tool.Spec().ID) {
			tools = append(tools, tool)
		}
	}
	for _, source := range t.spec.ToolSources {
		for _, tool := range source.Tools() {
			if t.active(tool.Spec().ID) {
				tools = append(tools, tool)
			}
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Spec().ID < tools[j].Spec().ID })
	return tools
}

func (t *sessionTools) active(id string) bool {
	if t.session == nil {
		return true
	}
	value, _ := t.session.Get(MetaToolPrefix + id)
	return value != "inactive"
}

// Tools describes the Tool surface of an AgentSpec as a Session sees it.
// sessionID may be empty for the Spec's default surface.
func (r *Runner) Tools(ctx context.Context, agent, sessionID string) ([]ToolEntry, error) {
	var s *session.Session
	if sessionID != "" {
		var err error
		s, err = r.store.Get(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if agent == "" {
			agent, _ = s.Get(MetaAgent)
		}
	}
	spec, ok := r.Spec(agent)
	if !ok {
		return nil, ErrUnknownAgent
	}
	all := &sessionTools{spec: spec}
	filter := &sessionTools{spec: spec, session: s}
	entries := make([]ToolEntry, 0)
	for _, tool := range all.Tools() {
		entries = append(entries, ToolEntry{Spec: tool.Spec(), Active: filter.active(tool.Spec().ID)})
	}
	return entries, nil
}

// SetToolActive records a Session-level activation change and saves it.
func (r *Runner) SetToolActive(ctx context.Context, sessionID, toolID string, active bool) (ToolEntry, error) {
	s, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return ToolEntry{}, err
	}
	agent, _ := s.Get(MetaAgent)
	spec, ok := r.Spec(agent)
	if !ok {
		return ToolEntry{}, ErrUnknownAgent
	}
	var found *gotato.ToolSpec
	for _, tool := range (&sessionTools{spec: spec}).Tools() {
		if tool.Spec().ID == toolID {
			ts := tool.Spec()
			found = &ts
			break
		}
	}
	if found == nil {
		return ToolEntry{}, gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown tool "+toolID)
	}
	if active {
		s.Set(MetaToolPrefix+toolID, "")
	} else {
		s.Set(MetaToolPrefix+toolID, "inactive")
	}
	if err := r.store.Save(ctx, s); err != nil {
		return ToolEntry{}, err
	}
	return ToolEntry{Spec: *found, Active: active}, nil
}

// Compact compacts a Session explicitly, under the Session lock.
func (r *Runner) Compact(ctx context.Context, sessionID string, opts modelctx.CompactOptions) (modelctx.Result, error) {
	if err := r.locks.acquire(ctx, sessionID, r.admission.Queue == WaitWhileBusy); err != nil {
		return modelctx.Result{}, err
	}
	defer r.locks.release(sessionID)
	s, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return modelctx.Result{}, err
	}
	result, err := modelctx.Compact(ctx, s, opts)
	if err != nil {
		return modelctx.Result{}, err
	}
	if result.Replaced {
		if err := r.store.Save(ctx, s); err != nil {
			return modelctx.Result{}, err
		}
	}
	return result, nil
}

// Fork creates a new Session from an existing one.
func (r *Runner) Fork(ctx context.Context, sessionID string, options ...session.Option) (*session.Session, error) {
	parent, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	child := session.Fork(parent, options...)
	if err := r.store.Save(ctx, child); err != nil {
		return nil, err
	}
	return child, nil
}

// CancelRun cancels an active Run. It reports invalid_state when the Run is
// not active rather than pretending to cancel.
func (r *Runner) CancelRun(ctx context.Context, runID gotato.RunID) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	entry, ok := r.active[runID]
	r.mu.Unlock()
	if !ok {
		return gotato.ErrorOf(gotato.ErrInvalidState, "service: run is not active")
	}
	entry.cancel()
	return nil
}

// Drain stops admitting Runs and waits for active ones. When ctx ends first,
// the remaining Runs are cancelled and Drain returns ctx.Err() after they
// settle.
func (r *Runner) Drain(ctx context.Context) error {
	r.mu.Lock()
	r.draining = true
	r.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		r.mu.Lock()
		for r.inflight > 0 {
			r.idle.Wait()
		}
		r.mu.Unlock()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		for _, entry := range r.active {
			entry.cancel()
		}
		r.mu.Unlock()
		<-finished
		return ctx.Err()
	}
}

func (r *Runner) admit() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining {
		return ErrDraining
	}
	if r.admission.MaxActiveRuns > 0 && r.inflight >= r.admission.MaxActiveRuns {
		return ErrCapacity
	}
	r.inflight++
	return nil
}

func (r *Runner) release() {
	r.mu.Lock()
	r.inflight--
	if r.inflight == 0 {
		r.idle.Broadcast()
	}
	r.mu.Unlock()
}

// runTracker is an advisory EventObserver that registers the Run for
// cancellation the moment its RunID is known (agent_start).
type runTracker struct {
	runner    *Runner
	sessionID string
	cancel    func()
	runID     gotato.RunID
}

func (t *runTracker) Advisory() bool { return true }

func (t *runTracker) Observe(_ context.Context, event gotato.Event) error {
	if event.Kind != gotato.EventAgentStart || t.runID != "" {
		return nil
	}
	t.runID = event.RunID
	t.runner.mu.Lock()
	t.runner.active[event.RunID] = activeRun{sessionID: t.sessionID, cancel: t.cancel}
	t.runner.bySession[t.sessionID] = event.RunID
	t.runner.mu.Unlock()
	return nil
}

func (t *runTracker) untrack() {
	t.runner.mu.Lock()
	if t.runID != "" {
		delete(t.runner.active, t.runID)
	}
	if t.runner.bySession[t.sessionID] == t.runID {
		delete(t.runner.bySession, t.sessionID)
	}
	t.runner.mu.Unlock()
}

// CancelSession cancels the Run in flight on a Session, if any.
func (r *Runner) CancelSession(ctx context.Context, sessionID string) error {
	r.mu.Lock()
	runID, ok := r.bySession[sessionID]
	r.mu.Unlock()
	if !ok {
		return gotato.ErrorOf(gotato.ErrInvalidState, "service: session has no active run")
	}
	return r.CancelRun(ctx, runID)
}
