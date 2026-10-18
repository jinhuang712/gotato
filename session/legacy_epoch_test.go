package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A schema version 1 document is read again on every call until its first new
// write, so its epoch must be the same on every read, and the same after the
// move into a directory: it is made of the creation time.
func TestSchemaV1DocumentHasAStableEpoch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	doc := session.Document{
		SchemaVersion: 1, ID: "doc-1",
		CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		Messages: []gotato.Message{gotato.UserMessage("hi")},
	}
	if err := os.WriteFile(filepath.Join(dir, "doc-1.json"), mustJSON(t, doc), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Load(ctx, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	want := first.Summary.Epoch()
	if want == "" {
		t.Fatal("no epoch")
	}
	for range 3 {
		again, err := store.Load(ctx, "doc-1")
		if err != nil || again.Summary.Epoch() != want {
			t.Fatalf("a later Load names epoch %q (%v), want %q", again.Summary.Epoch(), err, want)
		}
	}
	events, err := session.StoredEvents(ctx, store, "doc-1", 0)
	if err != nil || events[0].(gotato.SessionCreated).RecordEpoch != want {
		t.Fatalf("session_created holds %v (%v), want %q", events[0], err, want)
	}
	writer, err := session.OpenWriter(ctx, store, "doc-1", "a")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("again"))
	moved, err := store.Load(ctx, "doc-1")
	if err != nil || moved.Summary.Epoch() != want {
		t.Fatalf("after the first write the epoch is %q (%v), want %q", moved.Summary.Epoch(), err, want)
	}
}
