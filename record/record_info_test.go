package record_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
)

// A Info's times are time.Time. They go through a store's JSON and come
// back as the same instants, and the epoch reads the same after the reload,
// whatever digits the time carries and in whatever zone it was written.
func TestRecordInfoTimesSurviveASaveAndLoad(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
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
	if err := st.Create(ctx, "r", entries, h); err != nil {
		t.Fatal(err)
	}

	loaded, err := st.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	page, err := st.List(ctx, store.ListQuery{})
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

// The facts of a Writer's writes carry the last Run's end, the Run count and
// the usage, and the Record loaded from them knows them.
func TestRunTotalsSurviveASaveAndLoad(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.CreateWriter(ctx, st, record.New(record.WithID("st")), "a")
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
	loaded, err := st.Load(ctx, "st")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Header.Info.Runs != 1 || loaded.Header.Info.Usage.InputTokens != 7 {
		t.Fatalf("Info = %+v, want the listing columns for one Run and 7 input tokens", loaded.Header.Info)
	}
	opened, err := store.Open(ctx, st, "st")
	if err != nil {
		t.Fatal(err)
	}
	if _, held := opened.Holder(); !opened.LastRunEnd().Equal(ended) || opened.Info().Runs != 1 || opened.Usage().InputTokens != 7 || held {
		t.Fatalf("the loaded Record: LastRunEnd %v, Runs %d, Usage %+v, held %v; want %v, one Run, 7 input tokens, not held", opened.LastRunEnd(), opened.Info().Runs, opened.Usage(), held, ended)
	}
}

// A Record's stored times and epoch are the ones the Record had.
func TestStoredInfoTimesAreTheRecordsOwn(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := store.CreateWriter(ctx, st, record.New(record.WithID("same")), "a", store.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	before := w.Record().Info()
	loaded, err := st.Load(ctx, "same")
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
