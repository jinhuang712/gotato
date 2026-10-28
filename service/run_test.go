package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func TestRunDisposesOfTheLiveSession(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hello", Metadata: map[string]string{"tenant": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Result.Status != gotato.RunStatusCompleted || first.FinalText != "echo: hello" || first.Messages != 2 || first.Agent != "echo" {
		t.Fatalf("first = %+v", first)
	}
	// No agent survives the run; the session does.
	if runner.ActiveRuns() != 0 {
		t.Fatalf("active runs after completion = %d", runner.ActiveRuns())
	}
	s, err := session.View(ctx, store, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if agent, _ := s.Get(service.MetaAgent); agent != "echo" {
		t.Fatalf("session agent metadata = %q", agent)
	}
	if tenant, _ := s.Get("tenant"); tenant != "a" {
		t.Fatalf("request metadata not applied: %v", s.Metadata())
	}
	second, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Messages != 4 || second.Result.RunID == first.Result.RunID {
		t.Fatalf("second = %+v", second)
	}
	if runs := s.Runs(); len(runs) != 1 {
		t.Fatalf("stale handle should not see the second run: %d", len(runs))
	}
	fresh, _ := session.View(ctx, store, first.SessionID)
	if len(fresh.Runs()) != 2 {
		t.Fatalf("store has %d runs, want 2", len(fresh.Runs()))
	}
}

