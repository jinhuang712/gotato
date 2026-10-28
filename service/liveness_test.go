package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// Config.Instance is a label: each start appends a random suffix, so the
// store identity is unique per process start. These tests run a restarted
// process, and a process that overlaps the one it replaces, under one label.

// deadStore is a Store whose process died without Close: after die, its
// heartbeats reach nothing, and the Liveness row it left is removed.
type deadStore struct {
	session.Store
	dead atomic.Bool
}

func (d *deadStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if d.dead.Load() {
		return nil
	}
	return d.Store.Beat(ctx, row, ttl)
}

// die ends the Liveness row of instance, as its expiry would.
func (d *deadStore) die(ctx context.Context, instance string) error {
	d.dead.Store(true)
	return d.Store.Beat(ctx, session.InstanceRow{ID: instance}, 0)
}

// A process that died without Close leaves its Liveness row to expire. The
// restarted process has the same label but its own name, so it writes under a
// new row and is not refused.
func TestReusedInstanceLabelAfterExpiredRowWritesFine(t *testing.T) {
	ctx := context.Background()
	shared := session.NewMemoryStore()
	store := &deadStore{Store: shared}
	old := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, steadyLiveness)
	s, err := old.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The old process is gone without Close; its row lapses.
	if err := store.die(ctx, old.Instance()); err != nil {
		t.Fatal(err)
	}

	restarted := instanceRunner(t, shared, "pod-0", testkit.EchoModel{}, steadyLiveness)
	if !isInstance(old.Instance(), "pod-0") || !isInstance(restarted.Instance(), "pod-0") || old.Instance() == restarted.Instance() {
		t.Fatalf("instances = %q and %q, want pod-0 and a different suffix each", old.Instance(), restarted.Instance())
	}
	if _, err := restarted.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatalf("a write by a restarted process under the same label = %v, want it to succeed", err)
	}
	if _, err := restarted.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "again"}); err != nil {
		t.Fatalf("a Run on the old process's Session = %v, want it taken over", err)
	}
	view, err := restarted.Record(ctx, s.ID())
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
	view, err := restarted.Record(ctx, s.ID())
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

// A store that is out of reach for longer than the Liveness TTL expires the
// Instance's row, and the row never comes back. The Instance must take a new
// name and write again once the store answers, not fail every Run for good.
func TestInstanceRecoversWhenTheStoreReturnsAfterTheTTL(t *testing.T) {
	ctx := context.Background()
	store := &faultStore{Store: session.NewMemoryStore()}
	liveness := service.Liveness{TTL: 300 * time.Millisecond, Wait: time.Second}
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, liveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := a.Instance()

	store.fail("Beat", true)
	time.Sleep(2 * liveness.TTL)

	// While the store is out the Instance says so, and a Run is refused as
	// not run: it never reached the store.
	if !a.Lapsed() {
		t.Fatal("the Instance is not Lapsed after its row could not be renewed for two TTLs")
	}
	_, err = a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "during"})
	if !gotato.NotRun(err) {
		t.Fatalf("a Run refused because the row lapsed = %v, want not run", err)
	}

	store.fail("Beat", false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "after"})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Instance did not recover after the store returned: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if a.Lapsed() {
		t.Fatal("the Instance is still Lapsed after it wrote again")
	}
	if a.Instance() == before || !isInstance(a.Instance(), "instance-a") {
		t.Fatalf("Instance() = %q after recovery (was %q), want a new name with the same label", a.Instance(), before)
	}
	state, err := store.Instance(ctx, a.Instance())
	if err != nil || !state.Alive {
		t.Fatalf("the new name's row = %+v, %v; want alive", state, err)
	}
	// Its Claim is the new name's.
	record, err := a.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := record.Claim(); claim.Instance != a.Instance() {
		t.Fatalf("Claim = %+v, want the new name %q", claim, a.Instance())
	}
}
