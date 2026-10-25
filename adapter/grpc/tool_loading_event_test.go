package grpcadapter

import (
	"testing"

	gotato "github.com/jinhuang712/gotato"
)

// tool_loaded and tool_unloaded have a wire form that carries the tool and why.
func TestToolLoadedAndUnloadedOnTheWire(t *testing.T) {
	loaded, err := eventOf(gotato.ToolLoaded{Header: gotato.Header{SessionID: "s1", Number: 3}, Tool: "orders.lookup", Why: gotato.ToolLoadedQuery})
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.GetToolLoaded(); KindOf(loaded) != string(gotato.KindToolLoaded) || got.GetTool() != "orders.lookup" || got.GetWhy() != "query" {
		t.Fatalf("tool_loaded on the wire = %v (kind %q)", got, KindOf(loaded))
	}
	unloaded, err := eventOf(gotato.ToolUnloaded{Header: gotato.Header{SessionID: "s1", Number: 4}, Tool: "orders.lookup", Why: gotato.ToolUnloadedCompaction})
	if err != nil {
		t.Fatal(err)
	}
	if got := unloaded.GetToolUnloaded(); KindOf(unloaded) != string(gotato.KindToolUnloaded) || got.GetTool() != "orders.lookup" || got.GetWhy() != "compaction" {
		t.Fatalf("tool_unloaded on the wire = %v (kind %q)", got, KindOf(unloaded))
	}
}
