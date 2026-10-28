package gotato

import (
	"context"
	"strings"
	"testing"
)

// bigTool returns a Result of size bytes of text.
type bigTool struct{ size int }

func (t bigTool) Spec() ToolSpec { return (&scriptedTool{}).Spec() }
func (t bigTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{Executed: true, Content: []ContentPart{{Kind: ContentKindText, Text: strings.Repeat("x", t.size)}}}, nil
}

// A Tool Result over MaxMessageBytes does not end the Run after the Tool ran:
// the Model gets a failed Result that says why.
func TestOversizedToolResultIsAFailedResultNotAFailedRun(t *testing.T) {
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1")), WithTools(bigTool{size: 5000}), WithLimits(CoreLimits{MaxMessageBytes: 1000}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	result, err := session.Prompt(context.Background(), UserMessage("go"))
	if err != nil || result.Status != RunStatusCompleted {
		t.Fatalf("status %q, err %v", result.Status, err)
	}
	var found *ToolResult
	for _, m := range session.Transcript().Messages() {
		if m.ToolResult != nil {
			found = m.ToolResult
		}
	}
	if found == nil || found.Status != ToolResultStatusFailed || !found.Executed {
		t.Fatalf("tool result = %+v, want failed and executed", found)
	}
	if !strings.Contains(found.Content[0].Text, "too large") {
		t.Fatalf("notice = %q", found.Content[0].Text)
	}
}
