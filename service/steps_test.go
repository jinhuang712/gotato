package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// peekingModel calls a tool on its first call, and on its second reads the
// store to see what the Run has written so far.
type peekingModel struct {
	store session.Store
	mu    sync.Mutex
	id    string
	calls int
	seen  []gotato.Message
}

func (m *peekingModel) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	m.calls++
	call, id := m.calls, m.id
	m.mu.Unlock()
	if call == 1 {
		return testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: "lookup", Arguments: []byte(`{}`)})).Stream(ctx, gotato.ModelRequest{})
	}
	stored, err := session.Open(ctx, m.store, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.seen = stored.Messages()
	m.mu.Unlock()
	return testkit.NewFakeModel(testkit.Text("done")).Stream(ctx, gotato.ModelRequest{})
}

func TestEveryStepIsStoredBeforeTheNext(t *testing.T) {
	store := session.NewMemoryStore()
	model := &peekingModel{store: store}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{
		Name: "lookup", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "lookup", nil)
	if err != nil {
		t.Fatal(err)
	}
	model.id = s.ID()
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "find it"}); err != nil {
		t.Fatal(err)
	}
	// Before the second model call, the store held the prompt, the reply
	// that called the tool, and the tool's result.
	if len(model.seen) != 3 || model.seen[2].ToolResult == nil || model.seen[2].ToolResult.CallID != "call-1" || gotato.TextOf(model.seen[2]) != "found" {
		t.Fatalf("the store held %+v before the second model call", model.seen)
	}
}

// crashingStore fails every write, and every renewal of the instance's
// Liveness row, once a write has held a tool_started Entry, as the
// instance's death right after a tool starts would.
type crashingStore struct {
	session.Store
	mu      sync.Mutex
	crashed bool
}

func (s *crashingStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	s.mu.Lock()
	crashed := s.crashed
	s.mu.Unlock()
	if crashed {
		return context.Canceled
	}
	return s.Store.Beat(ctx, row, ttl)
}

func (s *crashingStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	s.mu.Lock()
	crashed := s.crashed
	for _, entry := range entries {
		if entry.Kind == gotato.KindToolStarted {
			s.crashed = true
		}
	}
	s.mu.Unlock()
	if crashed {
		return context.Canceled
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestACrashDuringAToolIsAnsweredOnTheNextRun(t *testing.T) {
	backing := session.NewMemoryStore()
	agent := func(model gotato.Model) service.Agent {
		return service.Agent{Name: "lookup", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")}}
	}
	// A short TTL lets the crashed instance's row expire quickly. The instance
	// that takes over is alive, so its TTL is long.
	crashLiveness := service.Liveness{TTL: 100 * time.Millisecond, Wait: 2 * time.Second}
	crashing, err := service.New(service.Config{Store: &crashingStore{Store: backing}, Liveness: crashLiveness, Agents: []service.Agent{
		agent(testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: "lookup", Arguments: []byte(`{}`)}))),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := crashing.CreateSession(ctx, "lookup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crashing.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "find it"}); err == nil {
		t.Fatal("the Run succeeded on a store that stopped writing")
	}

	// Another instance takes the Session over.
	model := testkit.NewFakeModel(testkit.Text("sorry, let me retry"))
	next, err := service.New(service.Config{Store: backing, Liveness: service.Liveness{TTL: steadyLiveness.TTL, Wait: 2 * time.Second}, Agents: []service.Agent{agent(model)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "did it work?"}); err != nil {
		t.Fatal(err)
	}
	request, _ := model.LastRequest()
	var answer *gotato.ToolResult
	for _, message := range request.Messages {
		if message.ToolResult != nil && message.ToolResult.CallID == "call-1" {
			answer = message.ToolResult
		}
	}
	if answer == nil || answer.SafeError != session.StoppedToolResult {
		t.Fatalf("the next request answered call-1 with %+v, want the stopped answer", answer)
	}
	view, err := next.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	runs := view.Runs()
	if len(runs) != 2 || runs[0].Error != "interrupted" || runs[1].Status != gotato.RunCompleted {
		t.Fatalf("runs = %+v, want the crashed Run ended as interrupted", runs)
	}
}

// A compacted Session that goes on and is loaded by another instance builds
// the same Context as before the reload (ROADMAP Phase 4).
func TestCompactAppendReloadKeepsTheContext(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := service.New(service.Config{Store: store, Instance: "instance-a", Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := first.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"one", "two", "three"} {
		if _, err := first.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt}); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := first.Compact(ctx, s.ID(), modelctx.CompactOptions{Keep: 2}); err != nil || !result.Replaced {
		t.Fatalf("Compact = %+v, %v", result, err)
	}
	if _, err := first.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "four"}); err != nil {
		t.Fatal(err)
	}
	before, err := first.Inspect(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close(ctx)

	second, err := service.New(service.Config{Store: store, Instance: "instance-b", Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := second.Inspect(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if after.PrefixHash != before.PrefixHash || after.SelectedMessages != before.SelectedMessages {
		t.Fatalf("after the reload: prefix %s with %d messages, before %s with %d", after.PrefixHash, after.SelectedMessages, before.PrefixHash, before.SelectedMessages)
	}
	if before.SelectedMessages != 8 {
		t.Fatalf("the compacted Session holds %d messages, want the Segment's four, the kept two, and the last Run's two", before.SelectedMessages)
	}
}
