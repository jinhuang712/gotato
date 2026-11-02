// Package host hosts many Sessions on many instances: AgentProfiles registered
// by name (package profile), a Store of Session Records (package store), and a
// live Session built for each Run, with the Claims, the heartbeat, the warm
// copies, the drain and the admission that keep any instance able to serve any
// Session.
//
// Session semantics stay in the runtime. For every Run the Runner loads the
// Session's Record, takes the Session's lock, looks up the Session's
// AgentProfile by name, builds a live gotato.Session from it exactly as an
// application would (WithTranscript,
// WithContextBuilder, WithToolSource, WithFeed,
// record.AutoCompact), runs it, closes it, and saves the Record.
// Continuity lives in the Store, so any process holding the Store can serve
// any Session.
//
// After a Run the Session stays in memory as a warm copy (Config.Warm,
// WarmPolicy): a cache, never a lock. It holds no Claim while it idles, the
// next Run uses it only while the Store's head is still the copy's, and it
// is compacted by its own timer before its prompt cache expires, then leaves
// at its TTL. Warm Sessions count toward Admission.MaxActiveSessions: at
// the limit a new Run evicts the longest-idle one first. Admission.MaxWarmSessions
// bounds the warm ones alone (10000 unless set), by the same eviction.
//
//	profiles := profile.NewRegistry(nil)
//	_ = profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "default", Model: model, Tools: tools})
//	runner, _ := host.New(host.Config{
//	    Store:        st,
//	    Profiles:     profiles,
//	    DefaultAgent: "default",
//	})
//	result, _ := runner.Run(ctx, host.RunRequest{SessionID: id, Prompt: "hello"})
//
// Package adapter/http exposes a Runner over HTTP; the gotato CLI drives one
// in-process. Both are thin.
package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host/internal/clock"
	"github.com/jinhuang712/gotato/host/internal/heartbeat"
	"github.com/jinhuang712/gotato/host/internal/runs"
	"github.com/jinhuang712/gotato/host/internal/warm"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
)

// persistContext returns a context for a write that must happen even when ctx
// is canceled: it keeps ctx's values, drops its cancellation, and ends after
// store.WriteTimeout, so a stalled Store cannot pin a settled Run. It bounds
// the terminal Session write and the cleanup delete, as the Writer bounds each
// of its own writes.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// [DATA-LOSS] the terminal write of a canceled Run still happens, and a stalled Store cannot hold it for good.
	return context.WithTimeout(context.WithoutCancel(ctx), store.WriteTimeout)
}

// DefaultDrainTimeout is how long Drain waits for the Runs in flight when
// Config.DrainTimeout says nothing: long enough for most Runs to finish during a
// rolling deploy.
const DefaultDrainTimeout = 10 * time.Second

// DefaultToolSetTimeout is how long Runner.Tools and Runner.SetToolActive wait
// for ToolSets when Config.ToolSetTimeout says nothing.
const DefaultToolSetTimeout = 10 * time.Second

// ReservedPrefix starts every Session metadata key the Runner owns. A caller
// that sets a key with this prefix (CreateSession, RunRequest.Metadata,
// RunRequest.Update, or the HTTP API) is refused with ErrReservedKey before
// anything is written: nothing under it is a setting a client may choose. The
// AgentProfile changes only by RunRequest.Agent, a Tool is switched only by
// SetToolActive, and the prompt cache key only by Fork.
const ReservedPrefix = "gotato."

