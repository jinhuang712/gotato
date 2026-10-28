package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func fileEntry(seq uint64, kind gotato.EventKind) session.Entry {
	return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerFact, Data: []byte(`{}`)}
}

func fileSummary(seq uint64) session.RecordInfo {
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	return session.RecordInfo{ID: "r", RecordEpoch: "e1", CreatedAt: at, UpdatedAt: at, Seq: seq}
}

// List reads a Record's summary from its last write alone. A damaged write in
// the middle of a Record (a stand-in for a large one) proves it.
func TestFileStoreListReadsOnlyTheLastWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "r", []session.Entry{fileEntry(1, gotato.EventKindSessionCreated)}, fileSummary(1)); err != nil {
		t.Fatal(err)
	}
	kinds := []gotato.EventKind{gotato.EventKindMessageCommitted, gotato.EventKindCompactionEnded, gotato.EventKindMessageCommitted}
	for i, kind := range kinds {
		seq := uint64(i + 2)
		if err := store.Append(ctx, "r", seq-1, []session.Entry{fileEntry(seq, kind)}, fileSummary(seq)); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Head != 4 || loaded.Info.Seq != 4 || len(loaded.Facts) != 2 || loaded.Facts[0].Seq != 3 {
		t.Fatalf("Load = head %d, %d facts, want head 4 and facts 3, 4", loaded.Head, len(loaded.Facts))
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "r", "*", "00000000000000000002.json"))
	if len(matches) != 1 {
		t.Fatalf("found %d files for position 2", len(matches))
	}
	if err := os.WriteFile(matches[0], []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	infos, err := store.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Seq != 4 {
		t.Fatalf("List = %+v (%v), want one Record at seq 4", infos, err)
	}
}
