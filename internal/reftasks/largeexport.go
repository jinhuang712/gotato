package reftasks

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
)

// LargeExportID is the ID of the tool LargeExport returns.
const LargeExportID = "orders.export"

const (
	// largeExportOrders is how many orders the export holds.
	largeExportOrders = 7500
	// firstExportOrder is the number of the first order of the export.
	firstExportOrder = 81500
	// exportTotalStride steps the totals of the orders apart.
	exportTotalStride = 37
	// exportTotalSpread is the range of the totals, in cents.
	exportTotalSpread = 9000
	// exportTotalFloor is the least total, in cents.
	exportTotalFloor = 1000
	// centsPerUnit converts a total from cents.
	centsPerUnit = 100
)

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
			ID:     fmt.Sprintf("order-%05d", firstExportOrder+i),
			Status: statuses[i%len(statuses)],
			Total:  float64(exportTotalFloor+(i*exportTotalStride)%exportTotalSpread) / centsPerUnit,
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
	return gotato.ToolWithEffect(exportTool{tool}, gotato.ToolEffectRead)
}

// exportTool is a FakeTool with the export's description.
type exportTool struct{ *testkit.FakeTool }

func (t exportTool) Spec() gotato.ToolSpec {
	spec := t.FakeTool.Spec()
	spec.Description = "Exports every order of the shop as JSON."
	return spec
}
