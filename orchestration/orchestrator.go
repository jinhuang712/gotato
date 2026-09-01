package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

type ConversationStatus string

const (
	ConversationActive   ConversationStatus = "active"
	ConversationDormant  ConversationStatus = "dormant"
	ConversationRetiring ConversationStatus = "retiring"
	ConversationClosed   ConversationStatus = "closed"
	ConversationArchived ConversationStatus = "archived"
)

type RetirementPolicy string

const (
	Retain    RetirementPolicy = "retain"
	AfterRun  RetirementPolicy = "after_run"
	AfterIdle RetirementPolicy = "after_idle"
	Ephemeral RetirementPolicy = "ephemeral"
)

type Request struct {
	AgentName          gotato.AgentName
	ConversationID     gotato.ConversationID
	ConversationKey    gotato.ConversationKey
	ExpectedGeneration *gotato.AgentGeneration
	Metadata           map[string]string
	Retirement         RetirementPolicy
}

// Record is the routing view of a Conversation. It carries identity and
// status only: conversation content belongs to Core and, once the live Agent
// is gone, to the SnapshotStore. Nothing above Orchestration needs the
// transcript to route a request, so nothing above Orchestration receives it.
type Record struct {
	ID           gotato.ConversationID  `json:"conversation_id"`
	Key          gotato.ConversationKey `json:"conversation_key"`
	AgentName    gotato.AgentName       `json:"agent_name"`
	LiveAgentID  gotato.AgentID         `json:"live_agent_id,omitempty"`
	Generation   gotato.AgentGeneration `json:"agent_generation"`
	Status       ConversationStatus     `json:"status"`
	StateVersion uint64                 `json:"state_version"`
}

func (r Record) Clone() Record { return r }

type Definition struct {
	Name gotato.AgentName
	New  func(context.Context, Request, *gotato.CoreSnapshot) (gotato.Agent, error)
}

// StoredState is everything a dormant Conversation needs to come back: the
// routing record that names its Agent definition, plus the Core snapshot to
// rebuild from.
type StoredState struct {
	Record   Record              `json:"record"`
	Snapshot gotato.CoreSnapshot `json:"snapshot"`
}

func (s StoredState) Clone() StoredState {
	return StoredState{Record: s.Record, Snapshot: s.Snapshot.Clone()}
}

// SnapshotStore is the single authority for retained Conversation state.
// Orchestration keeps no second copy: what the store does not hold is gone.
type SnapshotStore interface {
	Save(context.Context, StoredState) error
	Load(context.Context, gotato.ConversationID) (StoredState, bool, error)
	Delete(context.Context, gotato.ConversationID) error
}

type MemorySnapshotStore struct {
	mu     sync.Mutex
	states map[gotato.ConversationID]StoredState
}

func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{states: make(map[gotato.ConversationID]StoredState)}
}

func (s *MemorySnapshotStore) Save(ctx context.Context, state StoredState) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.states[state.Record.ID] = state.Clone()
	s.mu.Unlock()
	return nil
}

func (s *MemorySnapshotStore) Load(ctx context.Context, id gotato.ConversationID) (StoredState, bool, error) {
	if err := contextError(ctx); err != nil {
		return StoredState{}, false, err
	}
	s.mu.Lock()
	state, ok := s.states[id]
	s.mu.Unlock()
	if !ok {
		return StoredState{}, false, nil
	}
	return state.Clone(), true, nil
}

func (s *MemorySnapshotStore) Delete(ctx context.Context, id gotato.ConversationID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.states, id)
	s.mu.Unlock()
	return nil
}

// Len reports how many Conversations the store retains. It exists so a caller
// can assert that discarded state is really gone.
func (s *MemorySnapshotStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.states)
}

type Option func(*Orchestrator)

func WithSnapshotStore(store SnapshotStore) Option {
	return func(o *Orchestrator) {
		if store != nil {
			o.store = store
		}
	}
}

// DefaultClosedRecords bounds how many closed Conversations stay addressable
// so a late request gets a closed error instead of silently opening a new
// Conversation under the same key.
const DefaultClosedRecords = 1024

// Limits bounds the coordination around Core. Core bounds one Agent's own
// work; these bound how many of them exist and how much they run at once.
// Zero leaves a dimension unbounded.
type Limits struct {
	// MaxAgents caps live Agent instances. A request that would exceed it is
	// rejected before any Agent is constructed.
	MaxAgents int
	// MaxActiveRuns caps Runs dispatched at the same time across all Agents.
	MaxActiveRuns int
	// IdleTTL is how long an AfterIdle Conversation may sit with no admitted
	// Run before its Agent is retired. There is no default: selecting
	// AfterIdle without setting it is a configuration error.
	IdleTTL time.Duration
	// Queue decides what happens to a request for a Conversation that is
	// already running one. The default rejects.
	Queue QueuePolicy
	// MaxQueuedPrompts caps requests waiting for their turn under QueueFIFO.
	MaxQueuedPrompts int
}

