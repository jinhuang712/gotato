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
// the limit a new Run evicts the longest-idle one first. Admission.MaxWarmSessions
// bounds the warm ones alone (10000 unless set), by the same eviction.
//
//	runner, _ := service.New(service.Config{
//	    Store:  store,
//	    Agents: []service.Agent{{Name: "default", Model: model, Tools: tools}},
//	})
//	result, _ := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hello"})
//
// Package httpapi exposes a Runner over HTTP; the gotato CLI drives one
// in-process. Both are thin.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// persistContext returns a context for a write that must happen even when ctx
// is canceled: it keeps ctx's values, drops its cancellation, and ends after
// session.WriteTimeout, so a stalled Store cannot pin a settled Run. It bounds
// the terminal Session write and the cleanup delete, as the Writer bounds each
// of its own writes.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// [DATA-LOSS] the terminal write of a canceled Run still happens, and a stalled Store cannot hold it for good.
	return context.WithTimeout(context.WithoutCancel(ctx), session.WriteTimeout)
}

// defaultRunTimeout is how long a Run of an Agent may last when neither the
// Agent's RunTimeout nor the request's Timeout says otherwise.
const defaultRunTimeout = 30 * time.Minute

// DefaultDrainTimeout is how long Drain waits for the Runs in flight when
// Config.DrainTimeout says nothing: long enough for most Runs to finish during a
// rolling deploy.
const DefaultDrainTimeout = 10 * time.Second

// DefaultToolSetTimeout is how long Runner.Tools and Runner.SetToolActive wait
// for ToolSets when Config.ToolSetTimeout says nothing.
const DefaultToolSetTimeout = 10 * time.Second

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
//
// A Tool of a ToolSet carries the ToolSet's name and whether it is loaded
// (Loaded; Active says whether the Session switched it off). A ToolSet that failed its last resolution is one
// entry with the ToolSet's name as Spec.ID and Unavailable set.
type ToolEntry struct {
	Spec        gotato.ToolSpec `json:"spec"`
	Active      bool            `json:"active"`
	ToolSet     string          `json:"toolset,omitempty"`
	Loaded      bool            `json:"loaded,omitempty"`
	Unavailable bool            `json:"unavailable,omitempty"`
}

// Agent is a named, reusable definition: the model, instruction, tools and
// limits its Sessions run. The Runner builds a live Session from it for each
// Run. Sharing a Model client between Agents and Runs is expected; nothing
// here is mutable Run state.
//
// Three fields give an Agent its Tools; each has one job:
//   - Tools: Go Tools (gotato.NewFuncTool and the like), fixed for the
//     Agent's life.
//   - Options: a gotato.ToolSet, such as an MCP server's, goes in with
//     gotato.WithToolSet, WithActiveToolSet or WithFoldedToolSet. The three
//     differ in how the Model sees the set (listed, always visible, or
//     folded behind search_tools).
//   - ToolSources: Tools that change between Turns, asked again at the
//     start of each Turn. A ToolSet is not a ToolSources entry.
type Agent struct {
	Name        string
	Model       gotato.Model
	ModelName   string
	Instruction string
	// Tools are Go Tools, the same for every Turn.
	Tools []gotato.Tool
	// ToolSources supply Tools that change between Turns (gotato.ToolSource).
	// Tools, ToolSources, read_result and read_segment are one ToolSource to
	// the Session: a Tool ID that two of them supply fails it whole, as any
	// bad Tool does (gotato.Tool). To attach a ToolSet, use Options with gotato.WithToolSet,
	// gotato.WithActiveToolSet or gotato.WithFoldedToolSet instead.
	ToolSources []gotato.ToolSource
	// MaxEffect is the highest Effect a Tool of this Agent may have. A Tool
	// above it is hidden from the model and from Runner.Tools, and no Session
	// can switch it on. Empty means no cap (gotato.WithMaxEffect).
	MaxEffect gotato.ToolEffect
	// ContextBuilder defaults to gotato.FullHistoryContext().
	ContextBuilder gotato.ContextBuilder
	Limits         *gotato.CoreLimits
	// RunTimeout bounds one Run of this Agent; the Run settles as
	// deadline_exceeded. Zero means 30 minutes, negative means no
	// deadline. A RunRequest's Timeout replaces it, and a deadline on the
	// caller's context that comes sooner ends the Run sooner. The core sets
	// no Run deadline of its own: there a Run's deadline is its ctx.
	RunTimeout time.Duration
	// Compact says when the Agent's Sessions compact by themselves: past the
	// ceiling, which is the Model's window less a reserve unless CeilingTokens sets
	// one (a negative CeilingTokens turns it off), and once when the provider refuses
	// a request as too long. A Session may override the ceiling through
	// MetaCompactCeiling.
	Compact modelctx.CompactPolicy
	// Warm says how long the Agent's idle Sessions stay in memory after a
	// Run, and whether they are compacted while they idle. The zero value is
	// the default: 60 minutes, and an idle compaction.
	Warm WarmPolicy
	// TimeAwayAfter is how long a Session must have been idle before a
	// message that starts a Run is followed by a time_away notice with how
	// long it has been and the time now (G-F29). Zero means 30 minutes;
	// a negative value turns the notice off. The time is shown
	// in the zone of Compact.Location, UTC when it is nil.
	TimeAwayAfter time.Duration
	// Options are appended verbatim for anything the fields above do not
	// cover. They are where a gotato.ToolSet is attached: gotato.WithToolSet,
	// gotato.WithActiveToolSet or gotato.WithFoldedToolSet (an MCP server's
	// tools arrive this way).
	Options []gotato.SessionOption
}

