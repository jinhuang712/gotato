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

// ErrNotPersisted reports that a settled Run could not be saved. The Run
// outcome is still returned; callers must treat the Session state as lost.
var ErrNotPersisted = errors.New("service: session was not persisted")

// Runner serves Runs against Sessions.
type Runner struct {
	store     session.Store
	specs     map[string]AgentSpec
	order     []string
	admission Admission
	now       func() time.Time

	locks *sessionLocks

	mu       sync.Mutex
	handles  map[string][]*runHandle
	byRun    map[gotato.RunID]*runHandle
	inflight int
	draining bool
	idle     *sync.Cond
}

// runHandle is one admitted Run. It exists from admission until the Run
// settles, so a Run still waiting on the Session lock is visible to
// CancelSession and to Drain instead of being an untracked inflight slot.
type runHandle struct {
	sessionID string

	mu     sync.Mutex
	runID  gotato.RunID
	abort  func()
	cancel context.CancelFunc
}

// attachAbort installs the started-Run cancellation path. Until it is
// installed, cancel stops the pre-start wait instead.
func (h *runHandle) attachAbort(abort func()) {
	h.mu.Lock()
	h.abort = abort
	h.mu.Unlock()
}

// attachRunID records the RunID the Agent assigned. The first one wins.
func (h *runHandle) attachRunID(runID gotato.RunID) {
	h.mu.Lock()
	if h.runID == "" {
		h.runID = runID
	}
	h.mu.Unlock()
}

// stop aborts a started Run, or the pre-start wait when none started yet.
func (h *runHandle) stop() {
	h.mu.Lock()
	abort := h.abort
	cancel := h.cancel
	h.mu.Unlock()
	if abort != nil {
		abort()
		return
	}
	cancel()
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
		handles:   map[string][]*runHandle{},
		byRun:     map[gotato.RunID]*runHandle{},
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

	// Register the Run before it can wait on the Session lock, so Drain and
	// CancelSession reach it for the whole admitted lifetime.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	handle := &runHandle{sessionID: s.ID(), cancel: cancelRun}
	r.registerHandle(handle)
	defer r.unregisterHandle(handle)

	// One Run per Session at a time. Every Session mutation goes through
	// withSessionLock; the admission policy decides the busy behavior.
	var out RunResult
	var runErr error
	started := false
	created := request.SessionID == ""
	lockErr := r.withSessionLock(runCtx, s.ID(), func() error {
		if request.SessionID != "" {
			// Re-read under the lock: another Run may have saved meanwhile,
			// and a Session deleted in between must not be resurrected from
			// the stale copy.
			fresh, err := r.store.Get(ctx, s.ID())
			if err != nil {
				return err
			}
			s = fresh
		}

		agentName := request.Agent
		if agentName == "" {
			agentName, _ = s.Get(MetaAgent)
		}
		spec, ok := r.Spec(agentName)
		if !ok {
			return ErrUnknownAgent
		}
		s.Set(MetaAgent, spec.Name)
		if spec.ModelName != "" {
			s.Set(MetaModel, spec.ModelName)
		}

		tracker := &runTracker{runner: r, handle: handle}
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
			return err
		}
		started = true
		// Cancellation aborts the Run inside the Agent so Prompt still
		// returns the settled (cancelled) result; cancelling the caller's
		// context would abandon the result instead.
		handle.attachAbort(func() {
			if controllable, ok := agent.(gotato.ControllableAgent); ok {
				controllable.Abort()
				return
			}
			cancelRun()
		})

		var result gotato.RunResult
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
		_ = agent.Close(context.Background())

		saveErr := r.store.Save(context.Background(), s)
		if saveErr != nil && runErr == nil {
			// The Run succeeded but its Session state is lost: report it as a
			// persistence failure instead of a clean success.
			runErr = fmt.Errorf("%w: %v", ErrNotPersisted, saveErr)
		}
		out = RunResult{
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
				out.Result.Status = runStatusForError(runErr)
			}
		}
		return nil
	})
	if lockErr != nil {
		if created && !started {
			// A one-shot Run that never started must not leave an empty
			// Session behind.
			_ = r.store.Delete(context.Background(), s.ID())
		}
		if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
			// Cancelled while waiting for the Session lock: report a settled
			// cancelled Run rather than a bare error.
			return cancelledRunResult(s.ID(), request.Agent, lockErr), lockErr
		}
		return RunResult{}, lockErr
	}
	return out, runErr
}

// cancelledRunResult reports a Run cancelled before its Agent started as a
// settled cancelled Run.
func cancelledRunResult(sessionID, agent string, err error) RunResult {
	code := gotato.ErrCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		code = gotato.ErrDeadlineExceeded
	}
	return RunResult{
		SessionID: sessionID,
		Agent:     agent,
		Result:    gotato.RunResult{Status: runStatusForError(err), Error: gotato.ErrorOf(code, err.Error())},
	}
}

// withSessionLock runs fn while holding the Session's single-flight lock. The
// admission queue policy decides whether a busy Session fails (ErrBusy) or the
// caller waits. Every Session mutation goes through it: Runs, compaction,
// tool activation, and deletion.
func (r *Runner) withSessionLock(ctx context.Context, sessionID string, fn func() error) error {
	if err := r.locks.acquire(ctx, sessionID, r.admission.Queue == WaitWhileBusy); err != nil {
		return err
	}
	defer r.locks.release(sessionID)
	return fn()
}

