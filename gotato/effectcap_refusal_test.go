package gotato_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// A call to a tool the Effect cap hides says that the tool exists but this
// Session does not allow it, and keeps the tool_resolution_failure code. A
// name that no tool carries stays an unknown tool.
func TestEffectCapRefusalNamesTheCap(t *testing.T) {
	ctx := context.Background()
	danger := testkit.NewFakeTool("delete_file", "deleted")
	model := testkit.NewFakeModel(
		testkit.ToolCalls(
			gotato.ToolCall{ID: "capped", ToolID: "delete_file", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "typo", ToolID: "delete_fiel", Arguments: []byte(`{}`)},
		),
		testkit.Text("done"),
	)
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model),
		gotato.WithTools(gotato.ToolWithEffect(danger, gotato.ToolEffectDestructive)),
		gotato.WithMaxEffect(gotato.ToolEffectRead))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(ctx) }()
	if result, err := agent.Prompt(ctx, gotato.UserMessage("go")); err != nil || result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Prompt = %+v, %v", result, err)
	}
	results := toolResultsByCall(model.Requests()[1])
	capped, typo := results["capped"], results["typo"]
	if capped.Status != gotato.ToolResultStatusFailed || capped.Executed || danger.Calls() != 0 {
		t.Fatalf("capped result = %+v, calls %d", capped, danger.Calls())
	}
	if !strings.Contains(capped.SafeError, string(gotato.CodeToolResolutionFailure)) || !strings.Contains(capped.SafeError, "not allowed in this Session") || strings.Contains(capped.SafeError, "unknown tool") {
		t.Errorf("capped SafeError = %q, want the code and that the tool is not allowed here", capped.SafeError)
	}
	if !strings.Contains(typo.SafeError, "unknown tool") {
		t.Errorf("typo SafeError = %q, want an unknown tool", typo.SafeError)
	}
}
