package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func newRunner(t *testing.T, specs ...service.Agent) (*service.Runner, session.Store) {
	t.Helper()
	store := session.NewMemoryStore()
	if len(specs) == 0 {
		specs = []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}
	}
	runner, err := service.New(service.Config{Store: store, Agents: specs})
	if err != nil {
		t.Fatal(err)
	}
	return runner, store
}

func TestRunDisposesOfTheLiveSession(t *testing.T) {
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

func TestSessionIsSingleFlightRejectPolicy(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := newRunner(t, service.Agent{Name: "slow", Model: model})
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

func TestWaitPolicyQueuesAndCancelSessionAborts(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{
		Store:     store,
		Agents:    []service.Agent{{Name: "slow", Model: model}},
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
	// Wait until the Run reached the Model so cancellation exercises the
	// settled-cancelled path instead of racing the initial Session load.
	waitFor(t, func() bool { return model.Calls() == 1 })
	if err := runner.CancelSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
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
	// Capacity is exercised in TestCapacityAndDrain; a run on a free session
	// still succeeds here.
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
		Agents:    []service.Agent{{Name: "slow", Model: model}},
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
	if kinds[0] != gotato.KindSessionCreated || !slices.Contains(kinds, gotato.KindRunStarted) || kinds[last] != gotato.KindRunEnded {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, kind := range kinds {
		if kind.Class() == gotato.ClassInstance {
			t.Fatalf("the Run's stream carries %s: %v", kind, kinds)
		}
	}
	for range 3 {
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
	if _, err := runner.Inspect(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("inspect missing err = %v", err)
	}
}

func TestAutoCompactFromSpecAndSession(t *testing.T) {
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{Ceiling: 20000}})
	ctx := context.Background()
	var last service.RunResult
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

func TestQueuedRunIsCancellable(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "slow", Model: model}},
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

	// Let the first Run leave before cancelling again, so the next
	// CancelSession can only match the queued Run (its handle is the only one
	// registered), settling it as cancelled instead of leaving it invisible.
	waitFor(t, func() bool { return runner.ActiveRuns() == 1 })
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
		Agents:    []service.Agent{{Name: "slow", Model: model}},
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
	runner, store := newRunner(t, service.Agent{Name: "slow", Model: model})

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
	stored, err := session.View(context.Background(), store, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runs := stored.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunCanceled {
		t.Fatalf("session run record = %+v, want cancelled", runs)
	}
	close(block)
}

// failingSaveStore wraps a Store and fails the write that ends a Run.
type failingSaveStore struct {
	session.Store
	mu    sync.Mutex
	saves int
	// failFinal fails the write that ends a Run: the one holding run_ended.
	failFinal bool
	// failRelease fails the write that releases the Session, and only that one.
	failRelease bool
}

func (s *failingSaveStore) fail(entries []session.Entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	for _, entry := range entries {
		if s.failFinal && entry.Kind == gotato.KindRunEnded {
			return true
		}
		if s.failRelease && entry.Kind == gotato.KindSessionReleased {
			return true
		}
	}
	return false
}

func (s *failingSaveStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	if s.fail(entries) {
		return errors.New("store: disk full")
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *failingSaveStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	if s.fail(entries) {
		return errors.New("store: disk full")
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestUnpersistedRunIsReported(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true}
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if !errors.Is(err, service.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted", err)
	}
	if result.Result.Status != gotato.RunFailed || !gotato.IsCode(result.Result.Error, gotato.ErrStoreFailure) || result.FinalText != "echo: hi" {
		t.Fatalf("run outcome lost: %+v", result)
	}
}

func TestLostReleaseKeepsTheRunCompletedAndWarns(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failRelease: true}
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := instanceRecorder(t, runner)
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if err != nil || result.Result.Status != gotato.RunCompleted || result.Result.Error != nil || result.FinalText != "echo: hi" {
		t.Fatalf("a Run whose Events are all stored = %+v, %v, want completed without an error", result, err)
	}
	instance.Wait(5*time.Second, func(event gotato.Event) bool { return event.Kind() == gotato.KindStoreWriteFailed })
	if len(instance.OfKind(gotato.KindStoreWriteFailed)) == 0 {
		t.Fatalf("no store_write_failed warning among %v", instance.Kinds())
	}
}

func TestRunnerKeysEachSessionsPromptCache(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"), testkit.Text("c"))
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: model})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	other, err := runner.Run(ctx, service.RunRequest{Prompt: "three"})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) != 3 {
		t.Fatalf("model calls = %d, want 3", len(requests))
	}
	// One stable key per Session, and never the raw Session ID.
	want := service.CacheKey(first.SessionID)
	if requests[0].CacheKey != want || requests[1].CacheKey != want ||
		requests[2].CacheKey != service.CacheKey(other.SessionID) || requests[2].CacheKey == want ||
		strings.Contains(want, first.SessionID) {
		t.Fatalf("cache keys = %q, %q, %q; want %q twice, then another", requests[0].CacheKey, requests[1].CacheKey, requests[2].CacheKey, want)
	}
}

func TestForkKeepsTheParentsCacheKey(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"), testkit.Text("c"))
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: model})
	ctx := context.Background()
	parent, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.Fork(ctx, parent.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := runner.Fork(ctx, child.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{child.ID(), grandchild.ID()} {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "branch"}); err != nil {
			t.Fatal(err)
		}
	}
	// A fork starts with its parent's history, so it reads the parent's cached
	// prefix: every generation carries the first parent's key.
	want := service.CacheKey(parent.SessionID)
	for i, request := range model.Requests() {
		if request.CacheKey != want {
			t.Fatalf("request %d cache key = %q, want the parent's %q", i, request.CacheKey, want)
		}
	}
	stored, err := session.View(ctx, store, grandchild.ID())
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := stored.Get(service.MetaCacheKey); key != want {
		t.Fatalf("stored %s = %q, want %q", service.MetaCacheKey, key, want)
	}
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