// QueuePolicy is what Orchestration does with a request whose Conversation is
// already running a Run. Core never queues: one Agent processes one Prompt at
// a time, and the surrounding policy decides the rest.
type QueuePolicy string

const (
	// RejectWhileBusy returns a typed busy error straight away.
	RejectWhileBusy QueuePolicy = "reject"
	// QueueFIFO waits for a turn in arrival order, bounded by
	// MaxQueuedPrompts. Waiting never bypasses an earlier request.
	QueueFIFO QueuePolicy = "fifo"
)

// Timer is a scheduled callback that can be cancelled.
type Timer interface{ Stop() bool }

// Clock schedules idle retirement. A test substitutes one so idle behaviour
// is asserted by advancing the clock rather than by sleeping.
type Clock interface {
	AfterFunc(time.Duration, func()) Timer
}

// WithClock replaces the scheduler used for idle retirement.
func WithClock(clock Clock) Option {
	return func(o *Orchestrator) {
		if clock != nil {
			o.clock = clock
		}
	}
}

type systemClock struct{}

func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// WithLimits sets the coordination bounds.
func WithLimits(limits Limits) Option {
	return func(o *Orchestrator) { o.limits = limits }
}

// WithClosedRecordLimit bounds the closed-Conversation tombstones the routing
// table keeps. Zero drops a closed Conversation from the table immediately.
func WithClosedRecordLimit(limit int) Option {
	return func(o *Orchestrator) {
		if limit >= 0 {
			o.closedLimit = limit
		}
	}
}

type Orchestrator struct {
	mu    sync.Mutex
	defs  map[gotato.AgentName]Definition
	byID  map[gotato.ConversationID]*entry
	byKey map[string]*entry
	// closed is a FIFO of tombstoned Conversation IDs. The routing table is
	// an index, and an index that only ever grows is a leak.
	closed      []gotato.ConversationID
	closedLimit int
	limits      Limits
	// live counts installed Agent handles; activeRuns counts dispatched Runs.
	// Both are reservations, so a rejected request creates neither an Agent
	// nor a Run.
	live       int
	activeRuns int
	// queue is a single arrival-ordered list. Keeping it global is what makes
	// FIFO mean FIFO: a later request never overtakes an earlier one, whatever
	// Conversation each names.
	queue   []*waiter
	clock   Clock
	store   SnapshotStore
	seq     uint64
	serving atomic.Bool
}

// reserveAgentLocked admits one more live Agent. The caller holds o.mu.
func (o *Orchestrator) reserveAgentLocked() error {
	if o.limits.MaxAgents > 0 && o.live >= o.limits.MaxAgents {
		return gotatoError(gotato.ErrLimitExceeded, "maximum live Agents reached")
	}
	o.live++
	return nil
}

// releaseAgentLocked gives back one live Agent slot. The caller holds o.mu.
func (o *Orchestrator) releaseAgentLocked() {
	if o.live > 0 {
		o.live--
	}
}

// LiveAgents reports how many Agent handles Orchestration currently holds.
func (o *Orchestrator) LiveAgents() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.live
}

// ActiveRuns reports how many Runs are dispatched right now.
func (o *Orchestrator) ActiveRuns() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.activeRuns
}

// waiter is one request holding its place in the dispatch queue.
type waiter struct {
	id      gotato.ConversationID
	ready   chan struct{}
	granted bool
	dropped bool
}

// grantLocked hands a reserved slot to the head of the queue when the named
// Conversation is idle and global capacity allows. The caller holds o.mu.
func (o *Orchestrator) grantLocked() {
	for len(o.queue) > 0 {
		head := o.queue[0]
		if o.limits.MaxActiveRuns > 0 && o.activeRuns >= o.limits.MaxActiveRuns {
			return
		}
		current := o.byID[head.id]
		if current == nil || current.agent == nil || current.record.Status != ConversationActive {
			// The Conversation went away while this request waited; wake it so
			// it can report the failure itself.
			o.queue = o.queue[1:]
			head.dropped = true
			close(head.ready)
			continue
		}
		if current.inFlight > 0 {
			return
		}
		o.queue = o.queue[1:]
		current.disarmIdle()
		current.inFlight++
		o.activeRuns++
		head.granted = true
		close(head.ready)
	}
}

// removeWaiterLocked drops an abandoned request from the queue. The caller
// holds o.mu.
func (o *Orchestrator) removeWaiterLocked(target *waiter) {
	for i, queued := range o.queue {
		if queued == target {
			o.queue = append(o.queue[:i], o.queue[i+1:]...)
			return
		}
	}
}

