package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func newRunner(t *testing.T, specs ...service.AgentSpec) (*service.Runner, session.Store) {
	t.Helper()
	store := session.NewMemoryStore()
	if len(specs) == 0 {
		specs = []service.AgentSpec{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}
	}
	runner, err := service.New(service.Config{Store: store, Specs: specs})
	if err != nil {
		t.Fatal(err)
	}
	return runner, store
}

func TestRunCreatesSessionAndAgentIsDisposable(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hello", Metadata: map[string]string{"tenant": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Result.Status != gotato.RunCompleted || first.FinalText != "echo: hello" || first.Messages != 2 || first.Agent != "echo" {
		t.Fatalf("first = %+v", first)
	}
	// No agent survives the run; the session does.
	if runner.ActiveRuns() != 0 {
		t.Fatalf("active runs after completion = %d", runner.ActiveRuns())
	}
	s, err := store.Get(ctx, first.SessionID)
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
	fresh, _ := store.Get(ctx, first.SessionID)
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
		service.AgentSpec{Name: "echo", Model: testkit.EchoModel{}},
		service.AgentSpec{Name: "tools", Model: model, Instruction: "spec instruction", Tools: []gotato.Tool{testkit.DemoEchoTool()}},
	)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "tools", map[string]string{
		service.MetaInstruction: "session instruction",
		service.MetaPanel:       "time",
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
	if len(request.Tools) != 1 || request.Tools[0].ID != testkit.DemoToolID {
		t.Fatalf("tools = %+v", request.Tools)
	}
	if !strings.Contains(gotato.TextOf(request.Messages[len(request.Messages)-1]), "<panel>") {
		t.Fatal("panel missing from the tail")
	}
	stored, _ := store.Get(ctx, s.ID())
	for _, message := range stored.Messages() {
		if strings.Contains(gotato.TextOf(message), "<panel>") {
			t.Fatal("panel leaked into the session")
		}
	}
	if _, err := runner.Run(ctx, service.RunRequest{Agent: "nope", Prompt: "x"}); !errors.Is(err, service.ErrUnknownAgent) {
		t.Fatalf("unknown agent err = %v", err)
	}
}

func TestSessionIsSingleFlightRejectPolicy(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := newRunner(t, service.AgentSpec{Name: "slow", Model: model})
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"})
	}()
	waitFor(t, func() bool { return model.Calls() == 1 })
	// The Run holds the Session lock; a second Run is rejected, not queued.
	_, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
	if !errors.Is(err, service.ErrBusy) && !gotato.IsCode(err, gotato.ErrBusy) {
		t.Fatalf("second run err = %v, want busy", err)
	}
	close(block)
	wg.Wait()
}

func TestWaitPolicyQueuesAndCancelRunAborts(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{
		Store:     store,
		Specs:     []service.AgentSpec{{Name: "slow", Model: model}},
		Admission: service.Admission{Queue: service.WaitWhileBusy, MaxActiveRuns: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	firstDone := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"})
		firstDone <- result
	}()
	waitFor(t, func() bool { return runner.CancelSession(ctx, s.ID()) == nil })
	// Cancelling the first run settles it as cancelled and frees the lock.
	first := <-firstDone
	if first.Result.Status != gotato.RunCanceled {
		t.Fatalf("first status = %s err=%v", first.Result.Status, first.Result.Error)
	}
	close(block)
	second, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
	if err != nil || second.Result.Status != gotato.RunCompleted {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	// Capacity: a third concurrent run on other sessions is rejected.
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityAndDrain(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	store := session.NewMemoryStore()
	runner, _ := service.New(service.Config{
		Store:     store,
		Specs:     []service.AgentSpec{{Name: "slow", Model: model}},
		Admission: service.Admission{MaxActiveRuns: 1},
	})
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(ctx, service.RunRequest{Prompt: "first"})
	}()
	waitFor(t, func() bool { return runner.ActiveRuns() == 1 })
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "second"}); !errors.Is(err, service.ErrCapacity) {
		t.Fatalf("capacity err = %v", err)
	}
	drainCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := runner.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain err = %v, want deadline (run was cancelled after grace)", err)
	}
	<-done
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "after"}); !errors.Is(err, service.ErrDraining) {
		t.Fatalf("after drain err = %v", err)
	}
}