// The Session metadata keys the Runner writes and reads, all under
// ReservedPrefix. A client reads them from a Record's metadata; none can set one.
const (
	metaAgent    = ReservedPrefix + "agent"     // AgentProfile name used for this Session
	metaTask     = ReservedPrefix + "task_spec" // the TaskSpec a Session was opened for, as the Run found it (profile.EncodeTaskSpec)
	metaModel    = ReservedPrefix + "model"     // informational: model name last used
	metaCacheKey = ReservedPrefix + "cache_key" // the prompt cache key; a fork keeps its parent's
	// metaToolPrefix + <tool id> = "inactive" hides one of the Agent's Tools
	// from the Model for this Session. Tool activation is Session state.
	metaToolPrefix = ReservedPrefix + "tool."
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

// Bounds is what guards this Instance itself: the Sessions it keeps in memory
// and what a busy Session does. They run before the Admitter (Config.Admitter),
// which is a policy of the service's own. Zero values disable a bound.
type Bounds struct {
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
// Bounds.MaxWarmSessions says nothing: generous for an Instance of a few
// gigabytes, and a limit all the same, so that a Runner left at its defaults
// does not hold every Session of the last hour.
const DefaultMaxWarmSessions = 10000

// Config configures a Runner.
//
// Several processes may share one Store, and so one FileStore directory:
// each is an Instance with its own name, and a Session is written by one
// Instance at a time (a Claim), whichever Instance served its last Run. To
// stop an Instance, call Drain, then shut down the HTTP server (for
// the package adapter/http: http.Server.Shutdown), then Close. Drain comes first because it
// lets the Runs in flight finish, which their open HTTP requests are waiting
// for, and ends the Session streams, which Shutdown would otherwise wait on
// for ever; Close comes last because it removes the Instance's Liveness row,
// after which the Runner writes nothing more.
type Config struct {
	// Store keeps the Sessions' Records. It may be shared by several
	// processes (see above).
	Store store.Store
	// Profiles holds the AgentProfiles and TaskSpecs the Runner serves
	// (profile.Registry). A Session looks its AgentProfile up by name each time
	// it is built, so one registered or replaced later reaches the Sessions
	// built after it, and never a Run in flight. It is required.
	Profiles *profile.Registry
	// DefaultAgent is the AgentProfile a new Session runs when its request
	// names none (RunRequest.Agent, CreateSessionRequest.Agent). Empty means
	// there is no default: such a request is refused as an unknown Agent.
	DefaultAgent string
	// Warm says how long the Instance's idle Sessions stay in memory after a
	// Run, and whether they are compacted while they idle. The zero value is
	// the default: 60 minutes, and an idle compaction.
	Warm WarmPolicy
	// TimeAwayAfter is how long a Session must have been idle before a
	// message that starts a Run is followed by a time_away notice with how
	// long it has been and the time now (G-F29). Zero means 30 minutes;
	// a negative value turns the notice off. The time is shown in the zone of
	// the AgentProfile's Compact.Location, UTC when it is nil.
	TimeAwayAfter time.Duration
	// Bounds guards the Instance's memory and says what a busy Session does
	// (Bounds).
	Bounds Bounds
	// Admitter decides whether a Run may start, once the drain and the memory
	// bound have let it through (Admitter). Nil means Limits{}: no limit. A
	// bound on the Runs in flight is Limits{MaxActiveRuns: n}.
	Admitter Admitter
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
	// Heartbeat sets how this Runner keeps its Liveness row, and how long a
	// Run waits for a Session that runs on another Instance.
	Heartbeat Heartbeat
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
	// clock is the time the Runner reads and waits by: warm TTLs, idle
	// compaction, renewals, waits. Nil means the system clock; the tests of this
	// package set it.
	clock clock.Clock
}

// Runner serves Runs against Sessions. It is built from parts that each do one
// job, with a lock of their own, and calls them in order (decision 048): the
// Liveness row, the table of admitted Runs, the warm copies, the Session locks,
// the Feeds, the Session streams and the releases that failed. The Runner holds
// the definitions and the store, one clock and the test hooks.
type Runner struct {
	// instance is this Runner's Liveness row: its name, the row and the guard
	// (heartbeat.go).
	instance *heartbeat.Row
	// runs holds the admitted Runs, their counts and the drain wait
	// (internal/runs). Lock order: runs, then warm.
	runs *runs.Table[*runHandle]
	// warm holds the idle Sessions, each a cache of a Record and never a lock
	// (warm.go).
	warm *warm.Cache
	// locks serializes the Runs of a Session in this process (locks.go).
	locks *sessionLocks
	// feeds are the Instance's stream and the Feeds of the Records in use
	// (events.go).
	feeds feedSet
	// streams are the Session streams open for clients (streams.go).
	streams streamSet
	// releases holds the Sessions whose release write failed (release.go).
	releases releaseSet

	// profiles are the definitions the Runner serves, and defaultAgent is the
	// AgentProfile a request that names none runs.
	profiles     *profile.Registry
	defaultAgent string
	store        store.Store

	// The settings of the Runner (Config): what the Liveness row and the wait
	// for another Instance's Session take, the bounds that guard the Instance,
	// the Admitter, and the bounds on the waits of ToolSets and of a drain
	// (negative is none).
	heartbeat      Heartbeat
	warmPolicy     WarmPolicy
	timeAway       time.Duration
	bounds         Bounds
	admitter       Admitter
	toolSetTimeout time.Duration
	drainTimeout   time.Duration

	// clock is the one clock of the Runner: every time it reads or waits for.
	clock clock.Clock
	// hooks are the seams the tests of this package set (export_test.go).
	hooks testHooks
}

// testHooks are the seams the tests of this package set, and nothing else
// does: each is nil, or zero, in a Runner that runs for real.
type testHooks struct {
	// beforeDetach runs when a Run has settled and its live Session still
	// takes messages.
	beforeDetach func()
	// onSendRetry runs each time Send goes round its loop again.
	onSendRetry func()
	// beforeRelease runs when a Run no longer takes messages and still holds
	// the Session.
	beforeRelease func()
	// drainStop is how long Drain waits for the Runs it stopped:
	// drainStopWait when zero.
	drainStop time.Duration
}

// New validates the configuration and creates a Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Store.Records == nil {
		return nil, errors.New("host: Store.Records is required")
	}
	if cfg.Store.Liveness == nil {
		return nil, errors.New("host: Store.Liveness is required")
	}
	if cfg.Profiles == nil {
		return nil, errors.New("host: Profiles is required")
	}
	r := &Runner{
		store:          cfg.Store,
		heartbeat:      cfg.Heartbeat.withDefaults(),
		streams:        streamSet{poll: cfg.StreamPoll},
		feeds:          feedSet{byKey: map[feedKey]*sessionFeed{}},
		toolSetTimeout: cfg.ToolSetTimeout,
		drainTimeout:   cfg.DrainTimeout,
		profiles:       cfg.Profiles,
		defaultAgent:   cfg.DefaultAgent,
		warmPolicy:     cfg.Warm,
		timeAway:       cfg.TimeAwayAfter,
		bounds:         cfg.Bounds,
		admitter:       cfg.Admitter,
		runs:           runs.New[*runHandle](),
		locks:          newSessionLocks(),
		clock:          cfg.clock,
	}
	if r.clock == nil {
		r.clock = clock.System()
	}
	r.instance = heartbeat.New(heartbeat.Config{
		Store:    cfg.Store.Liveness,
		Clock:    r.clock,
		Label:    strings.TrimSpace(cfg.Instance),
		Address:  strings.TrimSpace(cfg.Address),
		TTL:      r.heartbeat.TTL,
		OnLapse:  r.lapseNotice,
		OnRename: func() { r.dropAllWarm("liveness_lapsed") },
		OnBeat:   r.repairReleases,
	})
	if r.admitter == nil {
		r.admitter = Limits{}
	}
	// [CONTRACT] a negative MaxActiveRuns is refused, as MaxActiveSessions is, so a typo does not read as no limit.
	if v, ok := r.admitter.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return nil, err
		}
	}
	if r.bounds.MaxActiveSessions < 0 {
		return nil, errors.New("host: MaxActiveSessions cannot be negative")
	}
	if r.bounds.MaxWarmSessions == 0 {
		r.bounds.MaxWarmSessions = DefaultMaxWarmSessions
	}
	if r.bounds.Queue == "" {
		r.bounds.Queue = QueuePolicyReject
	}
	if r.bounds.Queue != QueuePolicyReject && r.bounds.Queue != QueuePolicyWait {
		return nil, fmt.Errorf("host: unknown queue policy %q", r.bounds.Queue)
	}
	if r.drainTimeout == 0 {
		r.drainTimeout = DefaultDrainTimeout
	}
	if r.streams.poll == 0 {
		r.streams.poll = defaultStreamPoll
	}
	r.warm = warm.New(r.clock)
	r.feeds.instance = newInstanceFeed()
	return r, nil
}

