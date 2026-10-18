// Package service turns the runtime into a service: named Agents, a Store of
// Session Records, and a live Session built for each Run.
//
// Session semantics stay in the runtime. For every Run the Runner loads the
// Session's Record, takes the Session's lock, builds a live gotato.Session
// from the Agent exactly as an application would (WithTranscript,
// WithContextBuilder, WithToolSource, session.NewRecorder,
// modelctx.AutoCompact), runs it, closes it, and saves the Record.
// Continuity lives in the Store, so any process holding the Store can serve
// any Session.
//
//	runner, _ := service.New(service.Config{
//	    Store:  store,
//	    Agents: []service.Agent{{Name: "default", Model: model, Tools: tools}},
//	})
//	result, _ := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hello"})
//
// Package httpapi exposes a Runner over HTTP; the gRPC adapter exposes the
// same Runner; the gotato CLI drives one in-process. All three are thin.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// persistTimeout bounds the terminal Session write and cleanup delete so a
// stalled Store cannot pin a settled Run.
const persistTimeout = 10 * time.Second

// Session metadata keys the service reads. They are ordinary application
// metadata from the runtime's point of view and may be set by any client.
const (
	MetaAgent          = "gotato.agent"           // Agent name used for this Session
	MetaInstruction    = "gotato.instruction"     // overrides Agent.Instruction
	MetaCompactCeiling = "gotato.compact_ceiling" // estimated-token budget for auto compaction
	MetaModel          = "gotato.model"           // informational: model name last used
	MetaCacheKey       = "gotato.cache_key"       // the prompt cache key; a fork keeps its parent's
	// MetaToolPrefix + <tool id> = "inactive" hides one of the Agent's Tools
	// from the Model for this Session. Tool activation is Session state.
	MetaToolPrefix = "gotato.tool."
)

// ToolEntry describes one Tool of an Agent as seen by a Session.
type ToolEntry struct {
	Spec   gotato.ToolSpec `json:"spec"`
	Active bool            `json:"active"`
}

// Agent is a named, reusable definition: the model, instruction, tools and
// limits its Sessions run. The Runner builds a live Session from it for each
// Run. Sharing a Model client between Agents and Runs is expected; nothing
// here is mutable Run state.
type Agent struct {
	Name        string
	Model       gotato.Model
	ModelName   string
	Instruction string
	Tools       []gotato.Tool
	ToolSources []gotato.ToolSource
	// MaxEffect is the highest Effect a Tool of this Agent may have. A Tool
	// above it is hidden from the model and from Runner.Tools, and no Session
	// can switch it on. Empty means no cap (gotato.WithMaxEffect).
	MaxEffect gotato.ToolEffect
	// ContextBuilder defaults to modelctx.FullHistory().
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
	Agents    []Agent
	Admission Admission
	// Instance names this Runner in the Claims it writes to the store. Empty
	// generates a new name, so each start is a new Instance.
	Instance string
	// Liveness sets this Runner's Liveness row, and how long a Run waits
	// for a Session that runs on another Instance.
	Liveness Liveness
}

// ErrBusy is returned when a Session already has a Run in flight and the
// policy is RejectWhileBusy.
var ErrBusy = gotato.ErrorOf(gotato.ErrBusy, "service: session has a run in flight")

// ErrCapacity is returned when MaxActiveRuns is reached.
var ErrCapacity = gotato.ErrorOf(gotato.ErrLimitExceeded, "service: maximum active runs reached")

// ErrUnknownAgent is returned for an unregistered Agent name.
var ErrUnknownAgent = gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown agent")

// ErrSessionExists is returned when an explicit Session ID is already taken.
var ErrSessionExists = gotato.ErrorOf(gotato.ErrInvalidState, "service: session already exists")

// ErrDraining is returned once Drain has started.
var ErrDraining = gotato.ErrorOf(gotato.ErrInvalidState, "service: draining")

// ErrNotPersisted reports that a settled Run could not be saved. The Run
// outcome is still returned; callers must treat the Session state as lost.
var ErrNotPersisted = errors.New("service: session was not persisted")

// Runner serves Runs against Sessions.
type Runner struct {
	store     session.Store
	instance  string
	liveness  Liveness
	beat      heartbeat
	agents    map[string]Agent
	order     []string
	admission Admission

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

	mu      sync.Mutex
	runID   gotato.RunID
	abort   func()
	cancel  context.CancelFunc
	stopped bool
}

