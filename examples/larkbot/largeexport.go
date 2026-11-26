// The export tool of the cluster scenarios: a tool whose result is about 1 MB, which the core spills.
// A small copy of the one in the Gotato test tools, so that this application does not import them.

package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// exportToolID is the ID of the tool largeExportTool returns.
const exportToolID = "orders.export"

// The export's size and the numbers its orders are made of.
const (
	largeExportOrders = 7500
	firstOrderNumber  = 81500 // the number of the first order
	totalBaseCents    = 1000  // the lowest total, in cents
	totalStepCents    = 37    // how much the total grows from one order to the next
	totalSpanCents    = 9000  // how many cents the totals go up before they start over
	centsPerUnit      = 100
)

// order is one order of the export.
type order struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Total  float64 `json:"total"`
	Note   string  `json:"note"`
}

// orderAt is the i-th order of the export.
func orderAt(i int) order {
	statuses := []string{"paid", "shipped", "refunded"}
	return order{
		ID:     fmt.Sprintf("order-%05d", firstOrderNumber+i),
		Status: statuses[i%len(statuses)],
		Total:  float64(totalBaseCents+(i*totalStepCents)%totalSpanCents) / centsPerUnit,
		Note:   "packed by hand and checked twice before it leaves the shelf",
	}
}

// largeExport is the export, built once: about 1 MB of indented JSON, an order
// on six lines.
var largeExport = sync.OnceValue(func() string {
	orders := make([]order, largeExportOrders)
	for i := range orders {
		orders[i] = orderAt(i)
	}
	encoded, err := json.MarshalIndent(map[string]any{"orders": orders}, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(encoded)
})

// largeExportTool is a Tool that returns a JSON export of about 1 MB, a result the
// core spills, to test spilling and read_result. It takes no argument and keeps
// no state. Its Effect is read.
func largeExportTool() gotato.Tool {
	tool := testkit.NewFakeTool(exportToolID, largeExport())
	return gotato.ToolWithEffect(exportTool{tool}, gotato.ToolEffectRead)
}

// exportTool is a FakeTool with the export's description.
type exportTool struct{ *testkit.FakeTool }

func (t exportTool) Spec() gotato.ToolSpec {
	spec := t.FakeTool.Spec()
	spec.Description = "Exports every order of the shop as JSON."
	return spec
}
