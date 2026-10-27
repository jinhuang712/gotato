package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/session"
)

// A Writer that released its Record can claim it again, from memory, while the
// store's head is still the Record's: the Claim keeps its Epoch, and nothing is
// loaded. Once another Instance has written, the copy is stale, and the claim
// is refused.
func TestResumeClaimsAReleasedRecordAgainWhileItsHeadIsCurrent(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	id := writer.Record().ID()
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	stale, err := writer.Stale(ctx)
	if err != nil || stale {
		t.Fatalf("Stale of a current copy = %v, %v", stale, err)
	}
	if err := writer.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if claim := writer.Record().Claim(); claim.Instance != "instance-a" || claim.Epoch != 1 {
		t.Fatalf("claim = %+v, want instance-a at epoch 1", claim)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Head != writer.Record().Head() || loaded.Info.Busy {
		t.Fatalf("store head %d busy %v, want the copy's head %d, idle", loaded.Head, loaded.Info.Busy, writer.Record().Head())
	}

	other, err := session.OpenWriter(ctx, store, id, "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if stale, err := writer.Stale(ctx); err != nil || !stale {
		t.Fatalf("Stale after another Instance wrote = %v, %v, want true", stale, err)
	}
	if err := writer.Resume(ctx); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("Resume of a stale copy = %v, want a conflict", err)
	}
	if again := writer.Resume(ctx); again == nil {
		t.Fatal("a Writer that failed resumed")
	}
}

func TestStaleOfADeletedRecordIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(), "instance-a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, writer.Record().ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Stale(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Stale of a deleted Record = %v, want not found", err)
	}
}