// instanceRecorder records the Runner's Instance stream from now on.
func instanceRecorder(t *testing.T, runner *service.Runner, options ...gotato.SubscribeOption) *testkit.EventRecorder {
	t.Helper()
	sub, err := runner.SubscribeInstance(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	rec := testkit.RecordSubscription(sub)
	t.Cleanup(rec.Close)
	return rec
}

func kindsOf(events []gotato.Event) []gotato.EventKind {
	kinds := make([]gotato.EventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind()
	}
	return kinds
}

// A load and a save are Instance Events: they time the store, they go to the
// Instance's subscribers, and they are neither in a Run's stream nor in the
// Session's Record.
func TestStoreEventsAreInstanceEvents(t *testing.T) {
	// No warm copy, so the second Run loads the Session and times the load.
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Warm: service.WarmPolicy{TTL: -1}})
	instance := instanceRecorder(t, runner)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}

	var events []gotato.Event
	result, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}, func(event gotato.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(events); len(kinds) == 0 || !slices.Contains(kinds, gotato.KindRunStarted) || kinds[len(kinds)-1] != gotato.KindRunEnded {
		t.Fatalf("the Run's stream = %v, want it to hold run_started and end at run_ended", kinds)
	}
	for _, event := range events {
		if event.Kind().Class() == gotato.ClassInstance {
			t.Fatalf("the Run's stream carries the Instance Event %s", event.Kind())
		}
	}

	isSaved := func(event gotato.Event) bool {
		saved, ok := event.(gotato.SessionSaved)
		return ok && saved.RunID == result.Result.RunID
	}
	if !instance.Wait(5*time.Second, isSaved) {
		t.Fatalf("the Instance stream never saw the save of %s: %v", result.Result.RunID, instance.Kinds())
	}
	var loaded gotato.SessionLoaded
	var saved gotato.SessionSaved
	for _, event := range instance.Events() {
		switch e := event.(type) {
		case gotato.SessionLoaded:
			if string(e.SessionID) == first.SessionID {
				loaded = e
			}
		case gotato.SessionSaved:
			if e.RunID == result.Result.RunID {
				saved = e
			}
		}
	}
	if loaded.Kind() == "" || loaded.RunID != "" || loaded.Number != 0 || loaded.Messages != first.Messages || loaded.ElapsedMS < 0 || loaded.Failed {
		t.Fatalf("session_loaded = %+v, want the %d stored messages", loaded, first.Messages)
	}
	if string(saved.SessionID) != first.SessionID || saved.Messages != result.Messages || saved.Failed {
		t.Fatalf("session_saved = %+v, want %d messages", saved, result.Messages)
	}

	// They time the store; they are not part of the Session's own Record.
	stored, err := session.StoredEvents(ctx, store, first.SessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range stored {
		if event.Kind().Class() == gotato.ClassInstance {
			t.Fatalf("the Record holds %s", event.Kind())
		}
	}
}

func TestStoreEventsFlagFailuresWithoutTheirText(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	instance := instanceRecorder(t, runner)
	ctx := context.Background()
	sink := func(gotato.Event) error { return nil }
	if _, err := runner.StreamRun(ctx, service.RunRequest{Prompt: "hi"}, sink); !errors.Is(err, service.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted", err)
	}
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: "missing", Prompt: "hi"}, sink); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	instance.Wait(5*time.Second, func(event gotato.Event) bool {
		loaded, ok := event.(gotato.SessionLoaded)
		return ok && loaded.Failed
	})
	var flagged []gotato.EventKind
	for _, event := range instance.Events() {
		raw, err := gotato.MarshalEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "disk full") {
			t.Fatalf("%s carries the store's error text: %s", event.Kind(), raw)
		}
		switch e := event.(type) {
		case gotato.SessionSaved:
			if e.Failed {
				flagged = append(flagged, e.Kind())
			}
		case gotato.SessionLoaded:
			if e.Failed {
				flagged = append(flagged, e.Kind())
			}
		}
	}
	if len(flagged) != 2 || flagged[0] != gotato.KindSessionSaved || flagged[1] != gotato.KindSessionLoaded {
		t.Fatalf("flagged = %v, want the failed save and the failed load", flagged)
	}
}

