package gotato

import (
	"encoding/json"
	"testing"
)

// A ToolUse describes a call before it runs, so it carries no outcome. The
// outcome is the ToolResult that a PostToolUse extension receives.
func TestToolUseCarriesNoOutcome(t *testing.T) {
	encoded, err := json.Marshal(ToolUse{CallID: "call-1", ToolID: "demo.echo"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"executed", "result"} {
		if _, ok := fields[key]; ok {
			t.Errorf("ToolUse JSON has %q: %s", key, encoded)
		}
	}
}

func TestMessageCloneIsolatesToolCallArguments(t *testing.T) {
	arguments := []byte(`{"a":1}`)
	message := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", ToolID: "t", Arguments: arguments}}}
	clone := message.Clone()
	clone.ToolCalls[0].Arguments[0] = 'X'
	if message.ToolCalls[0].Arguments[0] != '{' {
		t.Fatal("Message.Clone shares ToolCall.Arguments with the original")
	}
}

func TestToolResultCloneIsolatesSignature(t *testing.T) {
	signature := []byte("sig")
	result := ToolResult{Status: ToolResultStatusOK, Content: []ContentPart{{Kind: ContentKindText, Text: "x", Signature: signature}}}
	clone := result.Clone()
	clone.Content[0].Signature[0] = 'X'
	if result.Content[0].Signature[0] != 's' {
		t.Fatal("ToolResult.Clone shares ContentPart.Signature")
	}
}

func TestRunResultCloneIsolatesErrorDetails(t *testing.T) {
	result := RunResult{
		Status: RunStatusFailed,
		Error:  &RuntimeError{Code: CodeToolExecutionFailure, Message: "boom", Details: map[string]string{"tool": "echo"}},
	}
	clone := result.Clone()
	clone.Error.Details["tool"] = "changed"
	if result.Error.Details["tool"] != "echo" {
		t.Fatal("RunResult.Clone shares RuntimeError.Details with the original")
	}
}

// TextOf is what a message says: its text Parts. Reasoning is the model's
// thinking and must not leak into a final answer, a summary, or a replayed
// content field; ReasoningOf returns it separately.
func TestTextOfExcludesReasoning(t *testing.T) {
	m := Message{Role: RoleAssistant, Parts: []ContentPart{
		{Kind: ContentKindReasoning, Text: "let me think"},
		{Kind: ContentKindText, Text: "the answer"},
		{Kind: ContentKindReasoning, Text: " again"},
	}}
	if got := TextOf(m); got != "the answer" {
		t.Fatalf("TextOf = %q", got)
	}
	if got := ReasoningOf(m); got != "let me think again" {
		t.Fatalf("ReasoningOf = %q", got)
	}
}
