package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestStaleCopyCannotOverwriteANewerRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	id := first.Record().ID()
	second, err := session.OpenWriter(ctx, store, id, "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	if claim := second.Record().Claim(); claim.Instance != "instance-b" || claim.Epoch != 2 {
		t.Fatalf("claim = %+v, want instance-b at epoch 2", claim)
	}
	commit(t, second, gotato.UserMessage("from b"))
	_ = first.Record().Append(gotato.UserMessage("from a, stale"))
	_, err = first.Write(ctx, []gotato.Event{messageEvent(gotato.UserMessage("from a, stale"))})
	if !errors.Is(err, session.ErrConflict) || !gotato.IsCode(err, gotato.CodeConflict) {
		t.Fatalf("stale Flush = %v, want a conflict", err)
	}
	if again := first.Flush(ctx); !errors.Is(again, session.ErrConflict) {
		t.Fatalf("a Writer that failed wrote again: %v", again)
	}
	stored, err := session.Open(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if messages := stored.Messages(); len(messages) != 1 || gotato.TextOf(messages[0]) != "from b" {
		t.Fatalf("stored = %+v, want only the newer writer's Message", messages)
	}
}

func TestCreateRefusesATakenID(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("taken")), "instance-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("taken")), "instance-b"); !errors.Is(err, session.ErrExists) {
		t.Fatalf("Create = %v, want ErrExists", err)
	}
}

// A new Record is busy until its Writer releases it, as one that OpenWriter
// opens is, so another Instance does not take it as idle while its first Run
// is still going. WithIdle, for a Record that nothing runs against, writes the
// release in the same write.
func TestCreateIsBusyUntilReleasedUnlessIdle(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	busy, err := session.CreateWriter(ctx, store, session.New(session.WithID("busy")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "busy"); err != nil || !loaded.Info.Busy || loaded.Head != 2 {
		t.Fatalf("a new Record = %+v, %v; want busy at head 2", loaded.Info, err)
	}
	if err := busy.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "busy"); err != nil || loaded.Info.Busy || loaded.Head != 3 {
		t.Fatalf("after Release = %+v, %v; want idle at head 3", loaded.Info, err)
	}
	if kinds := storedKinds(t, store, "busy", 0); len(kinds) != 3 || kinds[2] != gotato.EventKindSessionReleased {
		t.Fatalf("entries = %v, want session_created, session_claimed, session_released", kinds)
	}

	var published []gotato.Event
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("idle")), "instance-a", session.WithIdle(), session.WithPublisher(func(e gotato.Event) { published = append(published, e) })); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "idle"); err != nil || loaded.Info.Busy || loaded.Head != 3 {
		t.Fatalf("an idle Record = %+v, %v; want idle at head 3", loaded.Info, err)
	}
	if len(published) != 3 || published[2].Kind() != gotato.EventKindSessionReleased || published[2].Head().Number != 3 {
		t.Fatalf("published = %v, want the three Events of the write, release last", published)
	}
}
