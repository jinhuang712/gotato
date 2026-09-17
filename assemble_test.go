package gotato

import (
	"strings"
	"testing"
)

func TestAssembleRequestLayout(t *testing.T) {
	built := ModelContext{
		SystemInstructions: "be helpful",
		System:             []Block{{Tag: "rules", Text: "- one\n- two"}},
		Messages: []Message{
			{ID: "m1", Role: RoleUser, Parts: []ContentPart{{Kind: ContentText, Text: "hi", Metadata: map[string]string{"x": "y"}}}, Usage: Usage{TotalTokens: 3}},
			{ID: "m2", Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentText, Text: "hello"}}, StopReason: StopEndTurn},
			{ID: "m3", Role: RoleUser, Parts: []ContentPart{{Kind: ContentText, Text: "now"}}},
		},
		Panel: []Block{{Tag: "time", Text: "T"}},
	}
	tools := []ToolSpec{{ID: "b", InputSchema: []byte(`{"type":"object"}`)}, {ID: "a", InputSchema: []byte(`{"type":"object"}`)}}
	request, hash := AssembleRequest(built, tools)

	if request.SystemInstructions != "be helpful\n\n<rules>\n- one\n- two\n</rules>" {
		t.Fatalf("system = %q", request.SystemInstructions)
	}
	if request.Tools[0].ID != "a" || request.Tools[1].ID != "b" {
		t.Fatalf("tools not sorted: %+v", request.Tools)
	}
	if request.Messages[0].ID != "" || request.Messages[0].Usage.TotalTokens != 0 || request.Messages[0].Parts[0].Metadata != nil || request.Messages[1].StopReason != "" {
		t.Fatalf("runtime fields leaked: %+v", request.Messages[:2])
	}
	tail := TextOf(request.Messages[2])
	if !strings.HasPrefix(tail, "now") || !strings.Contains(tail, "<panel>\n<time>T</time>\n</panel>") {
		t.Fatalf("tail = %q", tail)
	}
	if len(request.Messages[2].Parts) != 2 {
		t.Fatalf("panel must be a separate part on the tail, got %d parts", len(request.Messages[2].Parts))
	}
	if len(hash) != 16 {
		t.Fatalf("hash = %q", hash)
	}
	want := []CacheBreakpoint{{After: CacheAfterSystem}, {After: CacheAfterTools}, {After: CacheAfterMessage, Index: 1}}
	if len(request.CacheBreakpoints) != len(want) {
		t.Fatalf("breakpoints = %+v", request.CacheBreakpoints)
	}
	for i := range want {
		if request.CacheBreakpoints[i] != want[i] {
			t.Fatalf("breakpoint %d = %+v", i, request.CacheBreakpoints[i])
		}
	}
	// The original Context is untouched.
	if len(built.Messages[2].Parts) != 1 {
		t.Fatal("AssembleRequest mutated the built Context")
	}
}

func TestAssembleRequestPanelSkipsAssistantTail(t *testing.T) {
	built := ModelContext{Messages: []Message{AssistantMessage("a")}, Panel: []Block{{Tag: "t", Text: "x"}}}
	request, _ := AssembleRequest(built, nil)
	if len(request.Messages[0].Parts) != 1 {
		t.Fatal("panel must not be attached to an assistant message")
	}
	if len(request.CacheBreakpoints) != 0 {
		t.Fatalf("breakpoints for a bare single-message request = %+v", request.CacheBreakpoints)
	}
}

func TestAssembleRequestToolResultTailCarriesPanel(t *testing.T) {
	built := ModelContext{
		Messages: []Message{
			UserMessage("u"),
			{Role: RoleToolResult, ToolResult: &ToolResult{CallID: "c", Status: ToolResultOK}, Parts: []ContentPart{{Kind: ContentText, Text: "r"}}},
		},
		Panel: []Block{{Tag: "t", Text: "x"}},
	}
	request, _ := AssembleRequest(built, nil)
	if !strings.Contains(TextOf(request.Messages[1]), "<panel>") {
		t.Fatal("panel should ride on a tool_result tail so no extra user turn is created")
	}
}

func TestForModelKeepsSignatureAndToolResults(t *testing.T) {
	messages := []Message{
		{Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentReasoning, Text: "r", Signature: []byte{1}}}, ToolCalls: []ToolCall{{ID: "c", ToolID: "t", Arguments: []byte(`{}`)}}},
		{Role: RoleToolResult, ToolResult: &ToolResult{CallID: "c", Status: ToolResultOK, Executed: true, Metadata: map[string]string{"k": "v"}, Content: []ContentPart{{Kind: ContentText, Text: "ok", Metadata: map[string]string{"z": "1"}}}}},
	}
	out := ForModel(messages)
	if len(out[0].Parts[0].Signature) != 1 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("provider artifacts dropped: %+v", out[0])
	}
	if out[1].ToolResult == nil || out[1].ToolResult.CallID != "c" || out[1].ToolResult.Metadata != nil || out[1].ToolResult.Content[0].Metadata != nil {
		t.Fatalf("tool result = %+v", out[1].ToolResult)
	}
	// Mutating the output never touches the input.
	out[0].ToolCalls[0].Arguments[0] = 'x'
	if string(messages[0].ToolCalls[0].Arguments) != "{}" {
		t.Fatal("ForModel aliased the input")
	}
}
