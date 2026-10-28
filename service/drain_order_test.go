package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// slowBeatStore holds every Liveness write while hold is set, until the
// write's context ends, as a slow store would.
type slowBeatStore struct {
	session.Store
	hold atomic.Bool
}

func (s *slowBeatStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.hold.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Store.Beat(ctx, row, ttl)
}

// Drain says it drains before it writes the row, so a slow store does not
// leave a window in which Runs are admitted on an Instance the others already
// see leaving.
func TestDrainMarksDrainingBeforeTheRowIsWritten(t *testing.T) {
	ctx := context.Background()
	store := &slowBeatStore{Store: session.NewMemoryStore()}
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, service.Liveness{TTL: 3 * time.Second, Wait: time.Second})
	t.Cleanup(func() { store.hold.Store(false) })
	if _, err := a.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatal(err)
	}
	store.hold.Store(true)
	done := make(chan error, 1)
	go func() { done <- a.Drain(ctx) }()
	deadline := time.Now().Add(400 * time.Millisecond)
	for !a.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the Runner is not draining while the write of its row is on its way")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := a.Run(ctx, service.RunRequest{Prompt: "new"}); !errors.Is(err, service.ErrDraining) {
		t.Fatalf("a Run while the row is being written = %v, want ErrDraining", err)
	}
	store.hold.Store(false)
	<-done
}

// A tool that ignores its context holds a Run, and Drain after its ctx ended
// waits for the Run only a bounded time.
func TestDrainReturnsAfterItsContextEvenForAToolThatIgnoresIt(t *testing.T) {
	ctx := context.Background()
	stuck := make(chan struct{})
	started := make(chan struct{})
	tool, err := gotato.NewFuncTool("stuck", "ignores its context", func(context.Context, struct{}) (string, error) {
		close(started)
		<-stuck
		return "late", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "stuck", Arguments: []byte(`{}`)}), testkit.Text("ok"))
	a, err := service.New(service.Config{
		Store: session.NewMemoryStore(), Liveness: steadyLiveness,
		Agents: []service.Agent{{Name: "echo", Model: model, Tools: []gotato.Tool{tool}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(stuck); _ = a.Close(ctx) })
	a.SetDrainStopWait(300 * time.Millisecond)
	go func() { _, _ = a.Run(ctx, service.RunRequest{Prompt: "go"}) }()
	<-started

	drainCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	err = a.Drain(drainCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want its context's error", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("Drain took %v after its context ended; a tool that ignores its context must not hold it", took)
	}
}

// A draining Runner does not claim an idle Session for a compaction.
func TestCompactRefusesWhileDraining(t *testing.T) {
	ctx := context.Background()
	a := instanceRunner(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = a.Compact(ctx, s.ID(), modelctx.CompactConfig{})
	if !errors.Is(err, service.ErrDraining) || !gotato.NotRun(err) {
		t.Fatalf("Compact while draining = %v, want ErrDraining, not run", err)
	}
}

// Drain has a bound of its own: a Run still going when Config.DrainTimeout is
// over is stopped, with a ctx that has no deadline.
func TestDrainHasATimeoutOfItsOwn(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	block := make(chan struct{})
	model.Block = block
	t.Cleanup(func() { close(block) })
	a, err := service.New(service.Config{
		Store: session.NewMemoryStore(), Liveness: steadyLiveness, DrainTimeout: 200 * time.Millisecond,
		Agents: []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(ctx) })
	result := make(chan service.RunResponse, 1)
	go func() {
		response, _ := a.Run(ctx, service.RunRequest{Prompt: "go"})
		result <- response
	}()
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	began := time.Now()
	err = a.Drain(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want the timeout", err)
	}
	if took := time.Since(began); took < 150*time.Millisecond || took > 5*time.Second {
		t.Fatalf("Drain took %v, want about its 200ms timeout", took)
	}
	if response := <-result; response.Reconnect == nil {
		t.Fatalf("the Run the drain stopped = %+v, want a reconnect", response)
	}
}

func TestDrainTimeoutDefaultsToTenSeconds(t *testing.T) {
	if service.DefaultDrainTimeout != 10*time.Second {
		t.Fatalf("DefaultDrainTimeout = %v, want 10s", service.DefaultDrainTimeout)
	}
}
