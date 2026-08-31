package orchestration

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

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

func (failingStore) Save(context.Context, Record) error { return errors.New("store unavailable") }

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
	if err := o.CloseAgent(context.Background(), current.LiveAgentID); err != nil {
		t.Fatal(err)
	}
}

func generationPtr(value gotato.AgentGeneration) *gotato.AgentGeneration { return &value }