// QueuedPrompts reports how many requests are waiting for their turn.
func (o *Orchestrator) QueuedPrompts() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.queue)
}

// tombstone marks a closed Conversation as reclaimable and evicts the oldest
// tombstones beyond the configured bound.
func (o *Orchestrator) tombstone(id gotato.ConversationID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, existing := range o.closed {
		if existing == id {
			return
		}
	}
	o.closed = append(o.closed, id)
	for len(o.closed) > o.closedLimit {
		oldest := o.closed[0]
		o.closed = o.closed[1:]
		if evicted := o.byID[oldest]; evicted != nil {
			delete(o.byID, oldest)
			if evicted.record.Key != "" {
				key := routeKey(evicted.record.AgentName, evicted.record.Key)
				if o.byKey[key] == evicted {
					delete(o.byKey, key)
				}
			}
		}
	}
}

// ClosedRecords reports how many closed Conversations the routing table still
// answers for.
func (o *Orchestrator) ClosedRecords() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.closed)
}

type entry struct {
	record   Record
	agent    gotato.Agent
	inFlight uint32
	changed  chan struct{}
	// policy is the retirement policy chosen when this Conversation was
	// created. idleTimer is armed only while the Conversation is Active with
	// no admitted Run.
	policy    RetirementPolicy
	idleTimer Timer
}

// disarmIdle cancels a pending idle retirement. The caller holds o.mu.
func (e *entry) disarmIdle() {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
}

func New(options ...Option) *Orchestrator {
	o := &Orchestrator{
		defs:        make(map[gotato.AgentName]Definition),
		byID:        make(map[gotato.ConversationID]*entry),
		byKey:       make(map[string]*entry),
		closedLimit: DefaultClosedRecords,
		clock:       systemClock{},
		store:       NewMemorySnapshotStore(),
	}
	o.serving.Store(true)
	for _, option := range options {
		if option != nil {
			option(o)
		}
	}
	return o
}

func (o *Orchestrator) SetServing(serving bool) { o.serving.Store(serving) }
func (o *Orchestrator) Serving() bool           { return o.serving.Load() }

func (o *Orchestrator) Register(def Definition) error {
	if def.Name == "" || def.New == nil {
		return gotatoError(gotato.ErrInvalidArgument, "invalid Agent definition")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.defs[def.Name]; exists {
		return gotatoError(gotato.ErrInvalidArgument, "duplicate Agent definition")
	}
	o.defs[def.Name] = def
	return nil
}

func (o *Orchestrator) Resolve(ctx context.Context, request Request) (gotato.Agent, Record, error) {
	if err := contextError(ctx); err != nil {
		return nil, Record{}, err
	}
	if request.AgentName == "" {
		request.AgentName = "default"
	}
	key := routeKey(request.AgentName, request.ConversationKey)

	o.mu.Lock()
	defer o.mu.Unlock()
	if request.ConversationID != "" && request.ConversationKey != "" {
		byID := o.byID[request.ConversationID]
		byKey := o.byKey[key]
		if byID != nil && byKey != nil && byID != byKey {
			return nil, Record{}, gotatoError(gotato.ErrInvalidArgument, "ConversationID and ConversationKey conflict")
		}
	}

	var current *entry
	if request.ConversationID != "" {
		current = o.byID[request.ConversationID]
	}
	if current == nil && request.ConversationKey != "" {
		current = o.byKey[key]
	}
	if current != nil {
		if request.ExpectedGeneration != nil && current.record.Generation != *request.ExpectedGeneration {
			return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "stale Agent generation")
		}
		switch current.record.Status {
		case ConversationActive:
			if current.agent == nil {
				return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "active Conversation has no live Agent")
			}
			return current.agent, current.record.Clone(), nil
		case ConversationRetiring:
			return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "Conversation is retiring")
		case ConversationDormant:
			return o.rehydrateLocked(ctx, request, current)
		default:
			return nil, current.record.Clone(), gotatoError(gotato.ErrAgentClosed, "Conversation is closed")
		}
	}

	def, ok := o.defs[request.AgentName]
	if !ok {
		return nil, Record{}, gotatoError(gotato.ErrInvalidArgument, "unknown Agent definition: "+string(request.AgentName))
	}
	if request.ExpectedGeneration != nil {
		return nil, Record{}, gotatoError(gotato.ErrInvalidState, "expected generation has no matching Conversation")
	}
	policy := request.Retirement
	if policy == "" {
		policy = Retain
	}
	if err := o.validatePolicy(policy); err != nil {
		return nil, Record{}, err
	}
	// A rejected request creates neither an Agent nor a Run, so capacity is
	// reserved before the factory runs.
	if err := o.reserveAgentLocked(); err != nil {
		return nil, Record{}, err
	}
	id := gotato.ConversationID(nextID("conversation", &o.seq))
	agent, err := def.New(ctx, request, nil)
	if err != nil {
		o.releaseAgentLocked()
		return nil, Record{}, err
	}
	record := Record{ID: id, Key: request.ConversationKey, AgentName: request.AgentName, LiveAgentID: agentID(agent), Generation: 0, Status: ConversationActive, StateVersion: 1}
	current = &entry{record: record, agent: agent, changed: make(chan struct{}), policy: policy}
	o.byID[id] = current
	if request.ConversationKey != "" {
		o.byKey[key] = current
	}
	o.armIdleLocked(current)
	return agent, record.Clone(), nil
}

