package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	gotato "github.com/jinhuang712/gotato"
)

type ConversationStatus string

const (
	ConversationActive  ConversationStatus = "active"
	ConversationClosing ConversationStatus = "closing"
	ConversationClosed  ConversationStatus = "closed"
)

type Request struct {
	AgentName          gotato.AgentName
	ConversationID     gotato.ConversationID
	ConversationKey    gotato.ConversationKey
	ExpectedGeneration *gotato.AgentGeneration
	Metadata           map[string]string
}

// Record is the routing view of a Conversation. It carries identity and
// status only: conversation content belongs to the live Agent. Nothing above
// Orchestration needs the transcript to route a request, so nothing above
// Orchestration receives it.
type Record struct {
	ID          gotato.ConversationID  `json:"conversation_id"`
	Key         gotato.ConversationKey `json:"conversation_key"`
	AgentName   gotato.AgentName       `json:"agent_name"`
	LiveAgentID gotato.AgentID         `json:"live_agent_id,omitempty"`
	Generation  gotato.AgentGeneration `json:"agent_generation"`
	Status      ConversationStatus     `json:"status"`
	// Origin is set when this Conversation was spawned by another Run. It is
	// provenance, not ownership.
	Origin *Provenance `json:"origin,omitempty"`
}

func (r Record) Clone() Record {
	out := r
	if r.Origin != nil {
		origin := *r.Origin
		out.Origin = &origin
	}
	return out
}

type Definition struct {
	Name gotato.AgentName
	New  func(context.Context, Request) (gotato.Agent, error)
}

type Option func(*Orchestrator)

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
}

func New(options ...Option) *Orchestrator {
	o := &Orchestrator{
		defs:        make(map[gotato.AgentName]Definition),
		byID:        make(map[gotato.ConversationID]*entry),
		byKey:       make(map[string]*entry),
		closedLimit: DefaultClosedRecords,
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
	return o.resolveWithProvenance(ctx, request, nil)
}

func (o *Orchestrator) resolveWithProvenance(ctx context.Context, request Request, origin *Provenance) (gotato.Agent, Record, error) {
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
		case ConversationClosing:
			return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "Conversation is closing")
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
	// A rejected request creates neither an Agent nor a Run, so capacity is
	// reserved before the factory runs.
	if err := o.reserveAgentLocked(); err != nil {
		return nil, Record{}, err
	}
	id := gotato.ConversationID(nextID("conversation", &o.seq))
	agent, err := def.New(ctx, request)
	if err != nil {
		o.releaseAgentLocked()
		return nil, Record{}, err
	}
	record := Record{ID: id, Key: request.ConversationKey, AgentName: request.AgentName, LiveAgentID: agentID(agent), Generation: 0, Status: ConversationActive, Origin: origin}
	current = &entry{record: record, agent: agent, changed: make(chan struct{})}
	o.byID[id] = current
	if request.ConversationKey != "" {
		o.byKey[key] = current
	}
	return agent, record.Clone(), nil
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
	if current := l.o.byID[l.id]; current != nil && current.inFlight > 0 {
		current.inFlight--
		close(current.changed)
		current.changed = make(chan struct{})
	}
	l.o.grantLocked()
	l.o.mu.Unlock()
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

func (o *Orchestrator) closeLiveConversation(ctx context.Context, id gotato.ConversationID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	current := o.byID[id]
	if current == nil {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidArgument, "unknown Conversation")
	}
	if current.record.Status == ConversationClosed {
		o.mu.Unlock()
		return nil
	}
	if current.record.Status != ConversationActive {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "Conversation is already closing")
	}
	current.record.Status = ConversationClosing
	close(current.changed)
	current.changed = make(chan struct{})
	agent := current.agent
	generation := current.record.Generation
	o.mu.Unlock()

	if err := o.waitEntryIdle(ctx, id); err != nil {
		o.markCloseFailed(id, generation, err)
		return err
	}
	if waiter, ok := agent.(gotato.IdleWaiter); ok {
		if err := waiter.WaitForIdle(ctx); err != nil {
			o.markCloseFailed(id, generation, err)
			return err
		}
	}
	if err := agent.Close(ctx); err != nil {
		o.markCloseFailed(id, generation, err)
		return gotatoError(gotato.ErrInternalInvariant, "Agent close failed: "+err.Error())
	}

	o.mu.Lock()
	current = o.byID[id]
	if current == nil || current.record.Generation != generation || current.record.Status != ConversationClosing {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "close fence changed")
	}
	current.agent = nil
	o.releaseAgentLocked()
	current.record.LiveAgentID = ""
	current.record.Status = ConversationClosed
	close(current.changed)
	current.changed = make(chan struct{})
	o.mu.Unlock()
	o.tombstone(id)
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

func (o *Orchestrator) markCloseFailed(id gotato.ConversationID, generation gotato.AgentGeneration, err error) {
	o.mu.Lock()
	if current := o.byID[id]; current != nil && current.record.Generation == generation && current.record.Status == ConversationClosing {
		keepClosing := false
		if current.agent != nil {
			if lifecycle, ok := current.agent.(gotato.AgentLifecycle); ok {
				keepClosing = lifecycle.Status() == gotato.AgentClosing || lifecycle.Status() == gotato.AgentClosed
			}
		}
		if !keepClosing {
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
	// The record is copied under the lock: a close running on another
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
	return o.closeConversation(ctx, conversationID)
}

func (o *Orchestrator) CloseConversation(ctx context.Context, id gotato.ConversationID) error {
	return o.closeConversation(ctx, id)
}

func (o *Orchestrator) closeConversation(ctx context.Context, id gotato.ConversationID) error {
	o.mu.Lock()
	current := o.byID[id]
	if current == nil {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidArgument, "unknown Conversation")
	}
	if current.record.Status == ConversationClosed {
		o.mu.Unlock()
		return nil
	}
	if current.record.Status != ConversationActive {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "Conversation is already closing")
	}
	o.mu.Unlock()
	return o.closeLiveConversation(ctx, id)
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

// Drain stops admission and closes every live Conversation. It reports the
// Conversations it could not settle instead of claiming that every Agent closed.
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
		if err := o.closeLiveConversation(ctx, id); err != nil && first == nil {
			first = err
		}
	}
	if pending := o.pendingAgents(); len(pending) > 0 {
		return &DrainIncomplete{Pending: pending, Cause: first}
	}
	return first
}

// pendingAgents lists Conversations that still hold a live Agent or remain in
// the Closing transition.
func (o *Orchestrator) pendingAgents() []PendingAgent {
	o.mu.Lock()
	defer o.mu.Unlock()
	pending := make([]PendingAgent, 0)
	for id, current := range o.byID {
		if current.record.Status != ConversationActive && current.record.Status != ConversationClosing {
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
