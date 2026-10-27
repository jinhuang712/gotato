package session_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Writer of a Record that was deleted, and created again under the same ID
// at the same length, must not write into the new Record: its Entries belong
// to another log, and the new Record's RecordInfo would name the old epoch.
func TestWriterOfADeletedRecordCannotAppendToARecordCreatedAgain(t *testing.T) {
	stores := map[string]func(t *testing.T) session.Store{
		"memory": func(*testing.T) session.Store { return session.NewMemoryStore() },
		"file": func(t *testing.T) session.Store {
			store, err := session.NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return store
		},
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			old, err := session.CreateWriter(ctx, store, session.New(session.WithID("same")), "a", session.WithIdle())
			if err != nil {
				t.Fatal(err)
			}
			commit(t, old, gotato.UserMessage("one")) // a warm head for the file store
			if err := store.Delete(ctx, "same"); err != nil {
				t.Fatal(err)
			}
			fresh, err := session.CreateWriter(ctx, store, session.New(session.WithID("same")), "b", session.WithIdle())
			if err != nil {
				t.Fatal(err)
			}
			commit(t, fresh, gotato.UserMessage("one"))
			if old.Record().RecordEpoch() == fresh.Record().RecordEpoch() {
				t.Fatal("the two Records share an epoch")
			}
			before, err := store.Log(ctx, "same", 0)
			if err != nil {
				t.Fatal(err)
			}

			if err := old.Record().Append(gotato.UserMessage("two")); err != nil {
				t.Fatal(err)
			}
			_, err = old.Write(ctx, []gotato.Event{messageEvent(gotato.UserMessage("two"))})
			if !errors.Is(err, session.ErrNotFound) {
				t.Fatalf("a write of the deleted Record = %v, want ErrNotFound", err)
			}
			after, err := store.Log(ctx, "same", 0)
			if err != nil || len(after) != len(before) {
				t.Fatalf("the new Record holds %d Entries after the refused write (%v), want %d", len(after), err, len(before))
			}
			loaded, err := store.Load(ctx, "same")
			if err != nil || loaded.Info.Epoch() != fresh.Record().RecordEpoch() {
				t.Fatalf("the new Record's epoch = %q (%v), want %q", loaded.Info.Epoch(), err, fresh.Record().RecordEpoch())
			}
		})
	}
}

// Two processes on one directory: one deletes the Record and creates it again
// as a single, longer write, and the other, whose head cache still says the
// old head, must be refused instead of writing a position twice.
func TestFileStoreRefusesAPositionAnotherRecordHolds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	open := func() *session.FileStore {
		store, err := session.NewFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	a, b := open(), open()
	entry := func(seq uint64, kind gotato.EventKind, layer session.Layer) session.Entry {
		return session.Entry{Seq: seq, Kind: kind, Layer: layer, Data: []byte(`{}`)}
	}
	summary := func(epoch string, seq uint64) session.RecordInfo {
		return session.RecordInfo{ID: "r", RecordEpoch: epoch, CreatedAt: "2026-10-17T00:00:00.000Z", UpdatedAt: "2026-10-17T00:00:00.000Z", Seq: seq}
	}
	first := []session.Entry{entry(1, gotato.EventKindSessionCreated, session.LayerFact), entry(2, gotato.EventKindMessageCommitted, session.LayerFact), entry(3, gotato.EventKindMessageCommitted, session.LayerFact)}
	if err := a.Create(ctx, "r", first, summary("e1", 3)); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	second := append(append([]session.Entry(nil), first...), entry(4, gotato.EventKindMessageCommitted, session.LayerFact))
	if err := b.Create(ctx, "r", second, summary("e2", 4)); err != nil {
		t.Fatal(err)
	}
	err := a.Append(ctx, "r", 3, []session.Entry{entry(4, gotato.EventKindMessageCommitted, session.LayerFact)}, summary("e1", 4))
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("an Append of the deleted Record = %v, want ErrNotFound", err)
	}
	entries, err := os.ReadDir(dir + "/r")
	if err != nil || len(entries) != 1 {
		t.Fatalf("the Record's directory holds %d files (%v), want 1", len(entries), err)
	}
}