// validatePolicy rejects a retirement policy the configuration cannot honour.
// AfterIdle has no framework default TTL: it is a deployment decision.
func (o *Orchestrator) validatePolicy(policy RetirementPolicy) error {
	switch policy {
	case Retain, AfterRun, Ephemeral:
		return nil
	case AfterIdle:
		if o.limits.IdleTTL <= 0 {
			return gotatoError(gotato.ErrInvalidArgument, "AfterIdle requires a configured IdleTTL")
		}
		return nil
	default:
		return gotatoError(gotato.ErrInvalidArgument, "unknown retirement policy: "+string(policy))
	}
}

// armIdleLocked schedules idle retirement for an Active Conversation with no
// admitted Run. A Busy Agent is never scheduled for eviction. The caller holds
// o.mu.
func (o *Orchestrator) armIdleLocked(current *entry) {
	current.disarmIdle()
	if current.policy != AfterIdle || o.limits.IdleTTL <= 0 {
		return
	}
	if current.agent == nil || current.record.Status != ConversationActive || current.inFlight > 0 {
		return
	}
	id := current.record.ID
	generation := current.record.Generation
	current.idleTimer = o.clock.AfterFunc(o.limits.IdleTTL, func() { o.retireIdle(id, generation) })
}

// retireIdle honours an expired idle TTL. It re-checks the fence because the
// Conversation may have accepted a Run between the timer firing and this call.
func (o *Orchestrator) retireIdle(id gotato.ConversationID, generation gotato.AgentGeneration) {
	o.mu.Lock()
	current := o.byID[id]
	stale := current == nil || current.agent == nil ||
		current.record.Generation != generation ||
		current.record.Status != ConversationActive ||
		current.inFlight > 0
	o.mu.Unlock()
	if stale {
		return
	}
	_ = o.Retire(context.Background(), id, Retain)
}

func (o *Orchestrator) rehydrateLocked(ctx context.Context, request Request, current *entry) (gotato.Agent, Record, error) {
	def, ok := o.defs[current.record.AgentName]
	if !ok {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "Agent definition is unavailable")
	}
	if request.ExpectedGeneration != nil && current.record.Generation != *request.ExpectedGeneration {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "stale Agent generation")
	}
	// The store is the only authority for retained state. If it does not hold
	// this Conversation, the state is gone and rehydration must fail rather
	// than quietly start a fresh Agent under the same identity.
	state, found, err := o.store.Load(ctx, current.record.ID)
	if err != nil {
		return nil, current.record.Clone(), gotatoError(gotato.ErrRetirementFailed, err.Error())
	}
	if !found {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "Dormant Conversation has no retained state")
	}
	if state.Record.StateVersion != current.record.StateVersion {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "retained state version does not match the route")
	}
	if err := o.reserveAgentLocked(); err != nil {
		return nil, current.record.Clone(), err
	}
	snapshot := state.Snapshot
	request.AgentName = current.record.AgentName
	request.ConversationID = current.record.ID
	request.ConversationKey = current.record.Key
	agent, err := def.New(ctx, request, &snapshot)
	if err != nil {
		o.releaseAgentLocked()
		return nil, current.record.Clone(), err
	}
	current.agent = agent
	current.record.Generation++
	current.record.LiveAgentID = agentID(agent)
	current.record.Status = ConversationActive
	current.record.StateVersion++
	close(current.changed)
	current.changed = make(chan struct{})
	o.armIdleLocked(current)
	return agent, current.record.Clone(), nil
}