// attachAbort installs the started-Run cancellation path. Until it is
// installed, cancel stops the pre-start wait instead.
func (h *runHandle) attachAbort(abort func()) {
	h.mu.Lock()
	h.abort = abort
	h.mu.Unlock()
}

// abortIfStopped aborts a Run that was stopped before its live Session had a
// Run to abort: Session.Abort is a no-op until the Run starts. The runTracker
// calls it at run_start.
func (h *runHandle) abortIfStopped() {
	h.mu.Lock()
	stopped, abort := h.stopped, h.abort
	h.mu.Unlock()
	if stopped && abort != nil {
		abort()
	}
}

// attachRunID records the RunID the Session assigned. The first one wins.
func (h *runHandle) attachRunID(runID gotato.RunID) {
	h.mu.Lock()
	if h.runID == "" {
		h.runID = runID
	}
	h.mu.Unlock()
}

// stop aborts a started Run, or the pre-start wait when none started yet.
//
// Once the live Session is built, the Run is stopped only through Abort, never
// by cancelling the context Prompt waits on: Prompt then returns the settled
// cancelled result, with its RunID, usage and metrics, instead of giving up on
// it. A stop that lands before the Session has a Run to abort is kept, and
// abortIfStopped applies it at run_start. Before the live Session exists,
// cancelling the context is what reaches a Run that is waiting for the
// Session lock or loading the Session.
func (h *runHandle) stop() {
	h.mu.Lock()
	h.stopped = true
	abort := h.abort
	cancel := h.cancel
	h.mu.Unlock()
	if abort != nil {
		abort()
		return
	}
	if cancel != nil {
		cancel()
	}
}

// New validates the configuration and creates a Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Store == nil {
		return nil, errors.New("service: Store is required")
	}
	if len(cfg.Agents) == 0 {
		return nil, errors.New("service: at least one Agent is required")
	}
	r := &Runner{
		store:     cfg.Store,
		instance:  strings.TrimSpace(cfg.Instance),
		liveness:  cfg.Liveness.withDefaults(),
		agents:    map[string]Agent{},
		admission: cfg.Admission,
		locks:     newSessionLocks(),
		handles:   map[string][]*runHandle{},
		byRun:     map[gotato.RunID]*runHandle{},
	}
	if r.instance == "" {
		r.instance = session.NewID()
	}
	if r.admission.Queue == "" {
		r.admission.Queue = RejectWhileBusy
	}
	if r.admission.Queue != RejectWhileBusy && r.admission.Queue != WaitWhileBusy {
		return nil, fmt.Errorf("service: unknown queue policy %q", r.admission.Queue)
	}
	r.idle = sync.NewCond(&r.mu)
	for _, spec := range cfg.Agents {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return nil, errors.New("service: Agent.Name is empty")
		}
		if spec.Model == nil {
			return nil, fmt.Errorf("service: Agent %q has no Model", name)
		}
		if _, dup := r.agents[name]; dup {
			return nil, fmt.Errorf("service: duplicate Agent %q", name)
		}
		if !spec.MaxEffect.Valid() {
			return nil, fmt.Errorf("service: Agent %q has unknown MaxEffect %q", name, spec.MaxEffect)
		}
		spec.Name = name
		r.agents[name] = spec
		r.order = append(r.order, name)
	}
	return r, nil
}

// Store returns the Session store.
func (r *Runner) Store() session.Store { return r.store }

// Instance returns the name this Runner writes its Claims with.
func (r *Runner) Instance() string { return r.instance }

// Session reads a Session's whole Record from the store: its facts, Runs,
// usage and Events. It changes nothing.
func (r *Runner) Session(ctx context.Context, sessionID string) (*session.Record, error) {
	return session.View(ctx, r.store, sessionID)
}

// Sessions lists the stored Sessions, the most recently updated first.
func (r *Runner) Sessions(ctx context.Context) ([]session.Summary, error) {
	return r.store.List(ctx)
}

// Agents lists the registered Agent names; the first is the default.
func (r *Runner) Agents() []string { return append([]string(nil), r.order...) }

