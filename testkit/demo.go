package testkit

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
)

// EchoModel answers "echo: <last user text>" and ends the Turn. It is the
// CLI's default Model.
type EchoModel struct{}

// Stream implements gotato.Model.
func (EchoModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	text := ""
	for _, v := range slices.Backward(request.Messages) {
		if v.Role == gotato.RoleUser {
			text = promptText(v)
			break
		}
	}
	return &stream{events: Text("echo: " + text)}, nil
}

// DemoModel exercises the Tool loop deterministically: when the last user
// text is "use-tool" and no tool result follows it, it calls demo.echo with
// {"value":"from-tool"}; when it is "use-big-tool" it calls demo.echo with a
// value of demoBigBytes bytes, which the Session spills; otherwise it answers
// "demo response: <text>".
type DemoModel struct{}

// demoRecap is what DemoModel answers when it is asked for a Recap (a last
// user message that text.IsRecapAsk accepts: the ask of a compaction or of a
// merge), so a compaction with the model's Recap can run in the CLI without
// a provider.
const demoRecap = "## What happened\n- the user talked with the demo model\n## Key facts\n- the demo tool returned from-tool"

// demoBigBytes is the size of the value DemoModel passes to demo.echo for the
// prompt "use-big-tool": past one page, so the result is spilled.
const demoBigBytes = 48 << 10

// DemoToolID is the Tool DemoModel calls.
const DemoToolID = "demo.echo"

// Stream implements gotato.Model.
func (DemoModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	lastUser := ""
	hasToolResult := false
	for _, message := range slices.Backward(request.Messages) {

		if message.Role == gotato.RoleToolResult {
			hasToolResult = true
		}
		if message.Role == gotato.RoleUser && lastUser == "" {
			lastUser = promptText(message)
			break
		}
	}
	if text.IsRecapAsk(lastUser) {
		return &stream{events: Text(demoRecap)}, nil
	}
	if strings.TrimSpace(lastUser) == "use-tool" && !hasToolResult {
		return &stream{events: ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: DemoToolID, Arguments: []byte(`{"value":"from-tool"}`)})}, nil
	}
	if strings.TrimSpace(lastUser) == "use-big-tool" && !hasToolResult {
		value := strings.Repeat("demo line of a large tool result\n", demoBigBytes/33+1)
		arguments, _ := json.Marshal(map[string]string{"value": value})
		return &stream{events: ToolCalls(gotato.ToolCall{ID: "call-big", ToolID: DemoToolID, Arguments: arguments})}, nil
	}
	return &stream{events: Text("demo response: " + lastUser)}, nil
}

// promptText returns the first text part of a Message: the user's prompt.
func promptText(message gotato.Message) string {
	for _, part := range message.Parts {
		if part.Kind == gotato.ContentKindText {
			return part.Text
		}
	}
	return ""
}

// DemoEchoTool is the Tool DemoModel calls: it returns its "value" argument.
func DemoEchoTool() gotato.Tool {
	tool, err := gotato.NewFuncTool(DemoToolID, "Returns the value passed to it.", func(_ context.Context, in struct {
		Value string `json:"value" description:"text to echo back"`
	}) (string, error) {
		return in.Value, nil
	})
	if err != nil {
		panic(err)
	}
	return tool
}
