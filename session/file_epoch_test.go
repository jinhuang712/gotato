package session_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/session"
)

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
	entry := func(seq uint64, kind gotato.EventKind, layer record.Layer) record.Entry {
		return record.Entry{Seq: seq, Kind: kind, Layer: layer, Data: []byte(`{}`)}
	}
	summary := func(epoch string, seq uint64) record.Header {
		at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
		return record.Header{Epoch: epoch, Info: record.Info{ID: "r", CreatedAt: at, UpdatedAt: at, Messages: int(seq)}}
	}
	first := []record.Entry{entry(1, gotato.EventKindSessionCreated, record.LayerFact), entry(2, gotato.EventKindMessageCommitted, record.LayerFact), entry(3, gotato.EventKindMessageCommitted, record.LayerFact)}
	if err := a.Create(ctx, "r", first, summary("e1", 3)); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	second := append(append([]record.Entry(nil), first...), entry(4, gotato.EventKindMessageCommitted, record.LayerFact))
	if err := b.Create(ctx, "r", second, summary("e2", 4)); err != nil {
		t.Fatal(err)
	}
	err := a.Append(ctx, "r", 3, []record.Entry{entry(4, gotato.EventKindMessageCommitted, record.LayerFact)}, summary("e1", 4))
	if !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("an Append of the deleted Record = %v, want ErrNotFound", err)
	}
	entries, err := os.ReadDir(dir + "/r")
	if err != nil || len(entries) != 1 {
		t.Fatalf("the Record's directory holds %d files (%v), want 1", len(entries), err)
	}
}

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
	entry := func(seq uint64) record.Entry {
		kind := gotato.EventKindMessageCommitted
		if seq == 1 {
			kind = gotato.EventKindSessionCreated
		}
		return record.Entry{Seq: seq, Kind: kind, Layer: record.LayerFact, Data: []byte(`{}`)}
	}
	summary := func(epoch string, seq uint64) record.Header {
		at := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
		return record.Header{Epoch: epoch, Info: record.Info{ID: "r", CreatedAt: at, UpdatedAt: at, Messages: int(seq)}}
	}
	first := []record.Entry{entry(1), entry(2)}
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
	err := a.Append(ctx, "r", 2, []record.Entry{entry(3)}, summary("e1", 3))
	if !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("an Append held across a delete and create = %v, want ErrNotFound", err)
	}
	entries, err := b.Log(ctx, "r", 0, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("the new Record holds %d Entries (%v), want 2", len(entries), err)
	}
	loaded, err := b.Load(ctx, "r")
	if err != nil || loaded.Header.Epoch != "e2" || loaded.Seq != 2 {
		t.Fatalf("the new Record = %+v (%v), want epoch e2 at head 2", loaded.Header, err)
	}
}
