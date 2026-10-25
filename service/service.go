// Package service turns the runtime into a service: named Agents, a Store of
// Session Records, and a live Session built for each Run.
//
// Session semantics stay in the runtime. For every Run the Runner loads the
// Session's Record, takes the Session's lock, builds a live gotato.Session
// from the Agent exactly as an application would (WithTranscript,
// WithContextBuilder, WithToolSource, WithFeed,
// modelctx.AutoCompact), runs it, closes it, and saves the Record.
// Continuity lives in the Store, so any process holding the Store can serve
// any Session.
//
// After a Run the Session stays in memory as a warm copy (Agent.Warm,
// WarmPolicy): a cache, never a lock. It holds no Claim while it idles, the
// next Run uses it only while the Store's head is still the copy's, and it
// is compacted by its own timer before its prompt cache expires, then leaves
// at its TTL. Warm Sessions count toward Admission.MaxActiveSessions: at
// the limit a new Run evicts the longest-idle one first.
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
	"slices"
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

// DefaultRunTimeout is how long a Run of an Agent may last when neither the
// Agent's RunTimeout nor the request's Timeout says otherwise.
const DefaultRunTimeout = 30 * time.Minute

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
	// RunTimeout bounds one Run of this Agent; the Run settles as
	// deadline_exceeded. Zero means DefaultRunTimeout, negative means no
	// deadline. A RunRequest's Timeout replaces it, and a deadline on the
	// caller's context that comes sooner ends the Run sooner. The core sets
	// no Run deadline of its own: there a Run's deadline is its ctx.
	RunTimeout time.Duration
	// Compact says when the Agent's Sessions compact by themselves: past the
	// ceiling, which is the Model's window less a reserve unless Ceiling sets
	// one (a negative Ceiling turns it off), and once when the provider refuses
	// a request as too long. A Session may override the ceiling through
	// MetaCompactCeiling.
	Compact modelctx.CompactPolicy
	// Warm says how long the Agent's idle Sessions stay in memory after a
	// Run, and whether they are compacted while they idle. The zero value is
	// the default: 60 minutes, and an idle compaction.
	Warm WarmPolicy
	// TimeAwayAfter is how long a Session must have been idle before a
	// message that starts a Run is followed by a time_away notice with how
	// long it has been and the time now (G-F29). Zero means DefaultTimeAway
	// (30 minutes); a negative value turns the notice off. The time is shown
	// in the zone of Compact.Location, UTC when it is nil.
	TimeAwayAfter time.Duration
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
	// MaxActiveSessions is the hard limit on the Sessions this Instance
	// keeps: those a Run holds, counted once however many Runs wait on them,
	// and the warm ones idling in memory. At the limit, a Run that would
	// activate another Session first evicts the longest-idle warm Session
	// (session_unloaded, why evicted) and runs. Only when every Session is
	// running a Run is it refused with ErrOverloaded, before anything is
	// loaded, claimed or created, so the load balancer can send it to another
	// Instance. A Run on a Session that is active or warm already is not
	// refused. Zero means no limit.
	MaxActiveSessions int
	Queue             QueuePolicy
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
	// Address is where a caller can reach this Instance's own endpoint, such
	// as "http://10.0.0.7:8080". It is optional. The Instance advertises it in
	// its Liveness row, so a message for a Run that this Instance writes, sent
	// to another one, is answered not_writer with this address (see Send). The
	// Runner only carries it; it never connects to one.
	Address string
	// StreamPoll is how often an idle Session stream looks in the store for
	// Events that another Instance wrote. Zero means one second; a negative
	// value turns the look off, so a stream carries only what this Instance
	// writes.
	StreamPoll time.Duration
	// Clock times the warm Sessions: their TTL and their idle compaction.
	// Nil means the system clock; a test supplies one it advances itself.
	Clock Clock
}

// ErrBusy is returned when a Session already has a Run in flight and the
// policy is RejectWhileBusy.
var ErrBusy = gotato.ErrorOf(gotato.ErrBusy, "service: session has a run in flight")

// ErrCapacity is returned when MaxActiveRuns is reached.
// It is refused at admission, so the Run is not run, and another Instance, or
// this one later, may take it.
var ErrCapacity = refusedAtAdmission(gotato.ErrorOf(gotato.ErrLimitExceeded, "service: maximum active runs reached"))

// refusedAtAdmission marks a refusal made before a Run started: nothing ran,
// and the same request may succeed elsewhere or later.
func refusedAtAdmission(e *gotato.RuntimeError) *gotato.RuntimeError {
	e.NotRun, e.Retryable = true, true
	return e
}

// ErrOverloaded is returned when the Instance is at its MaxActiveSessions and
// the Run would activate another Session. The Run did not start and nothing
// was written, so it is safe to send again to another Instance.
var ErrOverloaded = gotato.ErrorOf(gotato.ErrOverloaded, "service: this instance is at its limit of active sessions")

// ErrUnknownAgent is returned for an unregistered Agent name.
// The Run is refused before it starts, so it is not run.
var ErrUnknownAgent = notRunError(gotato.ErrorOf(gotato.ErrInvalidArgument, "service: unknown agent"))

// notRunError marks a refusal made before a Run started.
func notRunError(e *gotato.RuntimeError) *gotato.RuntimeError {
	e.NotRun = true
	return e
}

// ErrSessionExists is returned when an explicit Session ID is already taken.
var ErrSessionExists = gotato.ErrorOf(gotato.ErrInvalidState, "service: session already exists")

// ErrDraining refuses a new Run, and taking a Session over from another
// Instance, once the Runner drains.
var ErrDraining = gotato.ErrorOf(gotato.ErrDraining, "service: draining")

