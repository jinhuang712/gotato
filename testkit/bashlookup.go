package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// The bash_lookup task (FEATURES.md G-F36, GOALS.md G-G09 on a Session whose
// model queries its own Record): a tool returns a JSON export of about 1 MB,
// which the core spills, and the model finds one line with grep and one field
// with jq in the file /results/<call_id> instead of reading the pages.
//
// The bash tool is built by adapter/bash/bashtool, a module that links the
// engine, which the root module cannot. So the task is a constructor,
// BashLookupTask, that takes the tool's installer, and it plays in a module
// that has it.
const (
	// BashLookupName is the task's name in reports.
	BashLookupName = "bash_lookup"
	// BashLookupExportID is the ID of the tool that returns the export.
	BashLookupExportID = "orders.export"
	// BashLookupOrder is the order the prompt asks about.
	BashLookupOrder = "order-88501"
	// BashLookupCalls are the IDs of the model's three calls, in order, so a
	// check can name the files /results/<call_id> they made.
	BashLookupExportCall = gotato.ToolCallID("export-1")
	BashLookupGrepCall   = gotato.ToolCallID("grep-1")
	BashLookupJQCall     = gotato.ToolCallID("jq-1")
)

// BashLookupInstruction is the instruction of the bash_lookup task.
const BashLookupInstruction = "You answer questions about the orders of a shop. Export them with orders.export when asked. " +
	"The export is large: find what you need in it with the bash tool. Answer in one short sentence."

// BashLookupPrompt is the task's one prompt.
const BashLookupPrompt = "Export the orders, then tell me on which line of the export " + BashLookupOrder + " is, and what its total is."

const bashLookupOrders = 7500

// bashLookupExport is the export, built once: about 1 MB of indented JSON, an
// order on six lines.
var bashLookupExport = sync.OnceValue(func() string {
	type order struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		Total  float64 `json:"total"`
		Note   string  `json:"note"`
	}
	statuses := []string{"paid", "shipped", "refunded"}
	orders := make([]order, bashLookupOrders)
	for i := range orders {
		orders[i] = order{
			ID:     fmt.Sprintf("order-%05d", 81500+i),
			Status: statuses[i%len(statuses)],
			Total:  bashLookupTotalOf(i),
			Note:   "packed by hand and checked twice before it leaves the shelf",
		}
	}
	encoded, err := json.MarshalIndent(map[string]any{"orders": orders}, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(encoded)
})

func bashLookupTotalOf(i int) float64 { return float64(1000+(i*37)%9000) / 100 }

// BashLookupTotal is the total of BashLookupOrder, as jq -r writes it.
func BashLookupTotal() string {
	return strconv.FormatFloat(bashLookupTotalOf(88501-81500), 'f', -1, 64)
}

// BashLookupExportBody is the text orders.export returns.
func BashLookupExportBody() string { return bashLookupExport() }

// BashLookupExport is the Tool of the task that returns the export. It takes no
// argument and keeps no state. Its Effect is read.
func BashLookupExport() gotato.Tool {
	tool := NewFakeTool(BashLookupExportID, bashLookupExport())
	tool.spec.Description = "Exports every order of the shop as JSON."
	return gotato.ToolWithEffect(tool, gotato.EffectRead)
}

// BashLookupTask is the bash_lookup task: one prompt, in which the Model calls
// orders.export, whose 1 MB result is spilled, then finds the line of an order
// with grep and its total with jq, through the bash tool that install builds
// for the Session, and answers in a sentence. install is
// bashtool.Tools() from adapter/bash/bashtool.
func BashLookupTask(install func(*session.Record, session.EntryLog) []gotato.Tool) Task {
	return Task{
		Name:         BashLookupName,
		Instruction:  BashLookupInstruction,
		Prompts:      []string{BashLookupPrompt},
		Tools:        []gotato.Tool{BashLookupExport()},
		SessionTools: []func(*session.Record, session.EntryLog) []gotato.Tool{install},
		Model:        bashLookupModel{},
		Check: func(answers []string) error {
			if len(answers) != 1 {
				return fmt.Errorf("got %d answers, want 1", len(answers))
			}
			answer := answers[0]
			if len(answer) > 200 {
				return fmt.Errorf("the answer is %d bytes, want a sentence", len(answer))
			}
			for _, want := range []string{BashLookupOrder, BashLookupTotal()} {
				if !strings.Contains(answer, want) {
					return fmt.Errorf("the answer %q does not hold %q", answer, want)
				}
			}
			return nil
		},
	}
}

// bashLookupModel plays the task from the request alone: the Messages of the
// Run say what it has done.
type bashLookupModel struct{}

// Stream implements gotato.Model.
func (bashLookupModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	return &stream{events: playBashLookup(request)}, nil
}

// BashLookupGrepCommand and BashLookupJQCommand are the commands the Model runs.
func BashLookupGrepCommand() string {
	return fmt.Sprintf("grep -n '\"id\": \"%s\"' /results/%s", BashLookupOrder, BashLookupExportCall)
}

func BashLookupJQCommand() string {
	return fmt.Sprintf("jq -r '.orders[] | select(.id == \"%s\") | .total' /results/%s", BashLookupOrder, BashLookupExportCall)
}

func playBashLookup(request gotato.ModelRequest) Script {
	var run []gotato.Message
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == gotato.RoleUser {
			run = request.Messages[i+1:]
			break
		}
	}
	results := map[gotato.ToolCallID]string{}
	for _, message := range run {
		if message.ToolResult != nil {
			results[message.ToolResult.CallID] = resultText(*message.ToolResult)
		}
	}
	bash := func(id gotato.ToolCallID, command string) Script {
		encoded, _ := json.Marshal(map[string]string{"command": command})
		return ToolCalls(gotato.ToolCall{ID: id, ToolID: "bash", Arguments: encoded})
	}
	if _, ok := results[BashLookupExportCall]; !ok {
		return ToolCalls(gotato.ToolCall{ID: BashLookupExportCall, ToolID: BashLookupExportID, Arguments: []byte(`{}`)})
	}
	grep, ok := results[BashLookupGrepCall]
	if !ok {
		return bash(BashLookupGrepCall, BashLookupGrepCommand())
	}
	total, ok := results[BashLookupJQCall]
	if !ok {
		return bash(BashLookupJQCall, BashLookupJQCommand())
	}
	line, _, _ := strings.Cut(strings.TrimSpace(grep), ":")
	return Text(fmt.Sprintf("%s is on line %s of the export, and its total is %s.", BashLookupOrder, line, strings.TrimSpace(total)))
}
