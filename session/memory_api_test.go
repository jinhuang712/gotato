package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func TestMemoryStore(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	s := testkit.NewRecord("a", "b")
	if _, err := session.CreateWriter(ctx, store, s, "instance-a"); err != nil {
		t.Fatal(err)
	}
	got, err := session.Open(ctx, store, s.ID())
	if err != nil || got.Len() != 2 {
		t.Fatalf("get = %v err=%v", got, err)
	}
	// Mutating a copy opened for reading does not change the stored Record.
	_ = got.Append(gotato.UserMessage("c"))
	again, _ := session.Open(ctx, store, s.ID())
	if again.Len() != 2 {
		t.Fatalf("store aliased live session: %d", again.Len())
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
}