// Instance returns the name this Runner writes its Claims with: Config.Instance
// and a random suffix, or a generated name when none was set. It is new for
// each start, and new again when the Runner took a new name after its Liveness
// row expired (see Lapsed).
func (r *Runner) Instance() string { return r.id() }

// Record reads a Session's whole Record from the store, as a Document: its
// facts, Runs, usage and Events. It is a copy and changes nothing: a Set on what
// it returns would be stored nowhere, so it has none.
func (r *Runner) Record(ctx context.Context, sessionID string) (record.Document, error) {
	rec, err := store.View(ctx, r.store.Records, sessionID)
	if err != nil {
		return record.Document{}, storeFailureOf(ctx, err, true)
	}
	return rec.Snapshot(), nil
}

// Events reads a Session's stored Events from the store: every Entry of its
// Record that is an Event, in the order of their numbers, as typed Events.
func (r *Runner) Events(ctx context.Context, sessionID string) ([]gotato.Event, error) {
	events, err := store.StoredEvents(ctx, r.store.Records, sessionID, 0, 0)
	return events, storeFailureOf(ctx, err, true)
}

// Sessions lists the stored Sessions, the most recently updated first, a page
// at a time: the first for a zero ListQuery, and the next for the Cursor a page
// names as its Next. A page may repeat or skip a Session updated while a caller
// pages. It needs a store whose Records is a store.Lister; without one it is
// not_supported.
func (r *Runner) Sessions(ctx context.Context, q store.ListQuery) (store.ListPage, error) {
	lister, ok := r.store.Records.(store.Lister)
	if !ok {
		return store.ListPage{}, gotato.ErrorOf(gotato.CodeNotSupported, "host: the store does not list Sessions (its Records is not a store.Lister)")
	}
	page, err := lister.List(ctx, q)
	return page, storeFailureOf(ctx, err, true)
}

// AgentProfiles lists the registered AgentProfile names, sorted.
func (r *Runner) AgentProfiles() []string { return r.profiles.AgentProfiles() }

// AgentProfile returns one registered AgentProfile, as a Session built now would
// run it; an empty name means the default (Config.DefaultAgent). A name nothing
// is registered under is ErrUnknownAgent.
func (r *Runner) AgentProfile(ctx context.Context, name string) (profile.AgentProfile, error) {
	return r.agentNamed(ctx, name)
}

// ActiveRuns reports Runs in flight.
func (r *Runner) ActiveRuns() int { return r.runs.ActiveRuns() }

// ActiveSessions reports the Sessions this Instance has activated: those that
// a Run holds or waits for.
func (r *Runner) ActiveSessions() int { return r.runs.ActiveSessions() }

// Draining reports whether the Runner drains: from the moment Drain has marked
// its Liveness row. Readiness probes use it to stop receiving traffic before
// the process exits.
func (r *Runner) Draining() bool { return r.runs.Draining() }