// ErrNotPersisted reports that a write of a Run failed, so the Record does not
// hold the Run. The Run outcome is still returned, settled as gotato.RunFailed
// with store_failure; callers must treat the Session state as lost. A Run whose
// only failed write is the release of the Session keeps its status and
// returns no error: its Events are all stored.
var ErrNotPersisted = errors.New("service: session was not persisted")

// Runner serves Runs against Sessions.
type Runner struct {
	store     session.Store
	instance  string
	liveness  Liveness
	address   string
	beat      heartbeat
	agents    map[string]Agent
	order     []string
	admission Admission

	locks *sessionLocks

	// clock times the warm Sessions. warm holds the idle ones, each a cache
	// of a Record and never a lock; warmClosed says the Runner has let them
	// all go and keeps none (see warm.go).
	clock Clock
	// beforeDetach is a test seam: it runs when a Run has settled and its live
	// Session still takes messages (see export_test.go).
	beforeDetach func()
	warmMu       sync.Mutex
	warm         map[string]*warmEntry
	warmClosed   bool
	warmSeq      uint64

	// instanceFeed is the Instance's stream; feeds are the Feeds of the
	// Records in use, one per Session ID and epoch, each mirrored to it (see
	// events.go).
	instanceFeed *gotato.Feed
	feedsMu      sync.Mutex
	feeds        map[feedKey]*sessionFeed

	mu       sync.Mutex
	handles  map[string][]*runHandle
	byRun    map[gotato.RunID]*runHandle
	inflight int
	draining bool
	// streams are the Session streams open for clients, and streamsOver says
	// the drain is over, so a new one ends at once.
	streamsMu   sync.Mutex
	streams     map[*SessionStream]struct{}
	streamsOver bool
	// streamStarts counts the starts of Session streams (SessionStream.startedAt).
	streamStarts uint64
	streamPoll   time.Duration
	// drained is made by the first Drain and closed once every Run admitted
	// before it has settled and released its Session.
	drained chan struct{}
	idle    *sync.Cond
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
	// drained says the drain stopped this Run at its deadline.
	drained bool
	// liveMu guards live, the Run's live Session while it runs, where a
	// message for the Run goes (see messages.go). ready is closed once there
	// is one, or the Run is over.
	liveMu      sync.Mutex
	live        *gotato.Session
	ready       chan struct{}
	readyClosed bool
	// next holds the messages a settled Run left in its queues, for the Run that
	// follows it (guarded by liveMu).
	next []gotato.Queued
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

// stopForDrain stops the Run because the drain reached its deadline.
func (h *runHandle) stopForDrain() {
	h.mu.Lock()
	h.drained = true
	h.mu.Unlock()
	h.stop()
}

// stoppedByDrain reports whether the drain stopped this Run.
func (h *runHandle) stoppedByDrain() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.drained
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
		store:      cfg.Store,
		instance:   strings.TrimSpace(cfg.Instance),
		liveness:   cfg.Liveness.withDefaults(),
		address:    strings.TrimSpace(cfg.Address),
		streamPoll: cfg.StreamPoll,
		agents:     map[string]Agent{},
		admission:  cfg.Admission,
		locks:      newSessionLocks(),
		clock:      cfg.Clock,
		warm:       map[string]*warmEntry{},
		feeds:      map[feedKey]*sessionFeed{},
		handles:    map[string][]*runHandle{},
		byRun:      map[gotato.RunID]*runHandle{},
	}
	if r.instance == "" {
		r.instance = session.NewID()
	}
	if r.clock == nil {
		r.clock = systemClock{}
	}
	if r.admission.MaxActiveSessions < 0 {
		return nil, errors.New("service: MaxActiveSessions cannot be negative")
	}
	if r.admission.Queue == "" {
		r.admission.Queue = RejectWhileBusy
	}
	if r.admission.Queue != RejectWhileBusy && r.admission.Queue != WaitWhileBusy {
		return nil, fmt.Errorf("service: unknown queue policy %q", r.admission.Queue)
	}
	if r.streamPoll == 0 {
		r.streamPoll = defaultStreamPoll
	}
	r.instanceFeed = gotato.NewInstanceFeed()
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

// Events reads a Session's stored Events from the store: every Entry of its
// Record that is an Event, in the order of their numbers, as typed Events. A
// Record written before the Event design shows gaps in the numbers where an
// Entry has no Event.
func (r *Runner) Events(ctx context.Context, sessionID string) ([]gotato.Event, error) {
	return session.StoredEvents(ctx, r.store, sessionID, 0)
}

// Sessions lists the stored Sessions, the most recently updated first.
func (r *Runner) Sessions(ctx context.Context) ([]session.Summary, error) {
	list, err := r.store.List(ctx)
	for i := range list {
		// A Record written before epochs names one, as every surface does.
		list[i].RecordEpoch = list[i].Epoch()
	}
	return list, err
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

// ActiveSessions reports the Sessions this Instance has activated: those that
// a Run holds or waits for.
func (r *Runner) ActiveSessions() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeSessionsLocked()
}

// activeSessionsLocked counts the Sessions with an admitted Run. Runs that
// create a Session have no ID yet, and each counts as one Session of its own.
func (r *Runner) activeSessionsLocked() int {
	n := len(r.handles)
	if fresh := len(r.handles[""]); fresh > 1 {
		n += fresh - 1
	}
	return n
}

// Draining reports whether the Runner drains: from the moment Drain has marked
// its Liveness row. Readiness probes use it to stop receiving traffic before
// the process exits.
func (r *Runner) Draining() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.draining
}