// QueuePolicy decides what happens when a Session is busy.
type QueuePolicy string

const (
	// QueuePolicyReject fails a second concurrent Run on the same Session with
	// ErrBusy (HTTP 409).
	QueuePolicyReject QueuePolicy = "reject"
	// QueuePolicyWait queues the Run until the Session is free or the caller's
	// context ends.
	QueuePolicyWait QueuePolicy = "wait"
)

// Admission bounds concurrent work. Zero values disable a bound.
type Admission struct {
	// MaxActiveRuns is the limit on the Runs this Instance has in flight. Zero
	// means no limit; a negative value is refused by New.
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
	// MaxWarmSessions bounds the idle Sessions this Instance keeps in memory
	// (WarmPolicy). When one more would pass it, the longest-idle warm Session
	// leaves memory (session_unloaded, why evicted); the store keeps it, and
	// its next Run loads it. A warm Session is a cache, so this is a memory
	// bound and refuses nothing. Zero means DefaultMaxWarmSessions, and a
	// negative value means no bound beyond MaxActiveSessions and the TTL. The
	// memory a warm Session takes depends on its length: about 8 KB for ten
	// Messages and 570 KB for a thousand (BenchmarkWarmSessionFootprint).
	MaxWarmSessions int
	Queue           QueuePolicy
}

// DefaultMaxWarmSessions is the bound on warm Sessions when
// Admission.MaxWarmSessions says nothing: generous for an Instance of a few
// gigabytes, and a limit all the same, so that a Runner left at its defaults
// does not hold every Session of the last hour.
const DefaultMaxWarmSessions = 10000

// Config configures a Runner.
//
// Several processes may share one Store, and so one FileStore directory:
// each is an Instance with its own name, and a Session is written by one
// Instance at a time (a Claim), whichever Instance served its last Run. To
// stop an Instance, call Drain, then shut down the HTTP server (for
// httpapi: http.Server.Shutdown), then Close. Drain comes first because it
// lets the Runs in flight finish, which their open HTTP requests are waiting
// for, and ends the Session streams, which Shutdown would otherwise wait on
// for ever; Close comes last because it removes the Instance's Liveness row,
// after which the Runner writes nothing more.
type Config struct {
	// Store keeps the Sessions' Records. It may be shared by several
	// processes (see above).
	Store     session.Store
	Agents    []Agent
	Admission Admission
	// Instance is a human-readable label for this Runner, such as a Kubernetes pod name.
	// It is not the identity: each start appends a random suffix, so the name
	// the Runner writes to the store, in its Claims, its Liveness row and its
	// session_claimed Events, is unique per process start, for example
	// "pod-0-3fa9c1d2". Runner.Instance returns that name. Empty generates a
	// whole new name. A label may therefore be the same across restarts, or
	// shared by two live processes: the new process never takes the old
	// process's Claim as its own, it goes through the holder check like any
	// other Instance, and the old process's Close removes only its own row.
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
	// ToolSetTimeout bounds how long Runner.Tools and Runner.SetToolActive
	// wait for the Agent's ToolSets to answer. Zero means DefaultToolSetTimeout;
	// a negative value means no bound beyond the caller's context. A Run does
	// not use it: a Run is bounded by its own deadline.
	ToolSetTimeout time.Duration
	// DrainTimeout is how long Drain lets the Runs in flight finish before it
	// stops them. Zero means DefaultDrainTimeout; a negative value means no
	// bound beyond the context Drain is given, which can end the wait sooner
	// in any case.
	DrainTimeout time.Duration
	// clock times the warm Sessions: their TTL and their idle compaction.
	// Nil means the system clock; the tests of this package set it.
	clock clock
}

