package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// Config.Instance is a label: each start appends a random suffix, so the
// store identity is unique per process start. These tests run a restarted
// process, and a process that overlaps the one it replaces, under one label.

type reuseClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *reuseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *reuseClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// A process that died without Close leaves its Liveness row to expire. The
// restarted process has the same label but its own name, so it writes under a
// new row and is not refused.
func TestReusedInstanceLabelAfterExpiredRowWritesFine(t *testing.T) {
	ctx := context.Background()
	clock := &reuseClock{now: time.Now()}
	store := session.NewMemoryStore(session.WithStoreClock(clock.Now))
	old := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, steadyLiveness)
	s, err := old.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The old process is gone without Close; its row lapses.
	clock.Advance(time.Hour)

	restarted := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, steadyLiveness)
	if !isInstance(old.Instance(), "pod-0") || !isInstance(restarted.Instance(), "pod-0") || old.Instance() == restarted.Instance() {
		t.Fatalf("instances = %q and %q, want pod-0 and a different suffix each", old.Instance(), restarted.Instance())
	}
	if _, err := restarted.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatalf("a write by a restarted process under the same label = %v, want it to succeed", err)
	}
	if _, err := restarted.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "again"}); err != nil {
		t.Fatalf("a Run on the old process's Session = %v, want it taken over", err)
	}
	view, err := restarted.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := view.Claim(); claim.Instance != restarted.Instance() || claim.Epoch != 2 {
		t.Fatalf("claim = %+v, want the restarted instance at epoch 2", claim)
	}
}

// A new process with the label of a live one does not inherit its Claim: it
// goes through the holder check like any other Instance, so it waits for the
// live writer and is then refused as busy.
func TestReusedInstanceLabelWhileOldRunIsGoingIsBusy(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	old := instanceRunner(t, store, "pod-0", held, steadyLiveness)
	s, err := old.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := old.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
		done <- err
	}()
	<-held.started

	restarted := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, service.Liveness{TTL: steadyLiveness.TTL, Wait: 200 * time.Millisecond})
	began := time.Now()
	_, err = restarted.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "again"})
	if !errors.Is(err, service.ErrBusy) {
		t.Fatalf("a Run under the same label while the old Run is going = %v, want ErrBusy", err)
	}
	if waited := time.Since(began); waited < 150*time.Millisecond {
		t.Fatalf("refused after %v, want the wait for the live writer", waited)
	}
	view, err := restarted.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := view.Claim(); claim.Instance != old.Instance() || claim.Epoch != 1 {
		t.Fatalf("claim = %+v, want the old instance at its own epoch", claim)
	}
	close(held.gate)
	if err := <-done; err != nil {
		t.Fatalf("the old Run = %v, want it to complete: it was never fenced", err)
	}
}

// The old process's late Close removes its own row only, not the row of the
// process that reuses its label.
func TestLateCloseOfReusedInstanceLabelKeepsTheNewRow(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	old := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, steadyLiveness)
	if _, err := old.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatal(err)
	}
	restarted := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, steadyLiveness)
	if _, err := restarted.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err := store.Instance(ctx, restarted.Instance()); err != nil || !state.Alive {
		t.Fatalf("the new process's row after the old Close = %+v, %v; want alive", state, err)
	}
	if _, err := restarted.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatalf("a write by the new process after the old Close = %v, want it to succeed", err)
	}
}