// CreateSession creates and saves a Session bound to an Agent. It fails with
// ErrSessionExists when an ID given in options is taken.
func (r *Runner) CreateSession(ctx context.Context, agent string, metadata map[string]string, options ...session.Option) (*session.Record, error) {
	s, spec, err := r.newSession(agent, metadata, options...)
	if err != nil {
		return nil, err
	}
	// Nothing runs against a Session that is only created: it is released in
	// the write that creates it.
	if _, err := r.create(ctx, s, spec.Name, session.WithIdle()); err != nil {
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
	// keeps the Agent's RunTimeout.
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
	// RecordEpoch is the epoch of the Session's Record, for a client that
	// loses the Run's stream to resume the Session's stream with. Empty for a
	// Run that is not saved.
	RecordEpoch string `json:"record_epoch,omitempty"`
	// Reconnect is set when this instance's drain stopped the Run before it
	// completed. By the time the Run returns, its Session is saved and
	// released, and so is every other Session of the drain.
	Reconnect *Reconnect `json:"reconnect,omitempty"`
}

// Reconnect tells a caller to send its next request for the Session through
// the load balancer, to any instance, after waiting RetryAfterMS. When the
// Run has a RunID, its prompt is saved, and the caller continues the Session;
// otherwise nothing ran, and the caller sends the request again.
type Reconnect struct {
	RetryAfterMS int64 `json:"retry_after_ms"`
	// Why names the reason, such as "draining".
	Why string `json:"why,omitempty"`
}

// Run executes one Run synchronously.
func (r *Runner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	return r.run(ctx, request, nil)
}

// StreamRun executes one Run and delivers every Event of its stream to sink as
// it is produced, on the caller's goroutine: the Session's stored Events and
// its deltas, from the first Event of the Run to its RunEnded. The Run itself
// runs on another goroutine, and sink reads a Subscription, so a sink that is
// slow slows only itself: the Session never waits for it, and a stored Event
// the sink falls behind on is read again from the Record. A sink error is
// advisory: it stops delivery, not the Run. StreamRun returns after the Run
// settles and its last Event has been delivered.
func (r *Runner) StreamRun(ctx context.Context, request RunRequest, sink func(gotato.Event) error) (RunResult, error) {
	return r.StreamRunAdmitted(ctx, request, nil, sink)
}

// StreamRunAdmitted is StreamRun with a call, on the caller's goroutine, once
// the Run is admitted and before its first Event is delivered. A Run that is
// refused at admission (draining, at capacity, past the activation limit)
// returns its error without the call, so an endpoint can still answer the
// request with a status of its own before it starts to stream.
func (r *Runner) StreamRunAdmitted(ctx context.Context, request RunRequest, admitted func(), sink func(gotato.Event) error) (RunResult, error) {
	if sink == nil {
		return r.run(ctx, request, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	type outcome struct {
		result RunResult
		err    error
	}
	subs := make(chan gotato.Subscription, 1)
	admit := make(chan struct{}, 1)
	done := make(chan outcome, 1)
	go func() {
		result, err := r.run(ctx, request, &runStream{ready: func(sub gotato.Subscription) { subs <- sub }, admitted: admit})
		done <- outcome{result, err}
	}()
	var finished *outcome
	call := func() {
		if admitted != nil {
			admitted()
		}
	}
	select {
	case <-admit:
		call()
	case o := <-done:
		finished = &o
		select {
		case <-admit:
			call()
		default:
		}
	}
	if finished != nil {
		// The Run ended: it may have handed over its stream first.
		select {
		case sub := <-subs:
			deliver(ctx, sub, sink)
		default:
		}
		return finished.result, finished.err
	}
	select {
	case sub := <-subs:
		deliver(ctx, sub, sink)
	case o := <-done:
		finished = &o
		// A Run that ended may have handed over its stream first.
		select {
		case sub := <-subs:
			deliver(ctx, sub, sink)
		default:
		}
	}
	if finished == nil {
		o := <-done
		finished = &o
	}
	return finished.result, finished.err
}

func (r *Runner) run(ctx context.Context, request RunRequest, stream *runStream) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !request.Continue && strings.TrimSpace(request.Prompt) == "" {
		return RunResult{}, notRunError(gotato.ErrorOf(gotato.ErrInvalidArgument, "service: prompt is required"))
	}
	// The Run registers as it is admitted, in one step, so the limits count it
	// at once and Drain and CancelSession reach it even while it is still
	// loading its Session.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	handle := &runHandle{sessionID: request.SessionID, cancel: cancelRun, ready: make(chan struct{})}
	if err := r.admit(handle); err != nil {
		why := "at_capacity"
		switch {
		case errors.Is(err, ErrDraining):
			why = "draining"
		case errors.Is(err, ErrOverloaded):
			why = "activation_limit"
		}
		r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Why: why}, request.Agent)
		return RunResult{}, err
	}
	if stream != nil && stream.admitted != nil {
		stream.admitted <- struct{}{}
	}
	r.notice(gotato.RunRequested{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Agent: request.Agent}, request.Agent)
	release := sync.OnceFunc(r.release)
	followed := false
	defer func() {
		if !followed {
			r.unregisterHandle(handle)
			release()
		}
	}()
	out, err := r.serve(ctx, runCtx, request, stream, handle, nil)
	if next := handle.takeNext(); len(next) > 0 {
		// Messages the Run accepted after its last point of taking them run in
		// a Run that follows it, on this instance and under the same
		// admission. This call returns with its own Run's result.
		followed = true
		r.followOn(ctx, handle, next, release)
		return out, err
	}
	if handle.stoppedByDrain() && out.Result.Status != gotato.RunCompleted {
		// The drain stopped this Run, and it is saved and released. It
		// returns once every Run of the drain is, so the caller reconnects to
		// a Session that no instance is still using.
		out.Reconnect = NewReconnect("draining")
		release()
		r.mu.Lock()
		drained := r.drained
		r.mu.Unlock()
		select {
		case <-drained:
		case <-ctx.Done():
		}
	}
	return out, err
}

