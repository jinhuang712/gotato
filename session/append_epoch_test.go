package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Writer of a Record that was deleted, and created again under the same ID
// at the same length, must not write into the new Record: its Entries belong
// to another log, and the new Record's Summary would name the old epoch.
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
			old, err := session.Create(ctx, store, session.New(session.WithID("same")), "a", session.WithIdle())
			if err != nil {
				t.Fatal(err)
			}
			commit(t, old, gotato.UserMessage("one")) // a warm head for the file store
			if err := store.Delete(ctx, "same"); err != nil {
				t.Fatal(err)
			}
			fresh, err := session.Create(ctx, store, session.New(session.WithID("same")), "b", session.WithIdle())
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
			if err != nil || loaded.Summary.Epoch() != fresh.Record().RecordEpoch() {
				t.Fatalf("the new Record's epoch = %q (%v), want %q", loaded.Summary.Epoch(), err, fresh.Record().RecordEpoch())
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
	summary := func(epoch string, seq uint64) session.Summary {
		return session.Summary{ID: "r", RecordEpoch: epoch, CreatedAt: "2026-10-17T00:00:00.000Z", UpdatedAt: "2026-10-17T00:00:00.000Z", Seq: seq}
	}
	first := []session.Entry{entry(1, gotato.KindSessionCreated, session.LayerFact), entry(2, gotato.KindMessageCommitted, session.LayerFact), entry(3, gotato.KindMessageCommitted, session.LayerFact)}
	if err := a.Create(ctx, "r", first, summary("e1", 3)); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	second := append(append([]session.Entry(nil), first...), entry(4, gotato.KindMessageCommitted, session.LayerFact))
	if err := b.Create(ctx, "r", second, summary("e2", 4)); err != nil {
		t.Fatal(err)
	}
	err := a.Append(ctx, "r", 3, []session.Entry{entry(4, gotato.KindMessageCommitted, session.LayerFact)}, summary("e1", 4))
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("an Append of the deleted Record = %v, want ErrNotFound", err)
	}
	entries, err := os.ReadDir(dir + "/r")
	if err != nil || len(entries) != 1 {
		t.Fatalf("the Record's directory holds %d files (%v), want 1", len(entries), err)
	}
}

// A file system may give a deleted file's inode number to a new file, as ext4
// does, so the first file of a Record created again can be, to os.SameFile,
// the file another process's head cache remembers. The cache must see that the
// file changed, by its modification time or its size, and refuse the old
// writer. The test writes the new Record's file over the old one in place,
// which keeps the inode number on any file system.
func TestFileStoreHeadCacheSeesAFileThatReusedItsInode(t *testing.T) {
	cases := map[string]struct {
		fresh string // the new Record's epoch; "e1" is the old one
		touch bool   // whether the file's modification time changes
	}{
		"modification time": {fresh: "e2", touch: true},   // the same size
		"size":              {fresh: "e22", touch: false}, // the same modification time
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			open := func(dir string) *session.FileStore {
				store, err := session.NewFileStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				return store
			}
			entry := func(seq uint64, kind gotato.EventKind) session.Entry {
				return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerFact, Data: []byte(`{}`)}
			}
			summary := func(epoch string, seq uint64) session.Summary {
				return session.Summary{ID: "r", RecordEpoch: epoch, CreatedAt: "2026-10-17T00:00:00.000Z", UpdatedAt: "2026-10-17T00:00:00.000Z", Seq: seq}
			}
			first := []session.Entry{entry(1, gotato.KindSessionCreated), entry(2, gotato.KindMessageCommitted)}
			onlyFile := func(dir string) string {
				names, err := os.ReadDir(filepath.Join(dir, "r"))
				if err != nil || len(names) != 1 {
					t.Fatalf("the Record's directory holds %d files (%v), want 1", len(names), err)
				}
				return filepath.Join(dir, "r", names[0].Name())
			}

			dir := t.TempDir()
			a := open(dir)
			if err := a.Create(ctx, "r", first, summary("e1", 2)); err != nil {
				t.Fatal(err)
			}
			path := onlyFile(dir)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			elsewhere := t.TempDir()
			if err := open(elsewhere).Create(ctx, "r", first, summary(tc.fresh, 2)); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(onlyFile(elsewhere))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			modified := before.ModTime()
			if tc.touch {
				modified = modified.Add(time.Second)
			}
			if err := os.Chtimes(path, modified, modified); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("the rewritten file is not the same file (%v)", err)
			}
			if sameSize := after.Size() == before.Size(); sameSize != tc.touch {
				t.Fatalf("the rewritten file's size is %d, was %d", after.Size(), before.Size())
			}

			err = a.Append(ctx, "r", 2, []session.Entry{entry(3, gotato.KindMessageCommitted)}, summary("e1", 3))
			if !errors.Is(err, session.ErrNotFound) {
				t.Fatalf("an Append of the old Record = %v, want ErrNotFound", err)
			}
			onlyFile(dir)
		})
	}
}
