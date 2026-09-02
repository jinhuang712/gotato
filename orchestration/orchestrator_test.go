package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
	err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(testModel{}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRecordCarriesNoConversationContent(t *testing.T) {
	o := testOrchestrator(t)
	_, record, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "opaque"}, gotato.UserMessage("secret words"))
	if err != nil {
		t.Fatal(err)
	}
	// A Record is a routing view. Serializing one must never carry transcript
	// content to a layer that only needs to route.
	current, ok := o.Get(record.ID)
	if !ok {
		t.Fatal("conversation disappeared")
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret words") {
		t.Fatalf("Record leaked conversation content: %s", encoded)
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
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
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

	// Closing one gives the slot back.
	first, _ := o.Get(recordIDForKey(t, o, "cap-0"))
	if err := o.CloseConversation(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if o.LiveAgents() != 1 {
		t.Fatalf("live Agents after close = %d", o.LiveAgents())
	}
	if _, _, err := o.Resolve(context.Background(), Request{AgentName: "default", ConversationKey: "cap-overflow"}); err != nil {
		t.Fatalf("resolve after a slot freed = %v", err)
	}
}

func TestActiveRunCapacityIsReleasedExactlyOnce(t *testing.T) {
	release := make(chan struct{})
	o := New(WithLimits(Limits{MaxActiveRuns: 1}))
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
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

func TestRejectWhileBusyIsTheDefault(t *testing.T) {
	release := make(chan struct{})
	o := New()
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
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
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
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
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
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
	if err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
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