// CancelRun requests cancellation of an active Run while retaining its Agent
// and Conversation. Cancellation is best effort when a provider ignores the
// Context passed to Model.Stream.
func (o *Orchestrator) CancelRun(ctx context.Context, runID gotato.RunID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if runID == "" {
		return gotatoError(gotato.ErrInvalidArgument, "Run ID is required")
	}

	o.mu.Lock()
	agents := make([]gotato.Agent, 0, len(o.byID))
	for _, current := range o.byID {
		if current.agent != nil {
			agents = append(agents, current.agent)
		}
	}
	o.mu.Unlock()

	supported := false
	for _, agent := range agents {
		canceler, ok := agent.(gotato.RunCanceler)
		if !ok {
			continue
		}
		supported = true
		if err := canceler.CancelRun(ctx, runID); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		}
	}
	if !supported {
		return gotatoError(gotato.ErrNotSupported, "live Agents do not support Run cancellation")
	}
	return gotatoError(gotato.ErrInvalidState, "Run is not active")
}

func (o *Orchestrator) Dispatch(ctx context.Context, request Request, message gotato.Message) (gotato.RunResult, Record, error) {
	return o.dispatch(ctx, request, message, nil)
}

// DispatchWithAdmission is the same dispatch path as Dispatch, but signals
// after the conversation has acquired its single-flight lease and immediately
// before Core Prompt starts. Hosts that subscribe to Events before dispatch can
// use this boundary to correlate the next agent_start with this Run.
func (o *Orchestrator) DispatchWithAdmission(ctx context.Context, request Request, message gotato.Message, admitted chan<- struct{}) (gotato.RunResult, Record, error) {
	return o.dispatch(ctx, request, message, admitted)
}

func (o *Orchestrator) dispatch(ctx context.Context, request Request, message gotato.Message, admitted chan<- struct{}) (gotato.RunResult, Record, error) {
	if !o.Serving() {
		return gotato.RunResult{}, Record{}, gotatoError(gotato.ErrInvalidState, "Orchestration is draining")
	}
	agent, record, err := o.Resolve(ctx, request)
	if err != nil {
		return gotato.RunResult{}, record, err
	}
	lease, err := o.acquire(ctx, record.ID, record.Generation, request.ExpectedGeneration)
	if err != nil {
		return gotato.RunResult{}, record, err
	}
	defer lease.release()
	if admitted != nil {
		admitted <- struct{}{}
	}
	result, runErr := agent.Prompt(ctx, message)
	return result, o.record(record.ID), runErr
}

type dispatchLease struct {
	o        *Orchestrator
	id       gotato.ConversationID
	released atomic.Bool
}

func (l *dispatchLease) release() {
	if l.released.Swap(true) {
		return
	}
	l.o.mu.Lock()
	if l.o.activeRuns > 0 {
		l.o.activeRuns--
	}
	var retireAfterRun bool
	var generation gotato.AgentGeneration
	if current := l.o.byID[l.id]; current != nil {
		if current.inFlight > 0 {
			current.inFlight--
			close(current.changed)
			current.changed = make(chan struct{})
		}
		if current.inFlight == 0 && current.record.Status == ConversationActive {
			generation = current.record.Generation
			switch current.policy {
			case AfterRun:
				retireAfterRun = true
			case AfterIdle:
				l.o.armIdleLocked(current)
			}
		}
	}
	l.o.grantLocked()
	l.o.mu.Unlock()
	if retireAfterRun {
		// Retire takes the same lock, so it runs outside this critical
		// section. The caller already has its RunResult.
		go l.o.retireSettled(l.id, generation)
	}
}

// retireSettled honours AfterRun once the Run that triggered it has settled.
func (o *Orchestrator) retireSettled(id gotato.ConversationID, generation gotato.AgentGeneration) {
	o.mu.Lock()
	current := o.byID[id]
	stale := current == nil || current.agent == nil ||
		current.record.Generation != generation ||
		current.record.Status != ConversationActive ||
		current.inFlight > 0
	o.mu.Unlock()
	if stale {
		return
	}
	_ = o.Retire(context.Background(), id, Retain)
}

