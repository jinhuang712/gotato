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

// These tests document what happens today when two processes use the same
// Config.Instance name, as a restarted StatefulSet pod does. They record
// current behavior, not a promise: the rule for callers is that an Instance
// name is unique per process start.

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

// A process that died without Close leaves its Liveness row to expire, and an
// expired row never comes back: a new process with the same name is refused
// by the store at its first write.
func TestDocumentsCurrentBehaviorReusedInstanceNameAfterExpiredRow(t *testing.T) {
	ctx := context.Background()
	clock := &reuseClock{now: time.Now()}
	store := session.NewMemoryStore(session.WithStoreClock(clock.Now))
	old := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, fastLiveness)
	if _, err := old.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatal(err)
	}
	// The old process is gone without Close; its row lapses.
	clock.Advance(time.Hour)

	restarted := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, fastLiveness)
	_, err := restarted.CreateSession(ctx, "echo", nil)
	if !errors.Is(err, session.ErrExpired) {
		t.Fatalf("a write by a new process under an expired name = %v, want session.ErrExpired", err)
	}
}

// A new process with the name of a live one finds the old Claim to be its own:
// it takes the Session over at once, without the holder check or the wait, and
// at the same Epoch. The old writer is fenced by the position of the Claim
// Entry, not by the Epoch.
func TestDocumentsCurrentBehaviorReusedInstanceNameWhileOldRunIsGoing(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	old := instanceRunner(t, store, "pod-0", held, fastLiveness)
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

	restarted := instanceRunner(t, store, "pod-0", testkit.EchoModel{}, service.Liveness{TTL: fastLiveness.TTL, Wait: 200 * time.Millisecond})
	began := time.Now()
	if _, err := restarted.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "again"}); err != nil {
		t.Fatalf("a Run under the same name while the old Run is going = %v, want it to take over", err)
	}
	if waited := time.Since(began); waited >= 150*time.Millisecond {
		t.Fatalf("took over after %v, want no wait: the old Claim looks like its own", waited)
	}
	view, err := restarted.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := view.Claim(); claim.Instance != "pod-0" || claim.Epoch != 1 {
		t.Fatalf("claim = %+v, want pod-0 at the same epoch", claim)
	}
	close(held.gate)
	oldErr := <-done
	t.Logf("the old Run, fenced by the position of the Claim Entry, returned: %v", oldErr)
}
