package testkit_test

import (
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/testkit"
)

// The tool_search task loads Tools every way search_tools allows and calls one
// before it is loaded.
func TestToolSearchLoadsToolsEveryWay(t *testing.T) {
	s, answers := play(t, testkit.ToolSearchTask())
	var steps []string
	failed := 0
	for _, message := range s.Messages() {
		for _, call := range message.ToolCalls {
			step := call.ToolID
			if call.ToolID == gotato.SearchToolName {
				step += string(call.Arguments)
			}
			steps = append(steps, step)
		}
		if message.ToolResult != nil && message.ToolResult.Status != gotato.ToolResultOK {
			failed++
		}
	}
	want := []string{
		"orders.lookup", `search_tools{"names":["shop.invoice"]}`, "shop.invoice",
		`search_tools{"queries":["shipping parcel"]}`, "carrier.quote",
		`search_tools{"toolset":"warehouse"}`, `search_tools{"names":["warehouse.stock"]}`, "warehouse.stock",
		"warehouse.reserve", "warehouse.reserve",
		`search_tools{"names":["shop.address"]}`, "shop.address",
		"carrier.track",
	}
	if got := strings.Join(steps, "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if failed != 1 {
		t.Fatalf("%d failed Tool Results, want the one call to a Tool that was not loaded", failed)
	}
	if err := testkit.ToolSearchTask().Check(answers); err != nil {
		t.Fatal(err)
	}
}
