package session_test

import (
	"context"
	"errors"
	"testing"

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
		kind := gotato.KindMessageCommitted
		if seq == 1 {
			kind = gotato.KindSessionCreated
		}
		return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerFact, Data: []byte(`{}`)}
	}
	summary := func(epoch string, seq uint64) session.RecordInfo {
		return session.RecordInfo{ID: "r", RecordEpoch: epoch, CreatedAt: "2026-10-17T00:00:00.000Z", UpdatedAt: "2026-10-17T00:00:00.000Z", Seq: seq}
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
