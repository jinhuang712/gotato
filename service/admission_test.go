package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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
	if !errors.Is(err, service.ErrBusy) && !gotato.IsCode(err, gotato.CodeBusy) {
		t.Fatalf("second run err = %v, want busy", err)
	}
	close(block)
	wg.Wait()
}

func TestWaitPolicyQueuesAndAbortSessionAborts(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{
		Store:     store,
		Agents:    []service.Agent{{Name: "slow", Model: model}},
		Admission: service.Admission{Queue: service.QueuePolicyWait, MaxActiveRuns: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	firstDone := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"})
		firstDone <- result
	}()
	// Wait until the Run reached the Model so cancellation exercises the
	// settled-canceled path instead of racing the initial Session load.
	waitFor(t, func() bool { return model.Calls() == 1 })
	if err := runner.AbortSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	// Canceling the first run settles it as canceled and frees the lock.
	first := <-firstDone
	if first.Result.Status != gotato.RunStatusCanceled {
		t.Fatalf("first status = %s err=%v", first.Result.Status, first.Result.Error)
	}
	close(block)
	second, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
	if err != nil || second.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("second = %+v err=%v", second, err)
	}
	// Capacity is exercised in TestCapacityAndDrain; a run on a free session
	// still succeeds here.
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedRunIsCancelable(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "slow", Model: model}},
		Admission: service.Admission{Queue: service.QueuePolicyWait, MaxActiveRuns: 4},
	})
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}) }()
	waitFor(t, func() bool { return model.Calls() == 1 })

	queued := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
		queued <- result
	}()
	waitFor(t, func() bool { return runner.ActiveRuns() == 2 })

	// The started Run is the one AbortSession prefers.
	if err := runner.AbortSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-queued:
		t.Fatalf("canceling the started run settled the queued one: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}

	// Let the first Run leave before canceling again, so the next
	// AbortSession can only match the queued Run (its handle is the only one
	// registered), settling it as canceled instead of leaving it invisible.
	waitFor(t, func() bool { return runner.ActiveRuns() == 1 })
	waitFor(t, func() bool { return runner.AbortSession(ctx, s.ID()) == nil })
	select {
	case result := <-queued:
		if result.Result.Status != gotato.RunStatusCanceled {
			t.Fatalf("queued status = %q err=%v, want canceled", result.Result.Status, result.Result.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued run was not canceled")
	}
	close(block)
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

	if _, err := runner.SetToolActive(ctx, s.ID(), testkit.DemoToolID, false); !gotato.IsCode(err, gotato.CodeBusy) {
		t.Fatalf("SetToolActive during a run err = %v, want busy", err)
	}
	if err := runner.DeleteSession(ctx, s.ID()); !gotato.IsCode(err, gotato.CodeBusy) {
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
// AbortSession; before the handle was registered up front it was invisible.
func TestAbortSessionReachesRunLoadingSession(t *testing.T) {
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

	if err := runner.AbortSession(context.Background(), "loading-1"); err != nil {
		t.Fatalf("AbortSession while loading = %v", err)
	}
	select {
	case runErr := <-resultCh:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("run error = %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after AbortSession")
	}
}
