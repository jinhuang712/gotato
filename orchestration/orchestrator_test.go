package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

type testModel struct{}

func (testModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return &testModelStream{events: []gotato.ModelEvent{{Kind: gotato.ModelTextDelta, Text: "ok"}, {Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}}}, nil
}

type testModelStream struct {
	events []gotato.ModelEvent
	index  int
}

func (s *testModelStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if s.index >= len(s.events) {
		return gotato.ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}
func (s *testModelStream) Close() error { return nil }

func testOrchestrator(t *testing.T, options ...Option) *Orchestrator {
	t.Helper()
	o := New(options...)
	err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		agentOptions := []gotato.Option{gotato.WithModel(testModel{})}
		if snapshot != nil {
			agentOptions = append(agentOptions, gotato.WithInitialSnapshot(*snapshot))
		}
		return gotato.NewAgent(agentOptions...)
	}})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestConversationRetirementAndRehydration(t *testing.T) {
	o := testOrchestrator(t)
	request := Request{AgentName: "default", ConversationKey: "demo"}
	result, record, err := o.Dispatch(context.Background(), request, gotato.UserMessage("first"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != gotato.RunCompleted {
		t.Fatalf("status = %s", result.Status)
	}
	firstAgent := record.LiveAgentID
	if err := o.Retire(context.Background(), record.ID, Retain); err != nil {
		t.Fatal(err)
	}
	dormant, ok := o.Get(record.ID)
	if !ok || dormant.Status != ConversationDormant || dormant.LiveAgentID != "" {
		t.Fatalf("dormant record = %+v", dormant)
	}
	second, secondRecord, err := o.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || secondRecord.Generation != 1 || secondRecord.LiveAgentID == firstAgent {
		t.Fatalf("rehydration = %+v", secondRecord)
	}
	if _, _, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "demo", ExpectedGeneration: generationPtr(0)}, gotato.UserMessage("stale")); !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("expected stale generation, got %v", err)
	}
	if _, err := second.Prompt(context.Background(), gotato.UserMessage("second")); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentResolveDoesNotDuplicateRehydration(t *testing.T) {
	o := testOrchestrator(t)
	agent, record, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Retire(context.Background(), record.ID, Retain); err != nil {
		t.Fatal(err)
	}
	_ = agent

	const callers = 16
	ids := make(chan gotato.AgentID, callers)
	gens := make(chan gotato.AgentGeneration, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolved, current, resolveErr := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "same"})
			if resolveErr != nil {
				errs <- resolveErr
				return
			}
			ids <- resolved.(interface{ ID() gotato.AgentID }).ID()
			gens <- current.Generation
		}()
	}
	wg.Wait()
	close(ids)
	close(gens)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var firstID gotato.AgentID
	for id := range ids {
		if firstID == "" {
			firstID = id
		}
		if id != firstID {
			t.Fatalf("duplicate live Agent IDs: %s and %s", firstID, id)
		}
	}
	for generation := range gens {
		if generation != 1 {
			t.Fatalf("generation = %d", generation)
		}
	}
	if err := o.CloseAgent(context.Background(), firstID); err != nil {
		t.Fatal(err)
	}
}

type failingStore struct{}

func (failingStore) Save(context.Context, StoredState) error { return errors.New("store unavailable") }

func (failingStore) Load(context.Context, gotato.ConversationID) (StoredState, bool, error) {
	return StoredState{}, false, errors.New("store unavailable")
}

func (failingStore) Delete(context.Context, gotato.ConversationID) error {
	return errors.New("store unavailable")
}

func (failingStore) List(context.Context) ([]gotato.ConversationID, error) {
	return nil, errors.New("store unavailable")
}

func TestRetirementPersistenceFailureLeavesConversationActive(t *testing.T) {
	o := testOrchestrator(t, WithSnapshotStore(failingStore{}))
	_, record, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "failure"})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Retire(context.Background(), record.ID, Retain); !gotato.IsCode(err, gotato.ErrRetirementFailed) {
		t.Fatalf("expected retirement failure, got %v", err)
	}
	current, ok := o.Get(record.ID)
	if !ok || current.Status != ConversationActive || current.LiveAgentID == "" {
		t.Fatalf("route after failed retirement = %+v", current)
	}
	// The store is the authority for retained state. A discard it refuses is
	// reported rather than assumed, even though the Agent itself did close.
	if err := o.CloseAgent(context.Background(), current.LiveAgentID); !gotato.IsCode(err, gotato.ErrRetirementFailed) {
		t.Fatalf("close with a failing store = %v", err)
	}
	if closed, ok := o.Get(record.ID); !ok || closed.Status != ConversationClosed || closed.LiveAgentID != "" {
		t.Fatalf("route after discard = %+v", closed)
	}
}

