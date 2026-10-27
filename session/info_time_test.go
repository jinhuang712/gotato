package session_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A RecordInfo's times are time.Time. They go through a store's JSON and come
// back as the same instants, and the epoch reads the same after the reload,
// whatever digits the time carries and in whatever zone it was written.
func TestRecordInfoTimesSurviveASaveAndLoad(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("UTC+8", 8*3600)
	created := time.Date(2026, 10, 17, 9, 30, 15, 123_456_789, zone)
	updated := created.Add(1500 * time.Millisecond)
	ended := created.Add(time.Second)
	info := session.RecordInfo{ID: "r", RecordEpoch: "e1", CreatedAt: created, UpdatedAt: updated, LastRunEnd: ended, Seq: 2}
	entries := []session.Entry{
		{Seq: 1, Kind: gotato.EventKindSessionCreated, Layer: session.LayerFact, Data: []byte(`{}`)},
		{Seq: 2, Kind: gotato.EventKindMessageCommitted, Layer: session.LayerFact, Data: []byte(`{}`)},
	}
	if err := store.Create(ctx, "r", entries, info); err != nil {
		t.Fatal(err)
	}
	want := info.RecordEpoch

	loaded, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %v, %v", listed, err)
	}
	for name, got := range map[string]session.RecordInfo{"Load": loaded.Info, "List": listed[0]} {
		if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) || !got.LastRunEnd.Equal(ended) {
			t.Errorf("%s: times %v %v %v, want %v %v %v", name, got.CreatedAt, got.UpdatedAt, got.LastRunEnd, created, updated, ended)
		}
		if got.RecordEpoch != want {
			t.Errorf("%s: epoch %q, want %q", name, got.RecordEpoch, want)
		}
	}

	// A Record no Run has ended on writes no last_run_end.
	info.LastRunEnd = time.Time{}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "last_run_end") {
		t.Errorf("a zero LastRunEnd is written: %s", data)
	}
}

// A Record's stored times and epoch are the ones the Record had.
func TestStoredInfoTimesAreTheRecordsOwn(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := session.CreateWriter(ctx, store, session.New(session.WithID("same")), "a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	before := w.Record().Info()
	loaded, err := store.Load(ctx, "same")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Info.CreatedAt.Equal(before.CreatedAt) || !loaded.Info.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("stored times %v %v, the Record's %v %v", loaded.Info.CreatedAt, loaded.Info.UpdatedAt, before.CreatedAt, before.UpdatedAt)
	}
	if loaded.Info.RecordEpoch != before.RecordEpoch {
		t.Fatalf("epoch %q after the reload, %q before", loaded.Info.RecordEpoch, before.RecordEpoch)
	}
}
