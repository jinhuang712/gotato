package reftasks

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jinhuang712/gotato/testkit"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// LargeExportID is the ID of the tool LargeExport returns.
const LargeExportID = "orders.export"

const largeExportOrders = 7500

// largeExport is the export, built once: about 1 MB of indented JSON, an order
// on six lines.
var largeExport = sync.OnceValue(func() string {
	type order struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		Total  float64 `json:"total"`
		Note   string  `json:"note"`
	}
	statuses := []string{"paid", "shipped", "refunded"}
	orders := make([]order, largeExportOrders)
	for i := range orders {
		orders[i] = order{
			ID:     fmt.Sprintf("order-%05d", 81500+i),
			Status: statuses[i%len(statuses)],
			Total:  float64(1000+(i*37)%9000) / 100,
			Note:   "packed by hand and checked twice before it leaves the shelf",
		}
	}
	encoded, err := json.MarshalIndent(map[string]any{"orders": orders}, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(encoded)
})

// LargeExport is a Tool that returns a JSON export of about 1 MB, a result the
// core spills, to test spilling and read_result. It takes no argument and keeps
// no state. Its Effect is read.
func LargeExport() gotato.Tool {
	tool := testkit.NewFakeTool(LargeExportID, largeExport())
	return gotato.ToolWithEffect(exportTool{tool}, gotato.EffectRead)
}

// exportTool is a FakeTool with the export's description.
type exportTool struct{ *testkit.FakeTool }

func (t exportTool) Spec() gotato.ToolSpec {
	spec := t.FakeTool.Spec()
	spec.Description = "Exports every order of the shop as JSON."
	return spec
}