func generationPtr(value gotato.AgentGeneration) *gotato.AgentGeneration { return &value }

func TestRecordCarriesNoConversationContent(t *testing.T) {
	o := testOrchestrator(t)
	_, record, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "opaque"}, gotato.UserMessage("secret words"))
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Retire(context.Background(), record.ID, Retain); err != nil {
		t.Fatal(err)
	}
	// A Record is a routing view. Serializing one must never carry transcript
	// content to a layer that only needs to route.
	dormant, ok := o.Get(record.ID)
	if !ok {
		t.Fatal("conversation disappeared")
	}
	encoded, err := json.Marshal(dormant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret words") {
		t.Fatalf("Record leaked conversation content: %s", encoded)
	}
}

func TestDiscardedConversationLeavesNoRetainedState(t *testing.T) {
	store := NewMemorySnapshotStore()
	o := testOrchestrator(t, WithSnapshotStore(store))
	_, record, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "discard"}, gotato.UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Retire(context.Background(), record.ID, Retain); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 1 {
		t.Fatalf("retained states after retain = %d", store.Len())
	}
	if err := o.CloseConversation(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Fatalf("closing the Conversation left retained state: %d", store.Len())
	}
	if _, _, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "discard"}); !gotato.IsCode(err, gotato.ErrAgentClosed) {
		t.Fatalf("resolve after close = %v", err)
	}
}

func TestRehydrationNeedsTheStore(t *testing.T) {
	store := NewMemorySnapshotStore()
	o := testOrchestrator(t, WithSnapshotStore(store))
	request := Request{AgentName: "default", ConversationKey: "authority"}
	_, record, err := o.Dispatch(context.Background(), request, gotato.UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Retire(context.Background(), record.ID, Retain); err != nil {
		t.Fatal(err)
	}
	// Orchestration keeps no second copy of the state: emptying the store
	// makes the Conversation unrecoverable rather than silently fresh.
	if err := store.Delete(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.Resolve(context.Background(), request); !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("rehydration without retained state = %v", err)
	}
}

func TestClosedConversationsAreReclaimed(t *testing.T) {
	o := testOrchestrator(t, WithClosedRecordLimit(2))
	var ids []gotato.ConversationID
	for i := 0; i < 5; i++ {
		key := gotato.ConversationKey(fmt.Sprintf("bounded-%d", i))
		_, record, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: key})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.ID)
		if err := o.CloseConversation(context.Background(), record.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.ClosedRecords(); got != 2 {
		t.Fatalf("closed tombstones = %d, want 2", got)
	}
	// The two most recent closures still answer as closed.
	for _, id := range ids[3:] {
		if _, ok := o.Get(id); !ok {
			t.Fatalf("recent closed Conversation %s was evicted", id)
		}
	}
	// The oldest were reclaimed from the routing table.
	for _, id := range ids[:3] {
		if _, ok := o.Get(id); ok {
			t.Fatalf("old closed Conversation %s was never reclaimed", id)
		}
	}
}

func TestAgentCapacityRejectsBeforeConstruction(t *testing.T) {
	var built int
	o := New(WithLimits(Limits{MaxAgents: 2}))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		built++
		return gotato.NewAgent(gotato.WithModel(testModel{}))
	}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: gotato.ConversationKey(fmt.Sprintf("cap-%d", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "cap-overflow"}); !gotato.IsCode(err, gotato.ErrLimitExceeded) {
		t.Fatalf("resolve past the Agent bound = %v", err)
	}
	// A rejected request creates neither an Agent nor a Conversation.
	if built != 2 {
		t.Fatalf("factory ran %d times, want 2", built)
	}
	if o.LiveAgents() != 2 {
		t.Fatalf("live Agents = %d", o.LiveAgents())
	}
	if _, ok := o.Get(""); ok {
		t.Fatal("rejected request left a Conversation behind")
	}

	// Retiring one gives the slot back.
	first, _ := o.Get(recordIDForKey(t, o, "cap-0"))
	if err := o.Retire(context.Background(), first.ID, Retain); err != nil {
		t.Fatal(err)
	}
	if o.LiveAgents() != 1 {
		t.Fatalf("live Agents after retirement = %d", o.LiveAgents())
	}
	if _, _, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "cap-overflow"}); err != nil {
		t.Fatalf("resolve after a slot freed = %v", err)
	}
}