// Agent returns one registered Agent; an empty name means the default.
func (r *Runner) Agent(name string) (Agent, bool) {
	if name == "" {
		name = r.order[0]
	}
	spec, ok := r.agents[name]
	return spec, ok
}

// ActiveRuns reports Runs in flight.
func (r *Runner) ActiveRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflight
}

// Draining reports whether Drain has started. Readiness probes use it to stop
// receiving traffic before the process exits.
func (r *Runner) Draining() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.draining
}

// CreateSession creates and saves a Session bound to an Agent. It fails with
// ErrSessionExists when an ID given in options is taken.
func (r *Runner) CreateSession(ctx context.Context, agent string, metadata map[string]string, options ...session.Option) (*session.Record, error) {
	s, _, err := r.newSession(agent, metadata, options...)
	if err != nil {
		return nil, err
	}
	if _, err := r.create(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// newSession builds a Session bound to an Agent, without saving it.
func (r *Runner) newSession(agent string, metadata map[string]string, options ...session.Option) (*session.Record, Agent, error) {
	spec, ok := r.Agent(agent)
	if !ok {
		return nil, Agent{}, ErrUnknownAgent
	}
	s := session.New(options...)
	for key, value := range metadata {
		s.Set(key, value)
	}
	s.Set(MetaAgent, spec.Name)
	return s, spec, nil
}

// CreateSessionExclusive creates a Session with an explicit ID and fails with
// ErrSessionExists when the ID is taken. The store's Create is the check, so
// two concurrent creates cannot both succeed, also on two Instances.
func (r *Runner) CreateSessionExclusive(ctx context.Context, agent, id string, metadata map[string]string) (*session.Record, error) {
	if strings.TrimSpace(id) == "" {
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "service: session ID is required")
	}
	return r.CreateSession(ctx, agent, metadata, session.WithID(id))
}

// RunRequest is one unit of work.
type RunRequest struct {
	// SessionID names the Session. Empty creates a new Session.
	SessionID string
	// Agent selects the Agent for a new Session; an existing Session
	// keeps the Agent it was created with unless Agent is set.
	Agent string
	// Prompt is the user input. Empty with Continue=true resumes the loop.
	Prompt   string
	Continue bool
	// Metadata is applied to a new Session.
	Metadata map[string]string
	// Update sets metadata on the Session before the Run, under its Claim,
	// as the Run's first write. An empty value deletes a key.
	Update map[string]string
	// SkipSave runs without persisting the Session: a new Session lives only
	// for this Run, and a changed existing Session, Update included, is left
	// as it was.
	SkipSave bool
	// Timeout bounds this Run; the Run settles as deadline_exceeded. Zero
	// keeps the Agent's RunDeadline.
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

	// Admitted, then registered immediately: from here on Drain and
	// CancelSession reach the Run even while it is still loading its Session.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	handle := &runHandle{sessionID: request.SessionID, cancel: cancelRun}
	r.registerHandle(handle)
	defer r.unregisterHandle(handle)

	// Create the Session, or name the one to load. runCtx carries the
	// cancellation that Stop/Drain apply, so a stalled Store call does not
	// outlive them.
	var s *session.Record
	var writer *session.Writer
	var saved session.WriteStats
	sessionID := request.SessionID
	if sessionID == "" {
		if request.SkipSave {
			// No persistence: build an in-memory Session for this Run only.
			s = session.New()
			for key, value := range request.Metadata {
				s.Set(key, value)
			}
		} else {
			fresh, spec, err := r.newSession(request.Agent, request.Metadata)
			if err != nil {
				return RunResult{}, err
			}
			writer, err = r.create(runCtx, fresh)
			stats := session.WriteStats{}
			if writer != nil {
				stats = writer.Stats()
				saved = stats
			}
			r.emitRunnerEvent(context.WithoutCancel(runCtx), &spec, sink, storeEvent(gotato.EventSessionSaved, fresh.ID(), fresh.Len(), stats, err))
			if err != nil {
				return RunResult{}, err
			}
			s = fresh
		}
		// A newly created Session has a generated ID; retarget the handle so
		// CancelSession can find it for the rest of the Run.
		sessionID = s.ID()
		r.retargetHandle(handle, sessionID)
	}

	// One Run per Session at a time. Every Session mutation goes through
	// withSessionLock; the admission policy decides the busy behavior.
	var out RunResult
	var runErr error
	started := false
	created := request.SessionID == "" && !request.SkipSave
	lockErr := r.withSessionLock(runCtx, sessionID, func() error {
		// An existing Session is loaded once, under the lock: another Run
		// may have saved it meanwhile, and a Session deleted in between must
		// not be resurrected from a stale copy.
		var loadTime time.Duration
		if request.SessionID != "" {
			// A Run writes as this Instance: opening the Session claims it
			// when another Instance held it, and a stale writer is fenced.
			began := time.Now()
			var err error
			if request.SkipSave {
				s, err = session.Open(runCtx, r.store, sessionID)
				loadTime = time.Since(began)
			} else {
				writer, err = r.open(runCtx, sessionID)
				if writer != nil {
					loadTime = writer.Stats().Load
					s = writer.Record()
				} else {
					loadTime = time.Since(began)
				}
			}
			if err != nil {
				r.emitRunnerEvent(runCtx, nil, sink, storeEvent(gotato.EventSessionLoaded, sessionID, 0, session.WriteStats{Load: loadTime}, err))
				return err
			}
		}
		for key, value := range request.Update {
			s.Set(key, value)
		}

		agentName := request.Agent
		if agentName == "" {
			agentName, _ = s.Get(MetaAgent)
		}
		spec, ok := r.Agent(agentName)
		if request.SessionID != "" {
			observers := &spec
			if !ok {
				observers = nil
			}
			r.emitRunnerEvent(runCtx, observers, sink, storeEvent(gotato.EventSessionLoaded, sessionID, s.Len(), session.WriteStats{Load: loadTime}, nil))
		}
		// A Run that cannot start still releases the Session it opened.
		abandon := func(err error) error {
			if writer != nil {
				releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
				_ = writer.Release(releaseCtx)
				cancelRelease()
			}
			return err
		}
		if !ok {
			return abandon(ErrUnknownAgent)
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
		live, auto, err := r.buildSession(spec, s, writer, sink, tracker, InspectOptions{})
		if err != nil {
			return abandon(err)
		}
		started = true
		// From here a stop aborts the Run inside the live Session, so Prompt
		// still returns the settled (cancelled) result. Prompt waits on the
		// caller's context, not runCtx: a stop that cancelled runCtx while the
		// Session was loading reaches the Run through abortIfStopped instead.
		handle.attachAbort(live.Abort)

		var result gotato.RunResult
		if request.Continue {
			result, runErr = live.Continue(ctx)
		} else {
			result, runErr = live.Prompt(ctx, gotato.UserMessage(request.Prompt))
		}
		_ = live.Close(context.Background())

		if writer != nil {
			// Persist the settled Session even when the Run was cancelled, but
			// bound the write so a stalled Store cannot pin the Run (and Drain)
			// forever.
			saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
			saveErr := writer.Release(saveCtx)
			cancelSave()
			event := storeEvent(gotato.EventSessionSaved, s.ID(), s.Len(), writer.Stats().Since(saved), saveErr)
			tracker.stamp(&event)
			r.emitRunnerEvent(context.WithoutCancel(runCtx), &spec, sink, event)
			if saveErr != nil {
				// The Session state is lost whether or not the Run failed: keep
				// the Run error and add the persistence failure so callers can
				// observe both (errors.Is still matches each).
				persistErr := fmt.Errorf("%w: %w", ErrNotPersisted, saveErr)
				if runErr == nil {
					runErr = persistErr
				} else {
					runErr = errors.Join(runErr, persistErr)
				}
			}
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
			// Session behind. A failed cleanup is reported, not discarded.
			deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
			deleteErr := r.store.Delete(deleteCtx, sessionID)
			cancelDelete()
			if deleteErr != nil {
				lockErr = errors.Join(lockErr, fmt.Errorf("service: cleanup of session %s failed: %w", sessionID, deleteErr))
			}
		}
		if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
			// Cancelled while waiting for the Session lock: report a settled
			// cancelled Run rather than a bare error.
			return cancelledRunResult(sessionID, request.Agent, lockErr), lockErr
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

// runStatusForError classifies a terminal error when the Session itself did not
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

// buildSession builds the live Session for one Run from its Agent and its
// Record's own settings. override replaces the Session's instruction for an
// inspection; a Run passes none.
// buildSession builds the live Session for s. With a writer, the writer is its
// Transcript, so every step of a Run is written as it happens.
func (r *Runner) buildSession(spec Agent, s *session.Record, writer *session.Writer, sink func(gotato.Event) error, tracker *runTracker, override InspectOptions) (*gotato.Session, *modelctx.AutoCompactor, error) {
	var transcript gotato.Transcript = s
	if writer != nil {
		transcript = writer
	}
	instruction := spec.Instruction
	if stored, ok := s.Get(MetaInstruction); ok && stored != "" {
		instruction = stored
	}
	if override.Instruction != "" {
		instruction = override.Instruction
	}
	builder := spec.ContextBuilder
	if builder == nil {
		builder = modelctx.FullHistory()
	}
	policy := spec.Compact
	policy.DropReasoning = policy.DropReasoning || modelctx.DropsReasoning(spec.Model)
	if raw, ok := s.Get(MetaCompactCeiling); ok {
		if ceiling, err := strconv.Atoi(raw); err == nil && ceiling > 0 {
			policy.Ceiling = ceiling
			policy.Floor = 0
		}
	}
	var auto *modelctx.AutoCompactor
	extensions := []any{session.NewRecorder(s)}
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
		gotato.WithTranscript(transcript),
		gotato.WithContextBuilder(builder),
		gotato.WithExtensions(extensions...),
		gotato.WithCacheKey(sessionCacheKey(s)),
	}
	options = append(options, gotato.WithToolSource(&sessionTools{spec: spec, session: s}))
	if spec.MaxEffect != gotato.EffectUnspecified {
		options = append(options, gotato.WithMaxEffect(spec.MaxEffect))
	}
	if spec.Limits != nil {
		options = append(options, gotato.WithLimits(*spec.Limits))
	}
	options = append(options, spec.Options...)
	agent, err := gotato.NewSession(options...)
	if err != nil {
		return nil, nil, err
	}
	return agent, auto, nil
}

// CacheKey is the ModelRequest.CacheKey the Runner gives every Turn of a
// Session, so the provider serves a conversation from one prompt cache. It is
// a hash of the Session ID, because an ID the caller chose may name a user or
// a tenant, and the key goes to the provider. A Session forked by the Runner
// keeps its parent's key instead (MetaCacheKey), and an Agent may set another
// with gotato.WithCacheKey in its Options.
func CacheKey(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return "gotato-" + hex.EncodeToString(sum[:16])
}

// sessionCacheKey is the cache key a Session's Turns carry: the one in its
// metadata (MetaCacheKey), which a fork inherits from its parent, or else
// CacheKey of its ID.
func sessionCacheKey(s *session.Record) string {
	if key, ok := s.Get(MetaCacheKey); ok && key != "" {
		return key
	}
	return CacheKey(s.ID())
}

type sinkObserver struct{ fn func(gotato.Event) error }

func (o sinkObserver) Observe(_ context.Context, event gotato.Event) error { return o.fn(event) }
func (o sinkObserver) Advisory() bool                                      { return true }

// InspectOptions override Session settings for one inspection without
// touching the Session.
type InspectOptions struct {
	Instruction string
}

// Inspect reports the Context a Run against the Session would send now. It
// builds the live Session a Run would build and asks it, so the report includes the
// Agent's Extensions, Tools and model options, and says whether the next Run
// compacts the Session first. Optional InspectOptions override the Session's
// stored settings. The Session is not changed.
func (r *Runner) Inspect(ctx context.Context, sessionID string, options ...InspectOptions) (modelctx.Report, error) {
	s, err := session.Open(ctx, r.store, sessionID)
	if err != nil {
		return modelctx.Report{}, err
	}
	agentName, _ := s.Get(MetaAgent)
	spec, ok := r.Agent(agentName)
	if !ok {
		return modelctx.Report{}, ErrUnknownAgent
	}
	var override InspectOptions
	if len(options) > 0 {
		override = options[0]
	}
	live, auto, err := r.buildSession(spec, s, nil, nil, nil, override)
	if err != nil {
		return modelctx.Report{}, err
	}
	defer func() { _ = live.Close(context.Background()) }()
	inspection, err := live.InspectContext(ctx)
	if err != nil {
		return modelctx.Report{}, err
	}
	report := modelctx.ReportOf(inspection)
	report.SessionID = s.ID()
	report.Compactions = s.Compactions()
	if auto != nil {
		report.CompactionPending = auto.Pending()
		report.CompactCeiling = auto.Ceiling()
	}
	return report, nil
}

// sessionTools is the ToolSource a live Session sees: its Agent's
// static Tools and ToolSources within its MaxEffect, minus the ones the
// Session deactivated. It is re-read at every Turn boundary like any
// ToolSource.
type sessionTools struct {
	spec    Agent
	session *session.Record
}

func (t *sessionTools) Tools() []gotato.Tool {
	var tools []gotato.Tool
	for _, tool := range t.spec.Tools {
		if t.allowed(tool.Spec()) && t.active(tool.Spec().ID) {
			tools = append(tools, tool)
		}
	}
	for _, source := range t.spec.ToolSources {
		for _, tool := range source.Tools() {
			if t.allowed(tool.Spec()) && t.active(tool.Spec().ID) {
				tools = append(tools, tool)
			}
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Spec().ID < tools[j].Spec().ID })
	return tools
}

// allowed reports whether a Tool is within the Agent's MaxEffect.
func (t *sessionTools) allowed(spec gotato.ToolSpec) bool {
	return t.spec.MaxEffect == gotato.EffectUnspecified || spec.Effect.Within(t.spec.MaxEffect)
}

func (t *sessionTools) active(id string) bool {
	if t.session == nil {
		return true
	}
	value, _ := t.session.Get(MetaToolPrefix + id)
	return value != "inactive"
}

// Tools describes the Tool surface of an Agent as a Session sees it.
// sessionID may be empty for the Agent's default surface. A Tool above the
// Agent's MaxEffect is not listed.
func (r *Runner) Tools(ctx context.Context, agent, sessionID string) ([]ToolEntry, error) {
	var s *session.Record
	if sessionID != "" {
		var err error
		s, err = session.Open(ctx, r.store, sessionID)
		if err != nil {
			return nil, err
		}
		if agent == "" {
			agent, _ = s.Get(MetaAgent)
		}
	}
	spec, ok := r.Agent(agent)
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

// SetToolActive records a Session-level activation change and writes it as
// the Session's writer, under the Session lock.
func (r *Runner) SetToolActive(ctx context.Context, sessionID, toolID string, active bool) (ToolEntry, error) {
	var found *gotato.ToolSpec
	err := r.withSessionLock(ctx, sessionID, func() error {
		writer, err := r.open(ctx, sessionID)
		if err != nil {
			return err
		}
		s := writer.Record()
		agent, _ := s.Get(MetaAgent)
		spec, ok := r.Agent(agent)
		if !ok {
			_ = writer.Release(ctx)
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
			_ = writer.Release(ctx)
			return gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown tool "+toolID)
		}
		if active {
			s.Set(MetaToolPrefix+toolID, "")
		} else {
			s.Set(MetaToolPrefix+toolID, "inactive")
		}
		return writer.Release(ctx)
	})
	if err != nil {
		return ToolEntry{}, err
	}
	return ToolEntry{Spec: *found, Active: active}, nil
}

// Compact compacts a Session explicitly, under the Session lock. When the
// Session's Agent has a Model whose provider is sent only the current Round's
// reasoning (modelctx.DropsReasoning), the kept Messages lose their reasoning.
func (r *Runner) Compact(ctx context.Context, sessionID string, opts modelctx.CompactOptions) (modelctx.Result, error) {
	var result modelctx.Result
	err := r.withSessionLock(ctx, sessionID, func() error {
		writer, err := r.open(ctx, sessionID)
		if err != nil {
			return err
		}
		agentName, _ := writer.Record().Get(MetaAgent)
		if spec, ok := r.Agent(agentName); ok && modelctx.DropsReasoning(spec.Model) {
			opts.DropReasoning = true
		}
		result, err = modelctx.Compact(ctx, writer.Record(), opts)
		if err != nil {
			_ = writer.Release(ctx)
			return err
		}
		return writer.Release(ctx)
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

// Fork creates a new Session from an existing one. An explicit newID must be
// free: a fork never overwrites an existing Session.
func (r *Runner) Fork(ctx context.Context, sessionID, newID string) (*session.Record, error) {
	parent, err := session.View(ctx, r.store, sessionID)
	if err != nil {
		return nil, err
	}
	var options []session.Option
	if newID != "" {
		options = append(options, session.WithID(newID))
	}
	child := session.Fork(parent, options...)
	// The child starts with its parent's history, so it keeps its parent's
	// cache key and its first Turn reads the parent's cached prefix.
	child.Set(MetaCacheKey, sessionCacheKey(parent))
	// The store's Create is the existence check, so two concurrent forks with
	// the same explicit newID cannot both succeed.
	if _, err := r.create(ctx, child); err != nil {
		if errors.Is(err, ErrSessionExists) {
			return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "service: session "+child.ID()+" already exists")
		}
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

// cancelAll cancels every admitted Run: started Runs through their live Session, Runs
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

// retargetHandle moves an admitted Run to the Session ID it turned out to own.
// A Run registers before its Session is loaded, so a newly created Session's
// generated ID has to be attached afterwards.
func (r *Runner) retargetHandle(handle *runHandle, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if handle.sessionID == sessionID {
		return
	}
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
	handle.sessionID = sessionID
	r.handles[sessionID] = append(r.handles[sessionID], handle)
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

// attachRunID maps the Session-assigned RunID to the admitted Run so CancelRun
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

// runTracker is an advisory EventObserver that attaches the RunID the Session
// assigned to the admitted Run handle as soon as run_start fires.
type runTracker struct {
	runner *Runner
	handle *runHandle

	mu       sync.Mutex
	runID    gotato.RunID
	sequence uint64
}

func (t *runTracker) Advisory() bool { return true }

func (t *runTracker) Observe(_ context.Context, event gotato.Event) error {
	t.mu.Lock()
	t.runID, t.sequence = event.RunID, max(t.sequence, event.Sequence)
	t.mu.Unlock()
	if event.Kind != gotato.EventRunStart {
		return nil
	}
	t.runner.attachRunID(t.handle, event.RunID)
	t.handle.abortIfStopped()
	return nil
}

// stamp places an Event the Runner emits after the Run in that Run, right
// after its last Event.
func (t *runTracker) stamp(event *gotato.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	event.RunID, event.Sequence = t.runID, t.sequence+1
}

// storeEvent builds a session_loaded or session_saved Event: a load times the
// store's Load, and a save sums every write since the previous save Event,
// and counts them. The elapsed time keeps microseconds, because a load from
// memory takes far less than a millisecond. A failure is only flagged: the
// store's error text can name paths and hosts, and Events reach HTTP and gRPC
// clients.
func storeEvent(kind gotato.EventKind, sessionID string, messages int, stats session.WriteStats, err error) gotato.Event {
	elapsed := stats.Elapsed
	if kind == gotato.EventSessionLoaded {
		elapsed = stats.Load
	}
	payload := map[string]any{
		"messages":   messages,
		"elapsed_ms": float64(elapsed.Microseconds()) / 1000,
	}
	if kind == gotato.EventSessionSaved {
		payload["writes"] = stats.Writes
	}
	if err != nil {
		payload["failed"] = true
	}
	return gotato.Event{SessionID: gotato.SessionID(sessionID), Kind: kind, Class: gotato.EventProtected, Payload: payload, Timestamp: time.Now()}
}

// emitRunnerEvent delivers an Event the Runner produces itself to the Run's
// sink and to the EventObservers among the Agent's Extensions, the same places
// the live Session's Events reach. A nil spec reaches the sink only. Every
// delivery is advisory: a failing or panicking observer never fails the Run.
func (r *Runner) emitRunnerEvent(ctx context.Context, spec *Agent, sink func(gotato.Event) error, event gotato.Event) {
	if sink != nil {
		deliver(func() error { return sink(event) })
	}
	if spec == nil {
		return
	}
	for _, extension := range spec.Extensions {
		if observer, ok := extension.(gotato.EventObserver); ok {
			view := event
			view.Payload = maps.Clone(event.Payload)
			deliver(func() error { return observer.Observe(ctx, view) })
		}
	}
}

// deliver calls fn and drops its error or panic.
func deliver(fn func() error) {
	defer func() { _ = recover() }()
	_ = fn()
}