// runStatusForError classifies a terminal error when the Agent itself did not
// report a status: a caller-cancelled context is a cancelled Run, not a
// failure, and the Session's Run record says the same.
func runStatusForError(err error) gotato.RunStatus {
	switch {
	case errors.Is(err, context.Canceled), gotato.IsCode(err, gotato.ErrCancelled):
		return gotato.RunCanceled
	case errors.Is(err, context.DeadlineExceeded), gotato.IsCode(err, gotato.ErrDeadlineExceeded):
		return gotato.RunDeadlineExceeded
	default:
		return gotato.RunFailed
	}
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

// SetToolActive records a Session-level activation change and saves it under
// the Session lock, so it cannot lose a concurrent Run or compaction save.
func (r *Runner) SetToolActive(ctx context.Context, sessionID, toolID string, active bool) (ToolEntry, error) {
	var found *gotato.ToolSpec
	err := r.withSessionLock(ctx, sessionID, func() error {
		s, err := r.store.Get(ctx, sessionID)
		if err != nil {
			return err
		}
		agent, _ := s.Get(MetaAgent)
		spec, ok := r.Spec(agent)
		if !ok {
			return ErrUnknownAgent
		}
		for _, tool := range (&sessionTools{spec: spec}).Tools() {
			if tool.Spec().ID == toolID {
				ts := tool.Spec()
				found = &ts
				break
			}
		}
		if found == nil {
			return gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown tool "+toolID)
		}
		if active {
			s.Set(MetaToolPrefix+toolID, "")
		} else {
			s.Set(MetaToolPrefix+toolID, "inactive")
		}
		return r.store.Save(ctx, s)
	})
	if err != nil {
		return ToolEntry{}, err
	}
	return ToolEntry{Spec: *found, Active: active}, nil
}

// Compact compacts a Session explicitly, under the Session lock.
func (r *Runner) Compact(ctx context.Context, sessionID string, opts modelctx.CompactOptions) (modelctx.Result, error) {
	var result modelctx.Result
	err := r.withSessionLock(ctx, sessionID, func() error {
		s, err := r.store.Get(ctx, sessionID)
		if err != nil {
			return err
		}
		result, err = modelctx.Compact(ctx, s, opts)
		if err != nil {
			return err
		}
		if result.Replaced {
			return r.store.Save(ctx, s)
		}
		return nil
	})
	if err != nil {
		return modelctx.Result{}, err
	}
	return result, nil
}

// DeleteSession removes a Session under the Session lock, so a Run in flight
// is never resurrected by a delete or vice versa.
func (r *Runner) DeleteSession(ctx context.Context, sessionID string) error {
	return r.withSessionLock(ctx, sessionID, func() error {
		return r.store.Delete(ctx, sessionID)
	})
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
	handle := r.byRun[runID]
	r.mu.Unlock()
	if handle == nil {
		return gotato.ErrorOf(gotato.ErrInvalidState, "service: run is not active")
	}
	handle.stop()
	return nil
}

// CancelSession cancels the Run in flight on a Session, if any.
func (r *Runner) CancelSession(ctx context.Context, sessionID string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !r.cancelSession(sessionID) {
		return gotato.ErrorOf(gotato.ErrInvalidState, "service: session has no active run")
	}
	return nil
}

// cancelSession cancels the started Run for a Session, or the oldest Run still
// waiting for its lock when none has started.
func (r *Runner) cancelSession(sessionID string) bool {
	r.mu.Lock()
	waiting := append([]*runHandle(nil), r.handles[sessionID]...)
	r.mu.Unlock()
	var chosen *runHandle
	for _, handle := range waiting {
		handle.mu.Lock()
		started := handle.runID != ""
		handle.mu.Unlock()
		if started {
			chosen = handle
			break
		}
	}
	if chosen == nil && len(waiting) > 0 {
		chosen = waiting[0]
	}
	if chosen == nil {
		return false
	}
	chosen.stop()
	return true
}

// cancelAll cancels every admitted Run: started Runs through the Agent, Runs
// still waiting for a Session lock through their context.
func (r *Runner) cancelAll() {
	r.mu.Lock()
	var all []*runHandle
	for _, list := range r.handles {
		all = append(all, list...)
	}
	r.mu.Unlock()
	for _, handle := range all {
		handle.stop()
	}
}

// registerHandle records an admitted Run for the lifetime of the call.
func (r *Runner) registerHandle(handle *runHandle) {
	r.mu.Lock()
	r.handles[handle.sessionID] = append(r.handles[handle.sessionID], handle)
	r.mu.Unlock()
}

// unregisterHandle drops an admitted Run and its RunID mapping.
func (r *Runner) unregisterHandle(handle *runHandle) {
	r.mu.Lock()
	list := r.handles[handle.sessionID]
	for i, candidate := range list {
		if candidate == handle {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.handles, handle.sessionID)
	} else {
		r.handles[handle.sessionID] = list
	}
	handle.mu.Lock()
	runID := handle.runID
	handle.mu.Unlock()
	if runID != "" && r.byRun[runID] == handle {
		delete(r.byRun, runID)
	}
	r.mu.Unlock()
}

// attachRunID maps the Agent-assigned RunID to the admitted Run so CancelRun
// can find it while it is in flight.
func (r *Runner) attachRunID(handle *runHandle, runID gotato.RunID) {
	r.mu.Lock()
	handle.attachRunID(runID)
	r.byRun[runID] = handle
	r.mu.Unlock()
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
		r.cancelAll()
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

// runTracker is an advisory EventObserver that attaches the RunID the Agent
// assigned to the admitted Run handle as soon as agent_start fires.
type runTracker struct {
	runner *Runner
	handle *runHandle
}

func (t *runTracker) Advisory() bool { return true }

func (t *runTracker) Observe(_ context.Context, event gotato.Event) error {
	if event.Kind != gotato.EventAgentStart {
		return nil
	}
	t.runner.attachRunID(t.handle, event.RunID)
	return nil
}
