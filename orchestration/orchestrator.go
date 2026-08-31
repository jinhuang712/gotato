package orchestration

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

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

type Record struct {
	ID           gotato.ConversationID  `json:"conversation_id"`
	Key          gotato.ConversationKey `json:"conversation_key"`
	AgentName    gotato.AgentName       `json:"agent_name"`
	LiveAgentID  gotato.AgentID         `json:"live_agent_id,omitempty"`
	Generation   gotato.AgentGeneration `json:"agent_generation"`
	Status       ConversationStatus     `json:"status"`
	StateVersion uint64                 `json:"state_version"`
	Snapshot     *gotato.CoreSnapshot   `json:"snapshot,omitempty"`
}

func (r Record) Clone() Record {
	out := r
	if r.Snapshot != nil {
		snapshot := r.Snapshot.Clone()
		out.Snapshot = &snapshot
	}
	return out
}

type Definition struct {
	Name gotato.AgentName
	New  func(context.Context, Request, *gotato.CoreSnapshot) (gotato.Agent, error)
}

type SnapshotStore interface {
	Save(context.Context, Record) error
}

type MemorySnapshotStore struct {
	mu      sync.Mutex
	records map[gotato.ConversationID]Record
}

func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{records: make(map[gotato.ConversationID]Record)}
}

func (s *MemorySnapshotStore) Save(ctx context.Context, record Record) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.records[record.ID] = record.Clone()
	s.mu.Unlock()
	return nil
}

func (s *MemorySnapshotStore) Load(id gotato.ConversationID) (Record, bool) {
	s.mu.Lock()
	record, ok := s.records[id]
	s.mu.Unlock()
	return record.Clone(), ok
}

type Option func(*Orchestrator)

func WithSnapshotStore(store SnapshotStore) Option {
	return func(o *Orchestrator) {
		if store != nil {
			o.store = store
		}
	}
}

type Orchestrator struct {
	mu      sync.Mutex
	defs    map[gotato.AgentName]Definition
	byID    map[gotato.ConversationID]*entry
	byKey   map[string]*entry
	store   SnapshotStore
	seq     uint64
	serving atomic.Bool
}

type entry struct {
	record   Record
	agent    gotato.Agent
	inFlight uint32
	changed  chan struct{}
}

func New(options ...Option) *Orchestrator {
	o := &Orchestrator{
		defs:  make(map[gotato.AgentName]Definition),
		byID:  make(map[gotato.ConversationID]*entry),
		byKey: make(map[string]*entry),
		store: NewMemorySnapshotStore(),
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
	id := gotato.ConversationID(nextID("conversation", &o.seq))
	agent, err := def.New(ctx, request, nil)
	if err != nil {
		return nil, Record{}, err
	}
	record := Record{ID: id, Key: request.ConversationKey, AgentName: request.AgentName, LiveAgentID: agentID(agent), Generation: 0, Status: ConversationActive, StateVersion: 1}
	current = &entry{record: record, agent: agent, changed: make(chan struct{})}
	o.byID[id] = current
	if request.ConversationKey != "" {
		o.byKey[key] = current
	}
	return agent, record.Clone(), nil
}

func (o *Orchestrator) rehydrateLocked(ctx context.Context, request Request, current *entry) (gotato.Agent, Record, error) {
	if current.record.Snapshot == nil {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "Dormant Conversation has no snapshot")
	}
	def, ok := o.defs[current.record.AgentName]
	if !ok {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "Agent definition is unavailable")
	}
	if request.ExpectedGeneration != nil && current.record.Generation != *request.ExpectedGeneration {
		return nil, current.record.Clone(), gotatoError(gotato.ErrInvalidState, "stale Agent generation")
	}
	snapshot := current.record.Snapshot.Clone()
	request.AgentName = current.record.AgentName
	request.ConversationID = current.record.ID
	request.ConversationKey = current.record.Key
	agent, err := def.New(ctx, request, &snapshot)
	if err != nil {
		return nil, current.record.Clone(), err
	}
	current.agent = agent
	current.record.Generation++
	current.record.LiveAgentID = agentID(agent)
	current.record.Status = ConversationActive
	current.record.StateVersion++
	close(current.changed)
	current.changed = make(chan struct{})
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
	if !o.Serving() {
		return gotato.RunResult{}, Record{}, gotatoError(gotato.ErrInvalidState, "Orchestration is draining")
	}
	agent, record, err := o.Resolve(ctx, request)
	if err != nil {
		return gotato.RunResult{}, record, err
	}
	lease, err := o.acquire(record.ID, record.Generation, request.ExpectedGeneration)
	if err != nil {
		return gotato.RunResult{}, record, err
	}
	defer lease.release()
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
	if current := l.o.byID[l.id]; current != nil && current.inFlight > 0 {
		current.inFlight--
		close(current.changed)
		current.changed = make(chan struct{})
	}
	l.o.mu.Unlock()
}

func (o *Orchestrator) acquire(id gotato.ConversationID, generation gotato.AgentGeneration, expected *gotato.AgentGeneration) (*dispatchLease, error) {
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
	current.inFlight++
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

	if policy != Ephemeral {
		snapshotter, ok := agent.(gotato.Snapshotter)
		if !ok {
			err := gotatoError(gotato.ErrRetirementFailed, "Agent does not support snapshots")
			o.markRetirementFailed(id, generation, err)
			return err
		}
		snapshot, err := snapshotter.Snapshot(ctx)
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
		current.record.Snapshot = &snapshot
		current.record.StateVersion++
		record := current.record.Clone()
		o.mu.Unlock()
		if err := o.store.Save(ctx, record); err != nil {
			o.markRetirementFailed(id, generation, err)
			return gotatoError(gotato.ErrRetirementFailed, err.Error())
		}
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
	current.record.LiveAgentID = ""
	current.record.StateVersion++
	if policy == Ephemeral {
		current.record.Status = ConversationClosed
		current.record.Snapshot = nil
	} else {
		current.record.Status = ConversationDormant
	}
	close(current.changed)
	current.changed = make(chan struct{})
	finalRecord := current.record.Clone()
	o.mu.Unlock()
	if policy != Ephemeral {
		if err := o.store.Save(ctx, finalRecord); err != nil {
			o.mu.Lock()
			if current := o.byID[id]; current != nil && current.record.Generation == generation {
				current.record.Status = ConversationRetiring
				close(current.changed)
				current.changed = make(chan struct{})
			}
			o.mu.Unlock()
			return gotatoError(gotato.ErrRetirementFailed, err.Error())
		}
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
	current := o.byID[id]
	o.mu.Unlock()
	if current == nil {
		return Record{}, false
	}
	return current.record.Clone(), true
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
		if conversationClose {
			current.record.Status = ConversationClosed
			current.record.Snapshot = nil
		}
		o.mu.Unlock()
		return nil
	}
	if current.record.Status != ConversationActive {
		o.mu.Unlock()
		return gotatoError(gotato.ErrInvalidState, "Conversation is already retiring")
	}
	o.mu.Unlock()
	if err := o.Retire(ctx, id, Ephemeral); err != nil {
		return err
	}
	if !conversationClose {
		return nil
	}
	return nil
}

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
	return first
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
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
func gotatoError(code gotato.ErrorCode, message string) error {
	return &gotato.RuntimeError{Code: code, Operation: "Orchestration", Message: message}
}