func TestStreamRunInspectCompactFork(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	var kinds []gotato.EventKind
	result, err := runner.StreamRun(ctx, service.RunRequest{Prompt: "hi"}, func(event gotato.Event) error {
		kinds = append(kinds, event.Kind)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if kinds[0] != gotato.EventAgentStart || kinds[len(kinds)-1] != gotato.EventAgentEnd {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := 0; i < 3; i++ {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: result.SessionID, Prompt: "more"}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := runner.Inspect(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if report.SelectedMessages != 8 || report.PrefixHash == "" {
		t.Fatalf("report = %+v", report)
	}
	compact, err := runner.Compact(ctx, result.SessionID, modelctx.CompactOptions{Keep: 2})
	if err != nil || !compact.Replaced || compact.MessagesAfter != 3 {
		t.Fatalf("compact = %+v err=%v", compact, err)
	}
	child, err := runner.Fork(ctx, result.SessionID)
	if err != nil || child.ParentID() != result.SessionID || child.Len() != 3 {
		t.Fatalf("fork = %v err=%v", child, err)
	}
	if _, err := store.Get(ctx, child.ID()); err != nil {
		t.Fatal("fork not saved")
	}
	if _, err := runner.Inspect(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("inspect missing err = %v", err)
	}
}

func TestAutoCompactFromSpecAndSession(t *testing.T) {
	runner, store := newRunner(t, service.AgentSpec{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{Ceiling: 60}})
	ctx := context.Background()
	var last service.RunResult
	for i := 0; i < 6; i++ {
		var err error
		last, err = runner.Run(ctx, service.RunRequest{SessionID: last.SessionID, Prompt: strings.Repeat("word ", 20)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !last.Compacted {
		t.Fatalf("spec compaction never triggered: %+v", last)
	}
	s, _ := store.Get(ctx, last.SessionID)
	if len(s.Compactions()) == 0 {
		t.Fatal("no compaction recorded")
	}
}

func TestQueuedRunIsCancellable(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Specs:     []service.AgentSpec{{Name: "slow", Model: model}},
		Admission: service.Admission{Queue: service.WaitWhileBusy, MaxActiveRuns: 4},
	})
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}) }()
	waitFor(t, func() bool { return model.Calls() == 1 })

	queued := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
		queued <- result
	}()
	waitFor(t, func() bool { return runner.ActiveRuns() == 2 })

	// The started Run is the one CancelSession prefers.
	if err := runner.CancelSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-queued:
		t.Fatalf("cancelling the started run settled the queued one: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}

	// The queued Run becomes the started one; cancelling it settles it as
	// cancelled instead of leaving it invisible.
	waitFor(t, func() bool { return runner.CancelSession(ctx, s.ID()) == nil })
	select {
	case result := <-queued:
		if result.Result.Status != gotato.RunCanceled {
			t.Fatalf("queued status = %q err=%v, want cancelled", result.Result.Status, result.Result.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued run was not cancelled")
	}
	close(block)
}

func TestDrainCancelsQueuedRun(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Specs:     []service.AgentSpec{{Name: "slow", Model: model}},
		Admission: service.Admission{Queue: service.WaitWhileBusy, MaxActiveRuns: 4},
	})
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}) }()
	waitFor(t, func() bool { return model.Calls() == 1 })

	queued := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
		queued <- result
	}()
	waitFor(t, func() bool { return runner.ActiveRuns() == 2 })

	drainCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := runner.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain err = %v, want deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain waited %v for a queued run", elapsed)
	}
	select {
	case result := <-queued:
		if result.Result.Status != gotato.RunCanceled {
			t.Fatalf("queued status = %q, want cancelled", result.Result.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain left the queued run running")
	}
	close(block)
}

func TestCallerCancelReportsCancelled(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, store := newRunner(t, service.AgentSpec{Name: "slow", Model: model})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		done <- result
	}()
	waitFor(t, func() bool { return model.Calls() == 1 })
	cancel()
	result := <-done
	if result.Result.Status != gotato.RunCanceled {
		t.Fatalf("caller cancel status = %q err=%v, want cancelled", result.Result.Status, result.Result.Error)
	}
	stored, err := store.Get(context.Background(), result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runs := stored.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunCanceled {
		t.Fatalf("session run record = %+v, want cancelled", runs)
	}
	close(block)
}

func TestSessionMutationsRespectTheLock(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := newRunner(t, service.AgentSpec{Name: "slow", Model: model, Tools: []gotato.Tool{testkit.DemoEchoTool()}})
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"})
	}()
	// Calls() > 0 means the Run holds the Session lock and is talking to the Model.
	waitFor(t, func() bool { return model.Calls() == 1 })

	if _, err := runner.SetToolActive(ctx, s.ID(), testkit.DemoToolID, false); !gotato.IsCode(err, gotato.ErrBusy) {
		t.Fatalf("SetToolActive during a run err = %v, want busy", err)
	}
	if err := runner.DeleteSession(ctx, s.ID()); !gotato.IsCode(err, gotato.ErrBusy) {
		t.Fatalf("DeleteSession during a run err = %v, want busy", err)
	}
	close(block)
	<-done
	if _, err := runner.SetToolActive(ctx, s.ID(), testkit.DemoToolID, false); err != nil {
		t.Fatalf("SetToolActive after the run: %v", err)
	}
	entry, err := runner.SetToolActive(ctx, s.ID(), testkit.DemoToolID, true)
	if err != nil || !entry.Active {
		t.Fatalf("reactivate: %+v err=%v", entry, err)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
