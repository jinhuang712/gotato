package grpcadapter

import (
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// tool_result_spilled has a wire form: its size travels and, on a stream, its
// body does not.
func TestToolResultSpilledOnTheWire(t *testing.T) {
	event := gotato.ToolResultSpilled{Header: gotato.Header{SessionID: "s1", Number: 7}, CallID: "c1", ToolID: "orders.export", Bytes: 1 << 20}
	wire, err := eventOf(gotato.WithoutBody(event))
	if err != nil {
		t.Fatal(err)
	}
	got := wire.GetToolResultSpilled()
	if KindOf(wire) != string(gotato.KindToolResultSpilled) || got.GetCallId() != "c1" || got.GetToolId() != "orders.export" || got.GetBytes() != 1<<20 || got.GetBody() != "" {
		t.Fatalf("tool_result_spilled on the wire = %v (kind %q)", got, KindOf(wire))
	}
}