// unbuildableSpec is an Agent whose live Session cannot be built: a Run creates
// the Record, then fails before the live Session starts.
func unbuildableSpec() service.Agent {
	negative := -1.0
	return service.Agent{Name: "echo", Model: testkit.EchoModel{}, Options: []gotato.Option{
		gotato.WithModelOptions(gotato.ModelOptions{Temperature: &negative}),
	}}
}

func TestFailedOneShotLeavesNoSession(t *testing.T) {
	runner, store := newRunner(t, unbuildableSpec())
	_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "x"})
	if !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("failed one-shot left %d sessions (err=%v)", len(list), err)
	}
}

// failingDeleteStore fails every Delete, standing in for a Store whose cleanup
// cannot remove the one-shot Session.
type failingDeleteStore struct {
	session.Store
	err error
}

func (s *failingDeleteStore) Delete(context.Context, string) error { return s.err }

func TestFailedOneShotCleanupIsReported(t *testing.T) {
	delErr := errors.New("store: delete denied")
	runner, err := service.New(service.Config{
		Store:  &failingDeleteStore{Store: session.NewMemoryStore(), err: delErr},
		Agents: []service.Agent{unbuildableSpec()},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), service.RunRequest{Prompt: "x"})
	if !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if !errors.Is(err, delErr) {
		t.Fatalf("cleanup failure not reported: %v", err)
	}
}

func TestUnpersistedFailedRunStillReportsSaveFailure(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true}
	model := testkit.NewFakeModel(testkit.Text("x"))
	model.Err = errors.New("model exploded")
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "boom", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if !errors.Is(err, service.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted in the chain", err)
	}
	if !strings.Contains(err.Error(), "model exploded") {
		t.Fatalf("run error lost from the joined error: %v", err)
	}
	if result.Result.Status != gotato.RunFailed {
		t.Fatalf("status = %q err=%v, want failed", result.Result.Status, result.Result.Error)
	}
}

// createErrorStore fails to create one ID, standing in for a Store that is
// broken rather than holding the Session already.
type createErrorStore struct {
	session.Store
	failID string
	err    error
}