// Runner serves Runs against Sessions.
type Runner struct {
	store     session.Store
	label     string
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
	clock clock
	// beforeDetach is a test seam: it runs when a Run has settled and its live
	// Session still takes messages (see export_test.go).
	beforeDetach func()
	// onSendRetry is a test seam: it runs each time Send goes round its loop
	// again.
	onSendRetry func()
	// beforeRelease is a test seam: it runs when a Run no longer takes
	// messages and still holds the Session.
	beforeRelease func()
	warmMu        sync.Mutex
	warm          map[string]*warmEntry
	warmClosed    bool
	warmSeq       uint64

	// unreleased holds the Sessions whose release write failed. The Claim
	// still names this Instance, so the Session is marked busy under a live
	// holder until the heartbeat writes the release again (repairReleases).
	releaseMu  sync.Mutex
	unreleased map[string]struct{}

	// instanceFeed is the Instance's stream; feeds are the Feeds of the
	// Records in use, one per Session ID and epoch, each mirrored to it (see
	// events.go).
	instanceFeed *gotato.Feed
	feedsMu      sync.Mutex
	feeds        map[feedKey]*sessionFeed

	mu       sync.Mutex
	handles  map[string][]*runHandle
	inflight int
	// locked counts the callers inside sessionLock, Runs among them: Compact,
	// Notice, SetToolActive and DeleteSession hold a Claim without being
	// admitted as Runs, and Drain waits for them too.
	locked   int
	draining bool
	// streams are the Session streams open for clients, and streamsOver says
	// the drain is over, so a new one ends at once.
	streamsMu   sync.Mutex
	streams     map[*SessionStream]struct{}
	streamsOver bool
	// streamStarts counts the starts of Session streams (SessionStream.startedAt).
	streamStarts uint64
	streamPoll   time.Duration
	// toolSetTimeout bounds the ToolSet resolution of Tools and SetToolActive.
	toolSetTimeout time.Duration
	// drained is made by the first Drain and closed once every Run admitted
	// before it has settled and released its Session.
	drained chan struct{}
	idle    *sync.Cond
	// drainTimeout bounds how long Drain waits for the Runs; negative is none.
	drainTimeout time.Duration
	// drainStop is a test seam: drainStopWait when zero.
	drainStop time.Duration
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
		store:          cfg.Store,
		label:          strings.TrimSpace(cfg.Instance),
		liveness:       cfg.Liveness.withDefaults(),
		address:        strings.TrimSpace(cfg.Address),
		streamPoll:     cfg.StreamPoll,
		toolSetTimeout: cfg.ToolSetTimeout,
		drainTimeout:   cfg.DrainTimeout,
		agents:         map[string]Agent{},
		admission:      cfg.Admission,
		locks:          newSessionLocks(),
		clock:          cfg.clock,
		warm:           map[string]*warmEntry{},
		feeds:          map[feedKey]*sessionFeed{},
		handles:        map[string][]*runHandle{},
	}
	// The label is never the identity: the suffix makes the name unique per
	// start, so a restarted or overlapping process cannot share a Liveness
	// row or a Claim with the one it replaces.
	r.beat.instance = r.newName()
	if r.clock == nil {
		r.clock = systemClock{}
	}
	// [CONTRACT] a negative MaxActiveRuns is refused, as MaxActiveSessions is, so a typo does not read as no limit.
	if r.admission.MaxActiveRuns < 0 {
		return nil, errors.New("service: MaxActiveRuns cannot be negative")
	}
	if r.admission.MaxActiveSessions < 0 {
		return nil, errors.New("service: MaxActiveSessions cannot be negative")
	}
	if r.admission.MaxWarmSessions == 0 {
		r.admission.MaxWarmSessions = DefaultMaxWarmSessions
	}
	if r.admission.Queue == "" {
		r.admission.Queue = QueuePolicyReject
	}
	if r.admission.Queue != QueuePolicyReject && r.admission.Queue != QueuePolicyWait {
		return nil, fmt.Errorf("service: unknown queue policy %q", r.admission.Queue)
	}
	if r.drainTimeout == 0 {
		r.drainTimeout = DefaultDrainTimeout
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

// Instance returns the name this Runner writes its Claims with: Config.Instance
// and a random suffix, or a generated name when none was set. It is new for
// each start, and new again when the Runner took a new name after its Liveness
// row expired (see Lapsed).
func (r *Runner) Instance() string { return r.id() }

// Record reads a Record's whole Record from the store: its facts, Runs,
// usage and Events. It changes nothing.
func (r *Runner) Record(ctx context.Context, sessionID string) (*session.Record, error) {
	record, err := session.View(ctx, r.store, sessionID)
	return record, storeFailureOf(ctx, err, true)
}

// Events reads a Session's stored Events from the store: every Entry of its
// Record that is an Event, in the order of their numbers, as typed Events.
func (r *Runner) Events(ctx context.Context, sessionID string, opts ...EventsOption) ([]gotato.Event, error) {
	var config eventsConfig
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	events, err := session.StoredEvents(ctx, r.store, sessionID, 0, 0)
	return events, storeFailureOf(ctx, err, true)
}

// Sessions lists the stored Sessions, the most recently updated first.
func (r *Runner) Sessions(ctx context.Context, opts ...ListOption) ([]session.RecordInfo, error) {
	var config listConfig
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	list, err := r.store.List(ctx)
	return list, storeFailureOf(ctx, err, true)
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
