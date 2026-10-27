package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// Instance A keeps a Session's stream open while a Run lands on B, then on A.
// The Events B wrote reach A's subscriber, read from the Record, with no gap:
// a stream never misses a stored Event, whoever wrote it.
func TestSessionStreamHasNoGapAfterAnotherInstanceWrote(t *testing.T) {
	ctx := context.Background()
	store := &watchedStore{Store: session.NewMemoryStore()}
	liveness := steadyLiveness
	a := instanceRunner(t, store, "a", testkit.EchoModel{}, liveness)
	b := instanceRunner(t, store, "b", testkit.EchoModel{}, liveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "p0"}); err != nil {
		t.Fatal(err)
	}
	before, err := a.Events(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	head := before[len(before)-1].Head().Number
	emptyBefore := store.emptyReads.Load()
	sub, err := a.StreamSession(ctx, s.ID(), service.StreamFrom{After: head})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	store.waitLive(t, emptyBefore) // the subscriber catches up and goes live
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "p2"}); err != nil {
		t.Fatal(err)
	}
	all, err := a.Events(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	last := all[len(all)-1].Head().Number
	prev := head
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for prev < last {
		event, err := sub.Next(read)
		if err != nil {
			t.Fatalf("after %d: %v", prev, err)
		}
		if event.Kind().Class() != gotato.EventClassStored {
			continue
		}
		if number := event.Head().Number; number != prev+1 {
			t.Fatalf("a gap on A's Session stream: %d (%s) follows %d", number, event.Kind(), prev)
		}
		prev = event.Head().Number
	}
}