func (o *Orchestrator) acquire(ctx context.Context, id gotato.ConversationID, generation gotato.AgentGeneration, expected *gotato.AgentGeneration) (*dispatchLease, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	current := o.byID[id]
	if current == nil || current.agent == nil {
		return nil, gotatoError(gotato.ErrAgentClosed, "live Agent is unavailable")
	}
	if current.record.Status != ConversationActive {
		return nil, gotatoError(gotato.ErrInvalidState, "Conversation is not active")
	}
	if current.record.Generation != generation {
		return nil, gotatoError(gotato.ErrInvalidState, "stale Agent generation")
	}
	if expected != nil && *expected != generation {
		return nil, gotatoError(gotato.ErrInvalidState, "stale Agent generation")
	}
	if o.limits.MaxActiveRuns > 0 && o.activeRuns >= o.limits.MaxActiveRuns {
		return nil, gotatoError(gotato.ErrLimitExceeded, "maximum active Runs reached")
	}
	if current.inFlight > 0 || len(o.queue) > 0 {
		// Joining behind an existing queue keeps arrival order honest: a
		// request that finds the Agent idle still waits if others got here
		// first.
		if o.limits.Queue != QueueFIFO {
			return nil, gotatoError(gotato.ErrBusy, "Conversation is already running a Run")
		}
		if o.limits.MaxQueuedPrompts > 0 && len(o.queue) >= o.limits.MaxQueuedPrompts {
			return nil, gotatoError(gotato.ErrLimitExceeded, "prompt queue is full")
		}
		return o.waitForTurnLocked(ctx, id, generation, expected)
	}
	if o.limits.MaxActiveRuns > 0 && o.activeRuns >= o.limits.MaxActiveRuns {
		if o.limits.Queue != QueueFIFO {
			return nil, gotatoError(gotato.ErrLimitExceeded, "maximum active Runs reached")
		}
		if o.limits.MaxQueuedPrompts > 0 && len(o.queue) >= o.limits.MaxQueuedPrompts {
			return nil, gotatoError(gotato.ErrLimitExceeded, "prompt queue is full")
		}
		return o.waitForTurnLocked(ctx, id, generation, expected)
	}
	// A new admission cancels the idle countdown; it restarts from settlement.
	current.disarmIdle()
	current.inFlight++
	o.activeRuns++
	return &dispatchLease{o: o, id: id}, nil
}

// waitForTurnLocked parks a request in arrival order until a slot is handed to
// it. The caller holds o.mu and this releases it while waiting.
func (o *Orchestrator) waitForTurnLocked(ctx context.Context, id gotato.ConversationID, generation gotato.AgentGeneration, expected *gotato.AgentGeneration) (*dispatchLease, error) {
	pending := &waiter{id: id, ready: make(chan struct{})}
	o.queue = append(o.queue, pending)
	o.mu.Unlock()

	select {
	case <-pending.ready:
	case <-ctx.Done():
		o.mu.Lock()
		if pending.granted {
			// The slot arrived while this request was giving up; hand it
			// straight to whoever is next rather than losing it.
			o.mu.Unlock()
			(&dispatchLease{o: o, id: id}).release()
			o.mu.Lock()
		} else {
			o.removeWaiterLocked(pending)
		}
		return nil, queueAbandoned(ctx.Err())
	}

	o.mu.Lock()
	if pending.dropped {
		return nil, gotatoError(gotato.ErrAgentClosed, "Conversation went away while the request waited")
	}
	current := o.byID[id]
	if current == nil || current.agent == nil || current.record.Generation != generation ||
		(expected != nil && *expected != current.record.Generation) {
		o.mu.Unlock()
		(&dispatchLease{o: o, id: id}).release()
		o.mu.Lock()
		return nil, gotatoError(gotato.ErrInvalidState, "stale Agent generation")
	}
	return &dispatchLease{o: o, id: id}, nil
}