func TestActiveRunCapacityIsReleasedExactlyOnce(t *testing.T) {
	release := make(chan struct{})
	o := New(WithLimits(Limits{MaxActiveRuns: 1}))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "run-a"}, gotato.UserMessage("first"))
		done <- err
	}()
	<-started
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })

	if _, _, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "run-b"}, gotato.UserMessage("second")); !gotato.IsCode(err, gotato.ErrLimitExceeded) {
		t.Fatalf("dispatch past the Run bound = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return o.ActiveRuns() == 0 })
	if _, _, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "run-b"}, gotato.UserMessage("second")); err != nil {
		t.Fatalf("dispatch after the Run settled = %v", err)
	}
}

type gatedTestModel struct{ release chan struct{} }

func (m gatedTestModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &testModelStream{events: []gotato.ModelEvent{{Kind: gotato.ModelTextDelta, Text: "ok"}, {Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}}}, nil
}

func recordIDForKey(t *testing.T, o *Orchestrator, key string) gotato.ConversationID {
	t.Helper()
	_, record, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: gotato.ConversationKey(key)})
	if err != nil {
		t.Fatal(err)
	}
	return record.ID
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(time.Millisecond)
	}
}

// manualClock fires scheduled callbacks on demand so idle retirement is
// asserted by advancing time rather than by sleeping.
type manualClock struct {
	mu      sync.Mutex
	pending []*manualTimer
}

type manualTimer struct {
	clock   *manualClock
	fn      func()
	stopped bool
}

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (c *manualClock) AfterFunc(_ time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, fn: fn}
	c.pending = append(c.pending, timer)
	return timer
}

// fire runs every armed callback, mimicking the TTL expiring.
func (c *manualClock) fire() {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, timer := range pending {
		c.mu.Lock()
		stopped := timer.stopped
		c.mu.Unlock()
		if !stopped {
			timer.fn()
		}
	}
}

func (c *manualClock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, timer := range c.pending {
		if !timer.stopped {
			count++
		}
	}
	return count
}

func TestAfterRunRetiresAtSettlement(t *testing.T) {
	o := testOrchestrator(t)
	request := Request{AgentName: "default", ConversationKey: "after-run", Retirement: AfterRun}
	_, record, err := o.Dispatch(context.Background(), request, gotato.UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		current, ok := o.Get(record.ID)
		return ok && current.Status == ConversationDormant
	})
	current, _ := o.Get(record.ID)
	if current.LiveAgentID != "" {
		t.Fatalf("AfterRun left a live Agent: %+v", current)
	}
	waitFor(t, func() bool { return o.LiveAgents() == 0 })

	// The Conversation survives its Agent: the next request rehydrates it.
	_, revived, err := o.Dispatch(context.Background(), request, gotato.UserMessage("again"))
	if err != nil {
		t.Fatal(err)
	}
	if revived.Generation != 1 {
		t.Fatalf("generation after AfterRun rehydration = %d", revived.Generation)
	}
}

func TestAfterIdleStartsCountingAtSettlement(t *testing.T) {
	clock := &manualClock{}
	o := testOrchestrator(t, WithLimits(Limits{IdleTTL: time.Minute}), WithClock(clock))
	request := Request{AgentName: "default", ConversationKey: "after-idle", Retirement: AfterIdle}
	_, record, err := o.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if clock.armed() != 1 {
		t.Fatalf("idle timer armed = %d after creation", clock.armed())
	}

	// A new admission cancels the countdown and settlement restarts it.
	if _, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if clock.armed() != 1 {
		t.Fatalf("idle timer armed = %d after settlement", clock.armed())
	}

	clock.fire()
	waitFor(t, func() bool {
		current, ok := o.Get(record.ID)
		return ok && current.Status == ConversationDormant
	})
	waitFor(t, func() bool { return o.LiveAgents() == 0 })
}

func TestAfterIdleDoesNotEvictABusyAgent(t *testing.T) {
	release := make(chan struct{})
	clock := &manualClock{}
	o := New(WithLimits(Limits{IdleTTL: time.Minute}), WithClock(clock))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}}); err != nil {
		t.Fatal(err)
	}
	request := Request{AgentName: "default", ConversationKey: "busy", Retirement: AfterIdle}
	done := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("hello"))
		done <- err
	}()
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })

	// Firing the TTL while the Agent is Busy must not close it.
	clock.fire()
	record, ok := o.Get(recordIDForKeyIn(t, o, "busy"))
	if !ok || record.Status != ConversationActive || record.LiveAgentID == "" {
		t.Fatalf("busy Conversation after an expired TTL = %+v", record)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAfterIdleRequiresAConfiguredTTL(t *testing.T) {
	o := testOrchestrator(t)
	_, _, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "no-ttl", Retirement: AfterIdle})
	if !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("AfterIdle without a TTL = %v", err)
	}
	if o.LiveAgents() != 0 {
		t.Fatalf("rejected policy still built an Agent: %d", o.LiveAgents())
	}
}