// serve runs one admitted Run.
func (r *Runner) serve(ctx, runCtx context.Context, request RunRequest, stream *runStream, handle *runHandle, follow *followOn) (RunResult, error) {
	// Create the Session, or name the one to load. runCtx carries the
	// cancellation that Stop/Drain apply, so a stalled Store call does not
	// outlive them.
	var s *session.Record
	var writer *session.Writer
	var saved session.WriteStats
	var feed *gotato.Feed
	track := &tracker{}
	sessionID := request.SessionID
	// The Run's stream ends with the Run: what is queued is still delivered.
	defer func() {
		if stream != nil && stream.sub != nil {
			stream.sub.End()
		}
	}()
	// A Record's Feed is shared and counted; a Run that is not saved has a
	// Feed of its own, since its Events are in no Record.
	var acquired feedKey
	defer func() {
		if acquired != (feedKey{}) {
			r.releaseFeed(acquired)
		}
	}()
	if sessionID == "" {
		if request.SkipSave {
			// No persistence: build an in-memory Session for this Run only.
			s = session.New()
			for key, value := range request.Metadata {
				s.Set(key, value)
			}
			feed = gotato.NewFeed(nil)
			defer feed.Close()
			if err := r.streamTo(ctx, feed, stream, track); err != nil {
				return RunResult{}, err
			}
		} else {
			fresh, spec, err := r.newSession(request.Agent, request.Metadata)
			if err != nil {
				return RunResult{}, err
			}
			// The Run's stream starts before the Session is created, so its
			// first Event is the session_created that the Run caused.
			acquired = feedKey{fresh.ID(), fresh.RecordEpoch()}
			feed = r.acquireFeed(acquired, spec.Name)
			if err := r.streamTo(ctx, feed, stream, track); err != nil {
				return RunResult{}, err
			}
			writer, err = r.create(runCtx, fresh, spec.Name)
			stats := session.WriteStats{}
			if writer != nil {
				stats = writer.Stats()
				saved = stats
			}
			r.notice(savedEvent(fresh.ID(), "", fresh.Len(), stats, err), spec.Name)
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
	// A Run that follows another waits for the Session whatever the busy policy.
	lockErr := r.sessionLock(runCtx, sessionID, follow != nil, func() error {
		// An existing Session is loaded once, under the lock: another Run
		// may have saved it meanwhile, and a Session deleted in between must
		// not be resurrected from a stale copy.
		var loadTime time.Duration
		// The Events the open writes, a claim among them, are published once
		// the Run knows its Record and its Agent, so they go to that Record's
		// Feed and carry the Agent's name.
		var early []gotato.Event
		// reused says a warm copy served the Run: the Session was not loaded.
		reused := false
		if request.SessionID != "" {
			// A Run writes as this Instance: opening the Session claims it
			// when another Instance held it, and a stale writer is fenced.
			began := time.Now()
			var err error
			if request.SkipSave {
				feed = gotato.NewFeed(nil)
				defer feed.Close()
				if err := r.streamTo(ctx, feed, stream, track); err != nil {
					return err
				}
				s, err = session.Open(runCtx, r.store, sessionID)
				loadTime = time.Since(began)
			} else {
				var before *session.WriteStats
				writer, early, before, err = r.acquire(runCtx, sessionID)
				if before != nil {
					// A warm copy has written for earlier Runs: this Run's
					// writes count from here, its claim included.
					reused, saved = true, *before
				}
				if writer != nil {
					loadTime = writer.Stats().Load
					s = writer.Record()
				} else {
					loadTime = time.Since(began)
				}
			}
			if err != nil {
				r.notice(loadedEvent(sessionID, 0, loadTime, err), request.Agent)
				r.noteFailure(err, sessionID, request.Agent)
				return err
			}
			if writer != nil {
				// The Record's epoch is known once it is loaded: the Run uses
				// that Record's Feed, and its stream starts there, with the
				// claim the open wrote.
				acquired = feedKey{sessionID, s.RecordEpoch()}
				feed = r.acquireFeed(acquired, request.Agent)
				if err := r.streamTo(ctx, feed, stream, track); err != nil {
					releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
					_ = writer.Release(releaseCtx)
					cancelRelease()
					return err
				}
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
			label := spec.Name
			if !ok {
				label = ""
			}
			feed.SetLabel(label)
			if writer != nil {
				writer.SetPublisher(r.publisher(acquired, label))
			}
			for _, event := range early {
				feed.Publish(event)
			}
			if !reused {
				r.notice(loadedEvent(sessionID, s.Len(), loadTime, nil), label)
			}
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

		onRunStart := gotato.WithRunStart(func(runID gotato.RunID) {
			track.set(runID)
			r.attachRunID(handle, runID)
			handle.abortIfStopped()
		})
		live, auto, err := r.buildSession(ctx, spec, s, writer, InspectOptions{}, onRunStart, gotato.WithFeed(feed), gotato.WithQueueHandoff())
		if err != nil {
			return abandon(err)
		}
		// The notices that wait in the Record are read before this Run queues
		// any of its own.
		kept := s.PendingNotices()
		if follow == nil && !request.Continue {
			// A message that starts a Run after a long absence is followed by
			// a notice with the time.
			r.queueTimeAway(ctx, spec, s, live)
		}
		if len(kept) > 0 {
			// Notices that were queued and not committed, by an earlier Run or
			// by a call between Runs, on this instance or another, go in after
			// this Run's prompt. The Record is where they wait.
			queued := make([]gotato.Queued, len(kept))
			for i, notice := range kept {
				queued[i] = notice.Queued()
			}
			if rest := live.Requeue(queued); len(rest) > 0 {
				live.DropQueued(rest, "queue_full")
			}
		}
		if follow != nil {
			// The messages that waited behind the first one wait in this Run's
			// queues, in order, before anything new can reach them.
			if rest := live.Requeue(follow.rest); len(rest) > 0 {
				live.DropQueued(rest, "queue_full")
			}
		}
		started = true
		// From here a stop aborts the Run inside the live Session, so Prompt
		// still returns the settled (cancelled) result. Prompt waits on the
		// caller's context, not runCtx: a stop that cancelled runCtx while the
		// Session was loading reaches the Run through abortIfStopped instead.
		handle.attachAbort(live.Abort)
		handle.attachLive(live)

		// The Run's deadline: the request's Timeout, else the Agent's
		// RunTimeout. The caller's own deadline on ctx overrides it when
		// it is shorter, as any child context's does.
		runDeadline := request.Timeout
		if runDeadline == 0 {
			runDeadline = spec.RunTimeout
		}
		if runDeadline == 0 {
			runDeadline = DefaultRunTimeout
		}
		var result gotato.RunResult
		func() {
			runCtx := ctx
			if runDeadline > 0 {
				var cancelDeadline context.CancelFunc
				runCtx, cancelDeadline = context.WithTimeout(ctx, runDeadline)
				defer cancelDeadline()
			}
			switch {
			case request.Continue:
				result, runErr = live.Continue(runCtx)
			case follow != nil:
				result, runErr = live.Prompt(runCtx, follow.prompt)
			default:
				result, runErr = live.Prompt(runCtx, gotato.UserMessage(request.Prompt))
			}
		}()
		// The messages the Run left in its queues are taken in the same step
		// that stops delivering to it, and a Run follows with them unless
		// something ends the work they were meant for.
		if r.beforeDetach != nil {
			r.beforeDetach()
		}
		// A notice from a service the Run left waits for the next Run, even
		// when the Run was stopped, since it asks for no work: its
		// notice_queued is in the Record, and the next Run queues it again.
		// Gotato's own notices, such as time_away, mattered only at the time.
		notices := handle.detachLive(func(left []gotato.Queued) bool {
			why := ""
			switch {
			case writer == nil:
				why = "run_ended"
			case writer.Failed() != nil:
				why = "writer_failed"
			case r.Draining():
				why = "draining"
			case handle.isStopped() || runCtx.Err() != nil:
				why = "run_ended"
			}
			if why != "" {
				live.DropQueued(left, why)
				return false
			}
			return true
		})
		for _, notice := range notices {
			if notice.Message.Notice != nil && notice.Message.Notice.Source == gotato.NoticeFromGotato {
				live.DropQueued([]gotato.Queued{notice}, "run_ended")
			}
		}
		_ = live.Close(context.Background())
		if stream != nil && stream.sub != nil {
			stream.sub.End()
		}

		if writer != nil {
			// Persist the settled Session even when the Run was cancelled, but
			// bound the write so a stalled Store cannot pin the Run (and Drain)
			// forever.
			wroteAll := writer.Failed() == nil
			saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
			saveErr := writer.Release(saveCtx)
			cancelSave()
			r.notice(savedEvent(s.ID(), result.RunID, s.Len(), writer.Stats().Since(saved), saveErr), spec.Name)
			r.noteFailure(saveErr, s.ID(), spec.Name)
			if saveErr == nil && wroteAll {
				// The Session stays in memory, as a cache the store can always
				// replace; the next Run checks its head.
				r.keepWarm(writer, spec)
			}
			// Only the release failed: every fact of the Run is stored, so the
			// Run keeps its status. The warning Events above tell the operator,
			// and the Claim lapses with the Liveness row.
			if saveErr != nil && !wroteAll {
				// An earlier write of the Run failed, and the Writer refuses
				// the release too: keep the Run error and add the persistence
				// failure so callers can observe both (errors.Is still matches
				// each).
				var saveFailure *gotato.RuntimeError
				if !errors.As(saveErr, &saveFailure) {
					saveFailure = gotato.ErrorOf(gotato.ErrStoreFailure, saveErr.Error())
					saveFailure.Cause = saveErr
					saveErr = saveFailure
				}
				// completed never carries an error. The Run's own writes
				// settle it as failed, so this only guards the rule.
				if result.Status == gotato.RunCompleted {
					result.Status = gotato.RunFailed
					result.Error = saveFailure
				}
				persistErr := fmt.Errorf("%w: %w", ErrNotPersisted, saveErr)
				if runErr == nil {
					runErr = persistErr
				} else {
					runErr = errors.Join(runErr, persistErr)
				}
			}
		}
		r.noteFailure(runErr, s.ID(), spec.Name)
		out = RunResult{
			SessionID: s.ID(),
			Agent:     spec.Name,
			Model:     spec.ModelName,
			Result:    result,
			Compacted: auto != nil && compacted(auto),
			Messages:  s.Len(),
			Events:    len(s.Events()),
		}
		if writer != nil {
			out.RecordEpoch = s.RecordEpoch()
		}
		if request.SkipSave {
			// The Run stored nothing: its Events are those of its own stream,
			// numbered from 1, so the last number counts them.
			out.Events = int(feed.Last())
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

// noteFailure publishes the Instance Event for a write that failed: a
// session_fenced when another Instance holds the Claim, and a
// store_write_failed for any other store failure. A write refused because this
// Instance's Liveness row lapsed is reported by liveness_lapsed instead.
func (r *Runner) noteFailure(err error, sessionID, agent string) {
	if err == nil || lapsed(err) {
		return
	}
	header := gotato.NewHeader(gotato.SessionID(sessionID), "", 0)
	switch {
	case errors.Is(err, session.ErrConflict):
		r.notice(gotato.SessionFenced{Header: header, Instance: r.instance}, agent)
	case gotato.IsCode(err, gotato.ErrStoreFailure):
		r.notice(gotato.StoreWriteFailed{Header: header, Code: gotato.ErrStoreFailure}, agent)
	}
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
	return r.sessionLock(ctx, sessionID, false, fn)
}

// sessionLock is withSessionLock; forceWait makes the caller wait for a busy
// Session whatever the admission policy, as the Run that follows another does.
func (r *Runner) sessionLock(ctx context.Context, sessionID string, forceWait bool, fn func() error) error {
	// A message that arrives while the Session compacts while idle cancels
	// that compaction and is served first, so it never finds the Session busy.
	unhold, err := r.holdWarm(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unhold()
	onWait := func() {
		r.notice(gotato.RunWaiting{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy"}, "")
	}
	if err := r.locks.acquire(ctx, sessionID, forceWait || r.admission.Queue == WaitWhileBusy, onWait); err != nil {
		if errors.Is(err, ErrBusy) {
			r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy"}, "")
		}
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
// inspection; a Run passes none. With a writer, the writer is its Transcript,
// so every step of a Run is written as it happens; without one the Record is,
// and keeps the Session's Events in memory. extra options come after the
// Agent's own.
func (r *Runner) buildSession(ctx context.Context, spec Agent, s *session.Record, writer *session.Writer, override InspectOptions, extra ...gotato.Option) (*gotato.Session, *modelctx.AutoCompactor, error) {
	// Without a writer nothing is stored: the live Session numbers its Events
	// on its own stream, and does not use the Record's numbers, which are
	// positions in the log that this Run does not write.
	var transcript gotato.Transcript = unjournaled{s}
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
	// Every Run has its Session compacted when the provider refuses a request
	// as too long, and past the ceiling when there is one.
	auto := modelctx.AutoCompact(s, compactPolicyFor(spec, s))
	extensions := append([]any{auto}, spec.Extensions...)

	options := []gotato.Option{
		gotato.WithModel(spec.Model),
		gotato.WithInstruction(instruction),
		gotato.WithTranscript(transcript),
		gotato.WithContextBuilder(builder),
		gotato.WithCacheKey(sessionCacheKey(s)),
	}
	if len(extensions) > 0 {
		options = append(options, gotato.WithExtensions(extensions...))
	}
	filter := &sessionTools{spec: spec, session: s}
	options = append(options, gotato.WithToolSource(&sessionTools{spec: spec, session: s, segments: r.segmentTools(spec, s), results: r.resultTools(s)}),
		// A Tool switched off for the Session is never in the catalog, found or loaded.
		gotato.WithToolFilter(filter.active))
	if spec.MaxEffect != gotato.EffectUnspecified {
		options = append(options, gotato.WithMaxEffect(spec.MaxEffect))
	}
	if spec.Limits != nil {
		options = append(options, gotato.WithLimits(*spec.Limits))
	}
	options = append(options, spec.Options...)
	options = append(options, extra...)
	agent, err := gotato.NewSessionContext(ctx, options...)
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
	live, auto, err := r.buildSession(ctx, spec, s, nil, override)
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
		report.CompactionPending = auto.PendingFor(inspection.Request)
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
	// segments gives the built-in read_segment tool once the Session has a
	// Segment; nil for a surface that is not a Session's.
	segments gotato.ToolSource
	// results gives the built-in read_result tool to a Session whose Agent
	// has any tool; nil for a surface that is not a Session's.
	results gotato.ToolSource
}

// segmentTools is the ToolSource that offers read_segment for a Session of the
// Agent spec: the Messages are read back from the Runner's store, in pages
// that fit one page, in the zone of its Segments' times.
func (r *Runner) segmentTools(spec Agent, record *session.Record) gotato.ToolSource {
	options := modelctx.ReadSegmentOptions{Location: spec.Compact.Location}
	return modelctx.SegmentTools(record, r.store, options)
}

// resultTools is the ToolSource that offers read_result: a spilled result is
// read back from the live Record or from the Runner's store.
func (r *Runner) resultTools(record *session.Record) gotato.ToolSource {
	return modelctx.ResultTools(record, r.store, modelctx.ReadResultOptions{})
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
	// read_result is offered whenever the Agent has a tool, so the tool list
	// does not change on the Turn a result is first spilled.
	if t.results != nil && len(tools) > 0 {
		for _, tool := range t.results.Tools() {
			if t.allowed(tool.Spec()) && t.active(tool.Spec().ID) {
				tools = append(tools, tool)
			}
		}
	}
	if t.segments != nil {
		for _, tool := range t.segments.Tools() {
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
	if s != nil {
		// A Session that has a Segment offers read_segment too.
		all.segments = r.segmentTools(spec, s)
		all.results = r.resultTools(s)
	}
	entries := make([]ToolEntry, 0)
	for _, tool := range all.Tools() {
		entries = append(entries, ToolEntry{Spec: tool.Spec(), Active: filter.active(tool.Spec().ID)})
	}
	if s != nil {
		// A Session's ToolSets live in the Agent's Options, so the live Session
		// says what it offers now: search_tools, whose description is the
		// catalog, and the catalog tools the Session has loaded.
		live, _, err := r.buildSession(ctx, spec, s, nil, InspectOptions{})
		if err != nil {
			return nil, err
		}
		defer func() { _ = live.Close(context.Background()) }()
		for _, offered := range live.Tools() {
			if !slices.ContainsFunc(entries, func(e ToolEntry) bool { return e.Spec.ID == offered.ID }) {
				entries = append(entries, ToolEntry{Spec: offered, Active: true})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Spec.ID < entries[j].Spec.ID })
	}
	return entries, nil
}

// SetToolActive records a Session-level activation change and writes it as
// the Session's writer, under the Session lock.
func (r *Runner) SetToolActive(ctx context.Context, sessionID, toolID string, active bool) (ToolEntry, error) {
	var found *gotato.ToolSpec
	err := r.withSessionLock(ctx, sessionID, func() error {
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		s := writer.Record()
		agent, _ := s.Get(MetaAgent)
		r.publishOpened(writer, early, agent)
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
		err = writer.Release(ctx)
		if err == nil {
			r.keepWarm(writer, spec)
		}
		return err
	})
	if err != nil {
		return ToolEntry{}, err
	}
	return ToolEntry{Spec: *found, Active: active}, nil
}

// Compact compacts a Session explicitly, under the Session lock, and closes a
// Segment (modelctx.Compact), and merges the oldest Segments when they then pass
// their budget. What opts leaves unset comes from the Session's
// Agent: its Recap writer, its time zone and limits, the Model's window, the
// ceiling in force, the Segments' budget, and, with neither Keep nor Floor, its Floor. When the
// Agent's Model is sent only the current Round's reasoning
// (modelctx.DropsReasoning), the kept Messages lose their reasoning.
func (r *Runner) Compact(ctx context.Context, sessionID string, opts modelctx.CompactOptions) (modelctx.Result, error) {
	var result modelctx.Result
	err := r.withSessionLock(ctx, sessionID, func() error {
		writer, early, _, err := r.acquire(ctx, sessionID)
		if err != nil {
			return err
		}
		agentName, _ := writer.Record().Get(MetaAgent)
		r.publishOpened(writer, early, agentName)
		spec, known := r.Agent(agentName)
		if known {
			opts = compactOptionsFor(spec, writer.Record(), opts)
			if opts.Recap != nil && opts.Context == nil {
				opts.Context = r.recapContext(spec, writer.Record())
			}
		}
		result, err = modelctx.Compact(ctx, writer.Record(), opts)
		releaseErr := writer.Release(ctx)
		if known && releaseErr == nil {
			r.keepWarm(writer, spec)
		}
		if err != nil {
			return err
		}
		return releaseErr
	})
	if err != nil {
		return modelctx.Result{}, err
	}
	return result, nil
}

// compactOptionsFor fills what opts leaves unset from the Agent's compaction
// settings and its Model.
func compactOptionsFor(spec Agent, record *session.Record, opts modelctx.CompactOptions) modelctx.CompactOptions {
	policy := compactPolicyFor(spec, record)
	opts.DropReasoning = opts.DropReasoning || policy.DropReasoning
	if opts.Recap == nil {
		opts.Recap = policy.Recap
	}
	opts.Recap = withAgentModel(opts.Recap, spec.Model)
	if opts.Keep <= 0 && opts.Floor <= 0 {
		opts.Floor = policy.Floor
	}
	if opts.Window <= 0 {
		opts.Window = policy.Window
	}
	if opts.Ceiling <= 0 {
		opts.Ceiling = policy.EffectiveCeiling()
	}
	if opts.Location == nil {
		opts.Location = policy.Location
	}
	if opts.SegmentBudget <= 0 {
		opts.SegmentBudget = policy.SegmentBudget
	}
	opts.MessageTokens = cmpPositive(opts.MessageTokens, policy.MessageTokens)
	opts.ToolLines = cmpPositive(opts.ToolLines, policy.ToolLines)
	opts.ArgChars = cmpPositive(opts.ArgChars, policy.ArgChars)
	opts.TailTokens = cmpPositive(opts.TailTokens, policy.TailTokens)
	return opts
}

func cmpPositive(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// withAgentModel gives a modelctx.ModelRecap that names no Model the Agent's
// own, so an Agent turns the model's Recap on with Recap: modelctx.ModelRecap{}.
func withAgentModel(writer modelctx.RecapWriter, model gotato.Model) modelctx.RecapWriter {
	if recap, ok := writer.(modelctx.ModelRecap); ok && recap.Model == nil {
		recap.Model = model
		return recap
	}
	return writer
}

// recapContext returns what a Recap written by a model sends as its prefix:
// the request the Agent's next model call would send for the Session now,
// built by a live Session that is inspected and not run.
func (r *Runner) recapContext(spec Agent, record *session.Record) func(context.Context) (gotato.ModelRequest, error) {
	return func(ctx context.Context) (gotato.ModelRequest, error) {
		live, _, err := r.buildSession(ctx, spec, record, nil, InspectOptions{})
		if err != nil {
			return gotato.ModelRequest{}, err
		}
		defer func() { _ = live.Close(context.Background()) }()
		inspection, err := live.InspectContext(ctx)
		if err != nil {
			return gotato.ModelRequest{}, err
		}
		return inspection.Request, nil
	}
}

// compactPolicyFor is the Agent's CompactPolicy with the Model's window and
// the Session's own ceiling (MetaCompactCeiling) applied.
func compactPolicyFor(spec Agent, record *session.Record) modelctx.CompactPolicy {
	policy := spec.Compact
	policy.Recap = withAgentModel(policy.Recap, spec.Model)
	policy.DropReasoning = policy.DropReasoning || modelctx.DropsReasoning(spec.Model)
	if policy.Window <= 0 {
		policy.Window = modelctx.WindowOf(spec.Model)
	}
	if raw, ok := record.Get(MetaCompactCeiling); ok {
		if ceiling, err := strconv.Atoi(raw); err == nil && ceiling > 0 {
			policy.Ceiling = ceiling
		}
	}
	return policy
}

// DeleteSession removes a Session under the Session lock, so a Run in flight
// is never resurrected by a delete or vice versa.
func (r *Runner) DeleteSession(ctx context.Context, sessionID string) error {
	return r.withSessionLock(ctx, sessionID, func() error {
		r.dropWarm(sessionID, "deleted")
		if err := r.store.Delete(ctx, sessionID); err != nil {
			return err
		}
		r.notice(gotato.SessionDeleted{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0)}, "")
		r.endSessionStreams(sessionID, session.ErrNotFound)
		return nil
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
	childAgent, _ := child.Get(MetaAgent)
	if _, err := r.create(ctx, child, childAgent, session.WithIdle()); err != nil {
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

// CancelSession stops the Run in flight on a Session: everything in flight is
// cancelled through its context, and the Run settles as cancelled with every
// Turn it committed. It is the Stop of the messages a Run takes (Send).
//
// A Run going on another live Instance is not stopped from here: the call
// returns not_writer (NotRun, Retryable) with the writer's advertised address,
// as Send does. With no Run going anywhere it returns ErrNoRun, and for a
// Session the store does not know (or refuses the caller), not_found.
func (r *Runner) CancelSession(ctx context.Context, sessionID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	handles := append([]*runHandle(nil), r.handles[sessionID]...)
	r.mu.Unlock()
	// A Run that has a live Session has read the Record; one still loading it
	// has not, and the Record may not exist yet.
	if slices.ContainsFunc(handles, (*runHandle).hasLive) {
		if err := r.reachStore(ctx, sessionID); err != nil {
			return err
		}
	}
	if r.cancelSession(sessionID) {
		return nil
	}
	if err := r.checkWriter(ctx, sessionID); err != nil {
		return err
	}
	return ErrNoRun
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

// stopAll stops every admitted Run for the drain: started Runs through their
// live Session, Runs still waiting for a Session lock through their context.
func (r *Runner) stopAll() {
	r.mu.Lock()
	var all []*runHandle
	for _, list := range r.handles {
		all = append(all, list...)
	}
	r.mu.Unlock()
	for _, handle := range all {
		handle.stopForDrain()
	}
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
	handle.finish()
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

// Drain stops this instance in order, so a deploy loses no work:
//
//  1. its Liveness row says draining, so every Instance can see it;
//  2. Draining reports true, so /readyz answers 503, and the Runner admits
//     no new Run and takes no Session over from another Instance
//     (ErrDraining); creating or forking a Session activates nothing, so it
//     still works;
//  3. every Run in flight finishes, is saved, and releases its Session.
//
// When ctx ends first, the Runs still going are stopped, saved and released,
// and each returns with a Reconnect once every Run of the drain has settled;
// Drain then returns ctx.Err(). A Run that cannot be saved in time is fenced
// by the next Claim anyway. The Runner keeps renewing its row while it drains;
// call Close afterwards to remove it. A signal handler or an endpoint of your
// own calls Drain.
func (r *Runner) Drain(ctx context.Context) error {
	r.mu.Lock()
	first := r.drained == nil
	if first {
		r.drained = make(chan struct{})
	}
	drained := r.drained
	r.mu.Unlock()
	var markErr error
	if first {
		markErr = r.markDraining(ctx)
		r.mu.Lock()
		r.draining = true
		r.mu.Unlock()
		go func() {
			r.mu.Lock()
			for r.inflight > 0 {
				r.idle.Wait()
			}
			r.mu.Unlock()
			close(drained)
		}()
	}
	select {
	case <-drained:
		r.dropAllWarm("drain")
		r.endStreams()
		return markErr
	case <-ctx.Done():
		r.stopAll()
		<-drained
		r.dropAllWarm("drain")
		r.endStreams()
		return errors.Join(markErr, ctx.Err())
	}
}

// admit decides whether a Run may start, and registers its handle when it
// may. A draining Instance refuses first; then a Run that would activate a
// Session past MaxActiveSessions is refused, because another Instance can
// still take it, unless a warm Session can be evicted to make room; then
// MaxActiveRuns applies to every Run.
func (r *Runner) admit(handle *runHandle) error {
	for {
		victim, err := r.tryAdmit(handle)
		if victim == nil {
			return err
		}
		// Evicting waits for an idle compaction to unwind, so it happens
		// without the Runner's lock; the count is taken again after it.
		r.dropEntry(victim, "evicted")
	}
}

// tryAdmit is one pass of admit under the Runner's lock. When the Run needs
// room and a warm Session can give it, it returns that Session to evict and
// registers nothing.
func (r *Runner) tryAdmit(handle *runHandle) (victim *warmEntry, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining {
		return nil, ErrDraining
	}
	if limit := r.admission.MaxActiveSessions; limit > 0 {
		warm, evictable := r.warmCountLocked(handle.sessionID)
		if (handle.sessionID == "" || (len(r.handles[handle.sessionID]) == 0 && !warm)) && r.liveSessionsLocked() >= limit {
			if evictable == nil {
				return nil, ErrOverloaded
			}
			return evictable, nil
		}
	}
	if r.admission.MaxActiveRuns > 0 && r.inflight >= r.admission.MaxActiveRuns {
		return nil, ErrCapacity
	}
	r.inflight++
	r.handles[handle.sessionID] = append(r.handles[handle.sessionID], handle)
	return nil, nil
}

func (r *Runner) release() {
	r.mu.Lock()
	r.inflight--
	if r.inflight == 0 {
		r.idle.Broadcast()
	}
	r.mu.Unlock()
}

// unjournaled is a Record as a Transcript that is not a Journal, so a Session
// over it keeps no Events in the Record.
type unjournaled struct{ record *session.Record }

func (u unjournaled) Len() int                      { return u.record.Len() }
func (u unjournaled) Messages() []gotato.Message    { return u.record.Messages() }
func (u unjournaled) Append(m gotato.Message) error { return u.record.Append(m) }
func (u unjournaled) ID() string                    { return u.record.ID() }
func (u unjournaled) LoadedTools() []string         { return u.record.LoadedTools() }