func (o *Orchestrator) Retire(ctx context.Context, id gotato.ConversationID, policy RetirementPolicy) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if policy == "" {
		policy = Retain
	}
	o.mu.Lock()
	current := o.byID[id]
	if current == nil {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidArgument, "unknown Conversation")
	}
	if current.record.Status == ConversationDormant {
		o.mu.Unlock()
		return nil
	}
	if current.record.Status == ConversationClosed {
		o.mu.Unlock()
		return nil
	}
	if current.record.Status != ConversationActive {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "Conversation is already retiring")
	}
	current.record.Status = ConversationRetiring
	close(current.changed)
	current.changed = make(chan struct{})
	agent := current.agent
	generation := current.record.Generation
	o.mu.Unlock()

	if err := o.waitEntryIdle(ctx, id); err != nil {
		o.markRetirementFailed(id, generation, err)
		return err
	}
	if waiter, ok := agent.(gotato.IdleWaiter); ok {
		if err := waiter.WaitForIdle(ctx); err != nil {
			o.markRetirementFailed(id, generation, err)
			return err
		}
	}

	var snapshot *gotato.CoreSnapshot
	if policy != Ephemeral {
		snapshotter, ok := agent.(gotato.Snapshotter)
		if !ok {
			err := gotatoError(gotato.ErrRetirementFailed, "Agent does not support snapshots")
			o.markRetirementFailed(id, generation, err)
			return err
		}
		captured, err := snapshotter.Snapshot(ctx)
		if err != nil {
			o.markRetirementFailed(id, generation, err)
			return err
		}
		o.mu.Lock()
		current = o.byID[id]
		if current == nil || current.record.Generation != generation || current.record.Status != ConversationRetiring {
			o.mu.Unlock()
			return gotatoError(gotato.ErrInvalidState, "retirement fence changed")
		}
		current.record.StateVersion++
		record := current.record.Clone()
		o.mu.Unlock()
		// The state reaches the store before the live route is removed. If
		// this write fails the Conversation is not reported as retired.
		if err := o.store.Save(ctx, StoredState{Record: record, Snapshot: captured}); err != nil {
			o.markRetirementFailed(id, generation, err)
			return gotatoError(gotato.ErrRetirementFailed, err.Error())
		}
		snapshot = &captured
	}

	if err := agent.Close(ctx); err != nil {
		o.markRetirementFailed(id, generation, err)
		return gotatoError(gotato.ErrRetirementFailed, err.Error())
	}
	o.mu.Lock()
	current = o.byID[id]
	if current == nil || current.record.Generation != generation || current.record.Status != ConversationRetiring {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "retirement fence changed")
	}
	current.agent = nil
	current.disarmIdle()
	o.releaseAgentLocked()
	current.record.LiveAgentID = ""
	current.record.StateVersion++
	if policy == Ephemeral {
		current.record.Status = ConversationClosed
	} else {
		current.record.Status = ConversationDormant
	}
	close(current.changed)
	current.changed = make(chan struct{})
	finalRecord := current.record.Clone()
	o.mu.Unlock()
	if policy == Ephemeral {
		// Discard retirement keeps nothing. A later request under the same
		// key must not silently reuse this state. The route is reclaimed even
		// when the store refuses: the index bound holds regardless of store
		// health, and the failure is still reported.
		o.tombstone(id)
		if err := o.store.Delete(ctx, id); err != nil {
			return gotatoError(gotato.ErrRetirementFailed, err.Error())
		}
		return nil
	}
	if err := o.store.Save(ctx, StoredState{Record: finalRecord, Snapshot: *snapshot}); err != nil {
		o.mu.Lock()
		if current := o.byID[id]; current != nil && current.record.Generation == generation {
			current.record.Status = ConversationRetiring
			close(current.changed)
			current.changed = make(chan struct{})
		}
		o.mu.Unlock()
		return gotatoError(gotato.ErrRetirementFailed, err.Error())
	}
	return nil
}

func (o *Orchestrator) waitEntryIdle(ctx context.Context, id gotato.ConversationID) error {
	for {
		o.mu.Lock()
		current := o.byID[id]
		if current == nil {
			o.mu.Unlock()
			return gotatoError(gotato.ErrInvalidArgument, "unknown Conversation")
		}
		if current.inFlight == 0 {
			o.mu.Unlock()
			return nil
		}
		changed := current.changed
		o.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (o *Orchestrator) markRetirementFailed(id gotato.ConversationID, generation gotato.AgentGeneration, err error) {
	o.mu.Lock()
	if current := o.byID[id]; current != nil && current.record.Generation == generation && current.record.Status == ConversationRetiring {
		keepRetiring := false
		if current.agent != nil {
			if lifecycle, ok := current.agent.(gotato.AgentLifecycle); ok {
				keepRetiring = lifecycle.Status() == gotato.AgentClosing || lifecycle.Status() == gotato.AgentClosed
			}
		}
		if !keepRetiring {
			current.record.Status = ConversationActive
		}
		close(current.changed)
		current.changed = make(chan struct{})
	}
	o.mu.Unlock()
	_ = err
}

func (o *Orchestrator) Get(id gotato.ConversationID) (Record, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	current := o.byID[id]
	if current == nil {
		return Record{}, false
	}
	// The record is copied under the lock: a retirement running on another
	// goroutine may be rewriting it.
	return current.record.Clone(), true
}

// LiveAgent returns the live Agent handle installed for a Conversation, when
// there is one, together with the current record. The caller observes that
// handle; Orchestration keeps ownership of its lifecycle.
func (o *Orchestrator) LiveAgent(id gotato.ConversationID) (gotato.Agent, Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	current := o.byID[id]
	if current == nil {
		return nil, Record{}
	}
	return current.agent, current.record.Clone()
}

func (o *Orchestrator) record(id gotato.ConversationID) Record {
	record, _ := o.Get(id)
	return record
}

func (o *Orchestrator) CloseAgent(ctx context.Context, id gotato.AgentID) error {
	o.mu.Lock()
	var current *entry
	for _, candidate := range o.byID {
		if candidate.record.LiveAgentID == id {
			current = candidate
			break
		}
	}
	if current == nil {
		o.mu.Unlock()
		return gotatoError(gotato.ErrAgentClosed, "unknown live Agent")
	}
	conversationID := current.record.ID
	o.mu.Unlock()
	return o.closeConversation(ctx, conversationID, false)
}

func (o *Orchestrator) CloseConversation(ctx context.Context, id gotato.ConversationID) error {
	return o.closeConversation(ctx, id, true)
}

func (o *Orchestrator) closeConversation(ctx context.Context, id gotato.ConversationID, conversationClose bool) error {
	o.mu.Lock()
	current := o.byID[id]
	if current == nil {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidArgument, "unknown Conversation")
	}
	if current.record.Status == ConversationDormant || current.record.Status == ConversationClosed {
		if !conversationClose {
			o.mu.Unlock()
			return nil
		}
		current.record.Status = ConversationClosed
		o.mu.Unlock()
		// Closing the business Conversation deletes its retained state.
		o.tombstone(id)
		if err := o.store.Delete(ctx, id); err != nil {
			return gotatoError(gotato.ErrRetirementFailed, err.Error())
		}
		return nil
	}
	if current.record.Status != ConversationActive {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "Conversation is already retiring")
	}
	o.mu.Unlock()
	return o.Retire(ctx, id, Ephemeral)
}