func TestSpecsToolsAndSessionOverrides(t *testing.T) {
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"v"}`)}),
		testkit.Text("done"),
	)
	runner, store := newRunner(t,
		service.Agent{Name: "echo", Model: testkit.EchoModel{}},
		service.Agent{Name: "tools", Model: model, Instruction: "spec instruction", Tools: []gotato.Tool{testkit.DemoEchoTool()}},
	)
	ctx := context.Background()
	// A Session created before the panel was removed may still carry its
	// setting; the Runner ignores it.
	s, err := runner.CreateSession(ctx, "tools", map[string]string{
		service.MetaInstruction: "session instruction",
		"gotato.panel":          "time",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Result.Metrics.ToolCalls != 1 || result.Agent != "tools" {
		t.Fatalf("result = %+v", result)
	}
	request, _ := model.LastRequest()
	if request.SystemInstructions != "session instruction" {
		t.Fatalf("instruction override lost: %q", request.SystemInstructions)
	}
	if len(request.Tools) != 2 || request.Tools[0].ID != testkit.DemoToolID || request.Tools[1].ID != "read_result" {
		t.Fatalf("tools = %+v", request.Tools)
	}
	for _, message := range request.Messages {
		if strings.Contains(gotato.TextOf(message), "<panel>") {
			t.Fatalf("a request carries a panel: %+v", message)
		}
	}
	stored, _ := session.View(ctx, store, s.ID())
	if len(stored.Messages()) != 4 {
		t.Fatalf("stored %d messages, want 4", len(stored.Messages()))
	}
	if _, err := runner.Run(ctx, service.RunRequest{Agent: "nope", Prompt: "x"}); !errors.Is(err, service.ErrUnknownAgent) {
		t.Fatalf("unknown agent err = %v", err)
	}
}

func TestStreamRunInspectCompactFork(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	var kinds []gotato.EventKind
	result, err := runner.StreamRun(ctx, service.RunRequest{Prompt: "hi"}, func(event gotato.Event) error {
		kinds = append(kinds, event.Kind())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The Run's stream is the Session's own stored Events, ending at
	// run_ended; the store's timings are Instance Events and are not in it.
	// A new Session's stream opens with the Events that made it.
	last := len(kinds) - 1
	if kinds[0] != gotato.EventKindSessionCreated || !slices.Contains(kinds, gotato.EventKindRunStarted) || kinds[last] != gotato.EventKindRunEnded {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, kind := range kinds {
		if kind.Class() == gotato.EventClassInstance {
			t.Fatalf("the Run's stream carries %s: %v", kind, kinds)
		}
	}
	for range 3 {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: result.SessionID, Prompt: "more"}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := runner.Inspect(ctx, result.SessionID, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if report.SelectedMessages != 8 || report.PrefixHash == "" {
		t.Fatalf("report = %+v", report)
	}
	compact, err := runner.Compact(ctx, result.SessionID, modelctx.CompactConfig{KeepMessages: 2})
	if err != nil || !compact.Replaced || compact.MessagesAfter != 8 || compact.Compaction.Segment != 1 {
		t.Fatalf("compact = %+v err=%v", compact, err)
	}
	child, err := runner.Fork(ctx, result.SessionID, "")
	if err != nil || child.ParentID() != result.SessionID || child.Len() != 8 || len(child.Segments()) != 1 {
		t.Fatalf("fork = %v err=%v", child, err)
	}
	if _, err := session.View(ctx, store, child.ID()); err != nil {
		t.Fatal("fork not saved")
	}
	if _, err := runner.Inspect(ctx, "missing", service.InspectConfig{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("inspect missing err = %v", err)
	}
}

func TestAutoCompactFromSpecAndSession(t *testing.T) {
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{CeilingTokens: 20000}})
	ctx := context.Background()
	var last service.RunResponse
	for i := 0; i < 8 && !last.Compacted; i++ {
		var err error
		// A prompt past 2K tokens is cut in a Segment, so a compaction shrinks it.
		last, err = runner.Run(ctx, service.RunRequest{SessionID: last.SessionID, Prompt: strings.Repeat("word ", 4000)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !last.Compacted {
		t.Fatalf("spec compaction never triggered: %+v", last)
	}
	s, _ := session.View(ctx, store, last.SessionID)
	if len(s.Compactions()) == 0 {
		t.Fatal("no compaction recorded")
	}
}

func TestCallerCancelReportsCanceled(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, store := newRunner(t, service.Agent{Name: "slow", Model: model})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		done <- result
	}()
	waitFor(t, func() bool { return model.Calls() == 1 })
	cancel()
	result := <-done
	if result.Result.Status != gotato.RunStatusCanceled {
		t.Fatalf("caller cancel status = %q err=%v, want canceled", result.Result.Status, result.Result.Error)
	}
	stored, err := session.View(context.Background(), store, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runs := stored.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunStatusCanceled {
		t.Fatalf("session run record = %+v, want canceled", runs)
	}
	close(block)
}

// countingStore wraps a Store and counts its loads.
type countingStore struct {
	session.Store
	mu   sync.Mutex
	gets int
}

func (s *countingStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	return s.Store.Load(ctx, id)
}

func TestRunLoadsTheSessionOnce(t *testing.T) {
	store := &countingStore{Store: session.NewMemoryStore()}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if store.gets != 1 {
		t.Fatalf("a Run loaded the Session %d times, want 1", store.gets)
	}
}

// A canceled Run settles inside its live Session, so its result must keep the
// RunID, Turns and metrics the Session reported. Before the fix, canceling
// the Prompt context made Prompt give up on the settled result, and every
// canceled Run came back with an empty RunID.
func TestCanceledRunKeepsItsRunID(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, store := newRunner(t, service.Agent{Name: "slow", Model: model})
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan gotato.RunID, 1)
	done := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}, func(event gotato.Event) error {
			if event.Kind() == gotato.EventKindRunStarted {
				started <- event.Head().RunID
			}
			return nil
		})
		done <- result
	}()
	runID := <-started
	waitFor(t, func() bool { return model.Calls() == 1 })
	if err := runner.AbortSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Result.Status != gotato.RunStatusCanceled || result.Result.RunID != runID || result.Result.Metrics.Turns != 1 {
		t.Fatalf("canceled result = %+v, want status canceled, RunID %s, one Turn", result.Result, runID)
	}
	saved, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	runs := saved.Runs()
	if len(runs) != 1 || runs[0].RunID != runID || runs[0].Status != gotato.RunStatusCanceled {
		t.Fatalf("stored Runs = %+v", runs)
	}
}

func TestRunUpdateSetsMetadataUnlessSkipSave(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Update: map[string]string{"team": "orders"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Update: map[string]string{"team": "billing"}, SkipSave: true}); err != nil {
		t.Fatal(err)
	}
	stored, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if team, _ := stored.Get("team"); team != "orders" || stored.Len() != 2 {
		t.Fatalf("team = %q, messages = %d; want the saved Run's update and its two Messages", team, stored.Len())
	}
}

// A Run refused for its Agent writes nothing, the Update included.
func TestRunRefusedForItsAgentDoesNotApplyTheUpdate(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Agent: "nope", Prompt: "hi", Update: map[string]string{"team": "orders"}})
	if !errors.Is(err, service.ErrUnknownAgent) {
		t.Fatalf("err = %v, want ErrUnknownAgent", err)
	}
	stored, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if team, ok := stored.Get("team"); ok {
		t.Fatalf("the stored Record has team = %q from a refused Run", team)
	}
}

// Continue takes no Prompt: a request with both is refused, not run with the
// Prompt dropped.
func TestRunContinueWithAPromptIsRefused(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Continue: true})
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) || !gotato.NotRun(err) {
		t.Fatalf("err = %v, want invalid_argument that did not run", err)
	}
}

// StreamRunAdmitted calls admitted once the Run is admitted, whether or not
// there is a sink.
func TestStreamRunAdmittedCallsAdmittedWithANilSink(t *testing.T) {
	runner, _ := newRunner(t)
	calls := 0
	result, err := runner.StreamRunAdmitted(context.Background(), service.RunRequest{Prompt: "hi"}, func() { calls++ }, nil)
	if err != nil || result.FinalText != "echo: hi" {
		t.Fatalf("StreamRunAdmitted = %+v, %v", result, err)
	}
	if calls != 1 {
		t.Fatalf("admitted called %d times, want 1", calls)
	}
}
