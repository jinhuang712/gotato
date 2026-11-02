package record_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

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
	h := record.Header{Epoch: "e1", Info: record.Info{ID: "r", CreatedAt: created, UpdatedAt: updated}}
	entries := []record.Entry{
		{Seq: 1, Kind: gotato.EventKindSessionCreated, Layer: record.LayerFact, Data: []byte(`{}`)},
		{Seq: 2, Kind: gotato.EventKindMessageCommitted, Layer: record.LayerFact, Data: []byte(`{}`)},
	}
	if err := store.Create(ctx, "r", entries, h); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.List(ctx, session.ListQuery{})
	if err != nil || len(page.Infos) != 1 {
		t.Fatalf("List = %v, %v", page, err)
	}
	for name, got := range map[string]record.Info{"Load": loaded.Header.Info, "List": page.Infos[0]} {
		if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
			t.Errorf("%s: times %v %v, want %v %v", name, got.CreatedAt, got.UpdatedAt, created, updated)
		}
	}
	if loaded.Header.Epoch != "e1" {
		t.Errorf("Load: epoch %q, want %q", loaded.Header.Epoch, "e1")
	}
}

// The State of a Writer's write carries the last Run's end, the Run count and
// the usage, and the Record loaded from it knows them.
func TestStateTimesSurviveASaveAndLoad(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := session.CreateWriter(ctx, store, record.New(record.WithID("st")), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(ctx, []gotato.Event{
		gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)},
		gotato.RunEnded{Header: gotato.NewHeader("", "run-1", 0), Status: gotato.RunStatusCompleted, Usage: gotato.Usage{InputTokens: 7}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Release(ctx); err != nil {
		t.Fatal(err)
	}
	ended := w.Record().LastRunEnd()
	if ended.IsZero() {
		t.Fatal("the Record has no last Run end after a Run ended")
	}
	loaded, err := store.Load(ctx, "st")
	if err != nil {
		t.Fatal(err)
	}
	state, err := record.DecodeState(loaded.Header)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LastRunEnd.Equal(ended) || state.Runs != 1 || state.Usage.InputTokens != 7 || state.Running != "" || state.Busy {
		t.Fatalf("State = %+v, want the last Run end %v, one Run, 7 input tokens, nothing running, not busy", state, ended)
	}
	if loaded.Header.Info.Runs != 1 || loaded.Header.Info.Usage.InputTokens != 7 {
		t.Fatalf("Info = %+v, want the listing columns for one Run and 7 input tokens", loaded.Header.Info)
	}
	opened, err := session.Open(ctx, store, "st")
	if err != nil {
		t.Fatal(err)
	}
	if !opened.LastRunEnd().Equal(ended) {
		t.Fatalf("LastRunEnd of the loaded Record = %v, want %v", opened.LastRunEnd(), ended)
	}
}

// A Record's stored times and epoch are the ones the Record had.
func TestStoredInfoTimesAreTheRecordsOwn(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := session.CreateWriter(ctx, store, record.New(record.WithID("same")), "a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	before := w.Record().Info()
	loaded, err := store.Load(ctx, "same")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Header.Info.CreatedAt.Equal(before.CreatedAt) || !loaded.Header.Info.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("stored times %v %v, the Record's %v %v", loaded.Header.Info.CreatedAt, loaded.Header.Info.UpdatedAt, before.CreatedAt, before.UpdatedAt)
	}
	if loaded.Header.Epoch != w.Record().RecordEpoch() {
		t.Fatalf("epoch %q after the reload, %q before", loaded.Header.Epoch, w.Record().RecordEpoch())
	}
}
