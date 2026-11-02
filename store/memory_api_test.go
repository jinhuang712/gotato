package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

func TestMemoryStore(t *testing.T) {
	st := store.NewMemoryStore()
	ctx := context.Background()
	s := testkit.NewRecord("a", "b")
	if _, err := store.CreateWriter(ctx, st, s, "instance-a"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Open(ctx, st, s.ID())
	if err != nil || got.Len() != 2 {
		t.Fatalf("get = %v err=%v", got, err)
	}
	// Mutating a copy opened for reading does not change the stored Record.
	_ = got.Append(gotato.UserMessage("c"))
	again, _ := store.Open(ctx, st, s.ID())
	if again.Len() != 2 {
		t.Fatalf("store aliased live session: %d", again.Len())
	}
	if err := st.Delete(ctx, "missing"); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
}
