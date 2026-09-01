package host

import (
	"context"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/orchestration"
)

// recordingSink collects what a protocol adapter would encode.
type recordingSink struct {
	mu     sync.Mutex
	events []ProjectedEvent
	fail   error
}

func (s *recordingSink) Deliver(ctx context.Context, event ProjectedEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) kinds() []gotato.EventKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make([]gotato.EventKind, 0, len(s.events))
	for _, event := range s.events {
		kinds = append(kinds, event.Event.Kind)
	}
	return kinds
}

func equivalenceModel() gotato.Model {
	return &twoTurnModel{release: closedChannel()}
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// embeddedKinds runs the same scripted scenario straight against Core.
func embeddedKinds(t *testing.T) []gotato.EventKind {
	t.Helper()
	agent, err := gotato.NewAgent(gotato.WithModel(equivalenceModel()), gotato.WithTool(echoTool{}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	stream, err := agent.(gotato.EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), gotato.UserMessage("hello"))
		done <- runErr
	}()
	var kinds []gotato.EventKind
	for {
		event, nextErr := stream.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		kinds = append(kinds, event.Kind)
		if event.Kind == gotato.EventAgentEnd {
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return kinds
}

// hostedKinds runs the same scenario through the Host semantic boundary.
func hostedKinds(t *testing.T) []gotato.EventKind {
	t.Helper()
	o := orchestration.New()
	err := o.Register(orchestration.Definition{Name: "default", New: func(ctx context.Context, request orchestration.Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(equivalenceModel()), gotato.WithTool(echoTool{}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(o)
	defer server.Drain(context.Background())

	sink := &recordingSink{}
	outcome, err := server.StreamRun(context.Background(), Command{
		AgentName:       "default",
		ConversationKey: "equivalence",
		Prompt:          "hello",
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != gotato.RunCompleted {
		t.Fatalf("hosted outcome = %+v", outcome)
	}
	return sink.kinds()
}

// TestEmbeddedAndHostedProduceTheSameEventSequence is the claim the whole
// layering rests on: a Host adds addressing and delivery, never a second set
// of Agent semantics.
func TestEmbeddedAndHostedProduceTheSameEventSequence(t *testing.T) {
	embedded := embeddedKinds(t)
	hosted := hostedKinds(t)
	if len(embedded) == 0 {
		t.Fatal("embedded run produced no Events")
	}
	if len(embedded) != len(hosted) {
		t.Fatalf("event counts differ:\nembedded %v\nhosted   %v", embedded, hosted)
	}
	for i := range embedded {
		if embedded[i] != hosted[i] {
			t.Fatalf("event %d differs: embedded %s, hosted %s\nembedded %v\nhosted   %v", i, embedded[i], hosted[i], embedded, hosted)
		}
	}
}

func TestProjectedEventsCarryRoutingMetadata(t *testing.T) {
	o := orchestration.New()
	err := o.Register(orchestration.Definition{Name: "default", New: func(ctx context.Context, request orchestration.Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(equivalenceModel()), gotato.WithTool(echoTool{}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(o)
	defer server.Drain(context.Background())

	sink := &recordingSink{}
	outcome, err := server.StreamRun(context.Background(), Command{AgentName: "default", ConversationKey: "routing", Prompt: "hello"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) == 0 {
		t.Fatal("no projected events")
	}
	for _, projected := range sink.events {
		if projected.ConversationID != outcome.ConversationID {
			t.Fatalf("projection lost the ConversationID: %+v", projected)
		}
		// Routing metadata rides alongside the Core Event; it never replaces
		// a Core field.
		if projected.Event.AgentID == "" || projected.Event.Sequence == 0 {
			t.Fatalf("projection damaged the Core Event: %+v", projected.Event)
		}
	}
}

func TestCommandRejectsAnAmbiguousInput(t *testing.T) {
	server := NewServer(orchestration.New())
	if _, err := server.Run(context.Background(), Command{AgentName: "default", ConversationKey: "x"}); !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("command with neither Prompt nor Continue = %v", err)
	}
	if _, err := server.Run(context.Background(), Command{AgentName: "default", ConversationKey: "x", Prompt: "hi", Continue: true}); !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("command with both Prompt and Continue = %v", err)
	}
}

func TestDeliveryFailureDoesNotDecideRunSettlement(t *testing.T) {
	o := orchestration.New()
	err := o.Register(orchestration.Definition{Name: "default", New: func(ctx context.Context, request orchestration.Request, snapshot *gotato.CoreSnapshot) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(equivalenceModel()), gotato.WithTool(echoTool{}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(o)
	defer server.Drain(context.Background())

	sink := &recordingSink{fail: context.Canceled}
	if _, streamErr := server.StreamRun(context.Background(), Command{AgentName: "default", ConversationKey: "delivery", Prompt: "hello"}, sink); streamErr == nil {
		t.Fatal("delivery failure was not reported")
	}
	// Delivery stopped, but the Agent is untouched and still usable.
	record, err := server.Conversation(context.Background(), conversationIDFor(t, server, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != orchestration.ConversationActive || record.LiveAgentID == "" {
		t.Fatalf("delivery failure changed the route: %+v", record)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		outcome, runErr := server.Run(context.Background(), Command{AgentName: "default", ConversationKey: "delivery", Prompt: "again"})
		if runErr == nil && outcome.Status == gotato.RunCompleted {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Agent unusable after a delivery failure: %v", runErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func conversationIDFor(t *testing.T, server *Server, key string) string {
	t.Helper()
	_, record, err := server.Orchestration.Resolve(context.Background(), orchestration.Request{
		AgentName: "default", ConversationKey: gotato.ConversationKey(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(record.ID)
}
