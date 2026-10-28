package session

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// A caller that changes the Notices of a Load or List result, or of the
// summary it passed in, must not change what the store holds.
func TestMemoryStoreKeepsItsNoticesApart(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	info := RecordInfo{ID: "n", RecordEpoch: "e", Notices: []gotato.PendingNotice{{MessageID: "m1", Text: "first"}}}
	entries := []Entry{{Seq: 1, Layer: LayerFact, Kind: gotato.EventKindSessionCreated, Data: []byte(`{}`)}}
	if err := store.Create(ctx, "n", entries, info); err != nil {
		t.Fatal(err)
	}
	info.Notices[0].Text = "changed by the caller"
	loaded, err := store.Load(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Info.Notices[0].Text = "changed by Load's caller"
	listed, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := listed[0].Notices[0].Text; got != "first" {
		t.Fatalf("the store's Notice = %q, want %q", got, "first")
	}
}