func (s *createErrorStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	if id == s.failID {
		return s.err
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func TestForkTreatsStoreErrorAsFailure(t *testing.T) {
	backing := session.NewMemoryStore()
	getErr := errors.New("store: unavailable")
	runner, err := service.New(service.Config{
		Store:  &createErrorStore{Store: backing, failID: "child", err: getErr},
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "child"); !errors.Is(err, getErr) {
		t.Fatalf("fork err = %v, want the store error", err)
	}
	if _, err := session.View(ctx, backing, "child"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("fork saved a session despite a store error: %v", err)
	}
}

func TestForkWithExplicitIDIsExclusive(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Fork(ctx, parent.ID(), "shared-fork")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		// A loser either finds the ID taken or is rejected while the winner
		// holds the lock; neither may overwrite the winner.
		if !gotato.IsCode(err, gotato.ErrInvalidArgument) && !gotato.IsCode(err, gotato.ErrBusy) {
			t.Fatalf("unexpected fork err = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful forks = %d, want exactly 1", succeeded)
	}
}

func TestSessionMutationsRespectTheLock(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := newRunner(t, service.Agent{Name: "slow", Model: model, Tools: []gotato.Tool{testkit.DemoEchoTool()}})
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
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// blockingGetStore stalls Get until its context ends, standing in for a slow
// or hung Store during the pre-lock load.
type blockingGetStore struct {
	session.Store
	once    sync.Once
	started chan struct{}
}

func (s *blockingGetStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return session.Loaded{}, ctx.Err()
}

// A Run that is admitted but still loading its Session must be reachable by
// CancelSession; before the handle was registered up front it was invisible.
func TestCancelSessionReachesRunLoadingSession(t *testing.T) {
	blocking := &blockingGetStore{Store: session.NewMemoryStore(), started: make(chan struct{})}
	runner, err := service.New(service.Config{
		Store:  blocking,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resultCh := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(context.Background(), service.RunRequest{SessionID: "loading-1", Prompt: "hi"})
		resultCh <- runErr
	}()
	<-blocking.started

	if err := runner.CancelSession(context.Background(), "loading-1"); err != nil {
		t.Fatalf("CancelSession while loading = %v", err)
	}
	select {
	case runErr := <-resultCh:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("run error = %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after CancelSession")
	}
}

// briefNote is a ContextTransformer that puts a note in front of the history.
type briefNote struct{}

func (briefNote) Transform(_ context.Context, snapshot gotato.ContextSnapshot) ([]gotato.Message, error) {
	return append([]gotato.Message{gotato.UserMessage("note: answer briefly")}, snapshot.Messages...), nil
}

// TestInspectMatchesTheNextRun checks GOALS.md G-G03 at the Runner: what
// Inspect reports is what the next Run's first Turn sends, Extensions and
// model options included.
func TestInspectMatchesTheNextRun(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("on its way"))
	maxTokens := gotato.ModelOptions{MaxTokens: 256}
	runner, _ := newRunner(t, service.Agent{
		Name:        "orders",
		Model:       model,
		Instruction: "You track orders.",
		Tools:       []gotato.Tool{testkit.NewFakeTool("orders.lookup", "shipped")},
		Extensions:  []any{briefNote{}},
		Options:     []gotato.Option{gotato.WithModelOptions(maxTokens)},
	})
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "orders", nil, session.WithMessages(gotato.UserMessage("where is order 42?")))
	if err != nil {
		t.Fatal(err)
	}

	report, err := runner.Inspect(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if report.Request.Options.MaxTokens != 256 || gotato.TextOf(report.Request.Messages[0]) != "note: answer briefly" {
		t.Fatalf("the report left out the Spec's Extensions or model options: %+v", report.Request)
	}
	if report.CompactionPending || report.CompactCeiling != 0 {
		t.Fatalf("no compaction is configured: %+v", report)
	}

	var prefix any
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Continue: true}, func(event gotato.Event) error {
		if built, ok := event.(gotato.ContextBuilt); ok {
			prefix = built.PrefixHash
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if prefix != report.PrefixHash {
		t.Fatalf("context_built prefix %v, report prefix %s", prefix, report.PrefixHash)
	}
	sent, _ := model.LastRequest()
	want, _ := json.Marshal(report.Request)
	got, _ := json.Marshal(sent)
	if string(got) != string(want) {
		t.Fatalf("the Run sent a different request\n got: %s\nwant: %s", got, want)
	}
}

func TestInspectSaysWhenCompactionIsPending(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{Ceiling: 20000}})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Inspect(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if report.CompactionPending || report.CompactCeiling != 20000 {
		t.Fatalf("a short Session is under the ceiling: %+v", report)
	}
	for range 2 {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: strings.Repeat("word ", 4000)}); err != nil {
			t.Fatal(err)
		}
	}
	report, err = runner.Inspect(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.CompactionPending {
		t.Fatalf("a Session past the ceiling must report a pending compaction: approx %d tokens", report.ApproxTokens)
	}
}

// A cancelled Run settles inside its live Session, so its result must keep the
// RunID, Turns and metrics the Session reported. Before the fix, cancelling
// the Prompt context made Prompt give up on the settled result, and every
// cancelled Run came back with an empty RunID.
func TestCancelledRunKeepsItsRunID(t *testing.T) {
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
	done := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}, func(event gotato.Event) error {
			if event.Kind() == gotato.KindRunStarted {
				started <- event.Head().RunID
			}
			return nil
		})
		done <- result
	}()
	runID := <-started
	waitFor(t, func() bool { return model.Calls() == 1 })
	if err := runner.CancelSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Result.Status != gotato.RunCanceled || result.Result.RunID != runID || result.Result.Metrics.Turns != 1 {
		t.Fatalf("cancelled result = %+v, want status cancelled, RunID %s, one Turn", result.Result, runID)
	}
	saved, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	runs := saved.Runs()
	if len(runs) != 1 || runs[0].RunID != runID || runs[0].Status != gotato.RunCanceled {
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