// PendingAgent describes one Conversation that a drain could not settle.
type PendingAgent struct {
	ConversationID gotato.ConversationID `json:"conversation_id"`
	AgentID        gotato.AgentID        `json:"agent_id,omitempty"`
	Conversation   ConversationStatus    `json:"conversation_status"`
	Agent          gotato.AgentStatus    `json:"agent_status,omitempty"`
}

// DrainIncomplete reports a drain that ended with Agents still Busy or
// Closing. Go cannot forcibly terminate work that ignores its Context, so
// those Agents are reported as they are instead of as closed.
type DrainIncomplete struct {
	Pending []PendingAgent `json:"pending"`
	Cause   error          `json:"-"`
}

func (e *DrainIncomplete) Error() string {
	names := make([]string, 0, len(e.Pending))
	for _, pending := range e.Pending {
		state := string(pending.Conversation)
		if pending.Agent != "" {
			state += "/" + string(pending.Agent)
		}
		names = append(names, string(pending.ConversationID)+"="+state)
	}
	return "incomplete drain: " + strings.Join(names, " ")
}

func (e *DrainIncomplete) Unwrap() error { return e.Cause }

// Drain stops admission and retires every live Conversation with retention.
// It reports the Conversations it could not settle instead of claiming that
// every Agent closed.
func (o *Orchestrator) Drain(ctx context.Context) error {
	o.SetServing(false)
	o.mu.Lock()
	ids := make([]gotato.ConversationID, 0, len(o.byID))
	for id, current := range o.byID {
		if current.record.Status == ConversationActive {
			ids = append(ids, id)
		}
	}
	o.mu.Unlock()
	var first error
	for _, id := range ids {
		if err := o.Retire(ctx, id, Retain); err != nil && first == nil {
			first = err
		}
	}
	if pending := o.pendingAgents(); len(pending) > 0 {
		return &DrainIncomplete{Pending: pending, Cause: first}
	}
	return first
}

// pendingAgents lists Conversations that still hold a live Agent or remain in
// the Retiring transition.
func (o *Orchestrator) pendingAgents() []PendingAgent {
	o.mu.Lock()
	defer o.mu.Unlock()
	pending := make([]PendingAgent, 0)
	for id, current := range o.byID {
		if current.record.Status != ConversationActive && current.record.Status != ConversationRetiring {
			continue
		}
		if current.agent == nil && current.record.Status == ConversationActive {
			continue
		}
		entry := PendingAgent{ConversationID: id, AgentID: current.record.LiveAgentID, Conversation: current.record.Status}
		if current.agent != nil {
			if lifecycle, ok := current.agent.(gotato.AgentLifecycle); ok {
				entry.Agent = lifecycle.Status()
			}
		}
		pending = append(pending, entry)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ConversationID < pending[j].ConversationID })
	return pending
}

func routeKey(name gotato.AgentName, key gotato.ConversationKey) string {
	return string(name) + "\x00" + string(key)
}
func agentID(agent gotato.Agent) gotato.AgentID {
	if identified, ok := agent.(interface{ ID() gotato.AgentID }); ok {
		return identified.ID()
	}
	return ""
}
func nextID(prefix string, seq *uint64) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddUint64(seq, 1))
}

// queueAbandoned types the outcome of a request that gave up its place in the
// queue, so a caller can tell it apart from a failure inside the Run.
func queueAbandoned(err error) error {
	code := gotato.ErrCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		code = gotato.ErrDeadlineExceeded
	}
	return &gotato.RuntimeError{
		Code:      code,
		Operation: "Orchestration",
		Message:   "request left the prompt queue before its turn",
		Cause:     err,
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
func gotatoError(code gotato.ErrorCode, message string) error {
	return &gotato.RuntimeError{Code: code, Operation: "Orchestration", Message: message}
}
