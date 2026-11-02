package session

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// A caller that changes the State or the Metadata of a Load or List result, or
// of the Header it passed in, must not change what the store holds.
func TestMemoryStoreKeepsItsHeaderApart(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	h := Header{Epoch: "e", State: []byte(`{"notices":["first"]}`), Info: RecordInfo{ID: "n", Metadata: map[string]string{"k": "first"}}}
	entries := []Entry{{Seq: 1, Layer: LayerFact, Kind: gotato.EventKindSessionCreated, Data: []byte(`{}`)}}
	if err := store.Create(ctx, "n", entries, h); err != nil {
		t.Fatal(err)
	}
	h.State[2] = 'X'
	h.Info.Metadata["k"] = "changed by the caller"
	loaded, err := store.Load(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Header.State[2] = 'Y'
	loaded.Header.Info.Metadata["k"] = "changed by Load's caller"
	page, err := store.List(ctx, ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	page.Infos[0].Metadata["k"] = "changed by List's caller"
	again, err := store.Load(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(again.Header.State); got != `{"notices":["first"]}` {
		t.Fatalf("the store's State = %s", got)
	}
	if got := again.Header.Info.Metadata["k"]; got != "first" {
		t.Fatalf("the store's Metadata = %q, want %q", got, "first")
	}
}
