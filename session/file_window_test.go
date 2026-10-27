package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A writer is held between the epoch check and the write of its batch while another
// process deletes the Record and creates it again under the same ID, with the
// same length. The old writer's batch must not land in the new Record: its
// link finds no directory of its own epoch.
func TestFileStoreWriterHeldAfterItsCheckCannotWriteARecordCreatedAgain(t *testing.T) {
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
	entry := func(seq uint64) session.Entry {
		kind := gotato.EventKindMessageCommitted
		if seq == 1 {
			kind = gotato.EventKindSessionCreated
		}
		return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerFact, Data: []byte(`{}`)}
	}
	summary := func(epoch string, seq uint64) session.RecordInfo {
		return session.RecordInfo{ID: "r", RecordEpoch: epoch, CreatedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), Seq: seq}
	}
	first := []session.Entry{entry(1), entry(2)}
	if err := a.Create(ctx, "r", first, summary("e1", 2)); err != nil {
		t.Fatal(err)
	}

	recreated := false
	session.SetBeforeWrite(a, func() {
		if recreated {
			return
		}
		recreated = true
		if err := b.Delete(ctx, "r"); err != nil {
			t.Error(err)
			return
		}
		if err := b.Create(ctx, "r", first, summary("e2", 2)); err != nil {
			t.Error(err)
		}
	})
	err := a.Append(ctx, "r", 2, []session.Entry{entry(3)}, summary("e1", 3))
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("an Append held across a delete and create = %v, want ErrNotFound", err)
	}
	entries, err := b.Log(ctx, "r", 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("the new Record holds %d Entries (%v), want 2", len(entries), err)
	}
	loaded, err := b.Load(ctx, "r")
	if err != nil || loaded.Info.Epoch() != "e2" || loaded.Head != 2 {
		t.Fatalf("the new Record = %+v (%v), want epoch e2 at head 2", loaded.Info, err)
	}
}

// A file the store did not write, inside a Record's directory, is an error that
// names the path; it is neither a Record nor a missing one.
func TestFileStoreStrayFileInRecordDirIsAnError(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "r", "1.json")
	if err := os.WriteFile(stray, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load(context.Background(), "r")
	if err == nil || errors.Is(err, session.ErrNotFound) || !strings.Contains(err.Error(), stray) {
		t.Fatalf("want an error naming %s, got %v", stray, err)
	}
}