func recordIDForKeyIn(t *testing.T, o *Orchestrator, key string) gotato.ConversationID {
	t.Helper()
	_, record, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: gotato.ConversationKey(key)})
	if err != nil {
		t.Fatal(err)
	}
	return record.ID
}

func TestRejectWhileBusyIsTheDefault(t *testing.T) {
	release := make(chan struct{})
	o := New()
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}}); err != nil {
		t.Fatal(err)
	}
	request := Request{AgentName: "default", ConversationKey: "busy-reject"}
	done := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("first"))
		done <- err
	}()
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })

	if _, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("second")); !gotato.IsCode(err, gotato.ErrBusy) {
		t.Fatalf("second dispatch = %v", err)
	}
	if o.QueuedPrompts() != 0 {
		t.Fatalf("rejecting policy still queued %d", o.QueuedPrompts())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueueFIFOPreservesArrivalOrder(t *testing.T) {
	release := make(chan struct{})
	o := New(WithLimits(Limits{Queue: QueueFIFO, MaxQueuedPrompts: 8}))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}}); err != nil {
		t.Fatal(err)
	}
	request := Request{AgentName: "default", ConversationKey: "fifo"}
	first := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("first"))
		first <- err
	}()
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })

	const followers = 4
	order := make(chan int, followers)
	for i := 0; i < followers; i++ {
		index := i
		go func() {
			_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("queued"))
			if err != nil {
				t.Error(err)
			}
			order <- index
		}()
		// Each follower joins the queue before the next one is started, so
		// arrival order is what the assertion below can rely on.
		waitFor(t, func() bool { return o.QueuedPrompts() == index+1 })
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	for want := 0; want < followers; want++ {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("queued request %d ran before %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("queued request %d never ran", want)
		}
	}
	if o.QueuedPrompts() != 0 {
		t.Fatalf("queue not drained: %d", o.QueuedPrompts())
	}
}

func TestQueueIsBounded(t *testing.T) {
	release := make(chan struct{})
	o := New(WithLimits(Limits{Queue: QueueFIFO, MaxQueuedPrompts: 1}))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}}); err != nil {
		t.Fatal(err)
	}
	request := Request{AgentName: "default", ConversationKey: "bounded-queue"}
	running := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("first"))
		running <- err
	}()
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })

	queued := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("queued"))
		queued <- err
	}()
	waitFor(t, func() bool { return o.QueuedPrompts() == 1 })

	if _, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("overflow")); !gotato.IsCode(err, gotato.ErrLimitExceeded) {
		t.Fatalf("dispatch past the queue bound = %v", err)
	}
	close(release)
	if err := <-running; err != nil {
		t.Fatal(err)
	}
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
}

func TestAbandonedQueuedRequestReleasesItsPlace(t *testing.T) {
	release := make(chan struct{})
	o := New(WithLimits(Limits{Queue: QueueFIFO, MaxQueuedPrompts: 4}))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}}); err != nil {
		t.Fatal(err)
	}
	request := Request{AgentName: "default", ConversationKey: "abandon"}
	running := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("first"))
		running <- err
	}()
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })

	abandonCtx, abandon := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() {
		_, _, err := o.Dispatch(abandonCtx, request, gotato.UserMessage("abandoned"))
		abandoned <- err
	}()
	waitFor(t, func() bool { return o.QueuedPrompts() == 1 })
	abandon()
	if err := <-abandoned; !gotato.IsCode(err, gotato.ErrCancelled) {
		t.Fatalf("abandoned request = %v", err)
	}
	waitFor(t, func() bool { return o.QueuedPrompts() == 0 })

	close(release)
	if err := <-running; err != nil {
		t.Fatal(err)
	}
	// The slot the abandoned request gave up is still usable.
	if _, _, err := o.Dispatch(context.Background(), request, gotato.UserMessage("after")); err != nil {
		t.Fatalf("dispatch after an abandoned queue entry = %v", err)
	}
}
