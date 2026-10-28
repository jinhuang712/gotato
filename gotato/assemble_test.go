package gotato

import (
	"testing"
)

func TestAssembleRequestLayout(t *testing.T) {
	built := ModelContext{
		SystemInstructions: "be helpful",
		System:             []Block{{Tag: "rules", Text: "- one\n- two"}},
		Messages: []Message{
			{ID: "m1", Role: RoleUser, Parts: []ContentPart{{Kind: ContentKindText, Text: "hi", Metadata: map[string]string{"x": "y"}}}, Usage: Usage{TotalTokens: 3}},
			{ID: "m2", Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentKindText, Text: "hello"}}, StopReason: StopReasonEndTurn},
			{ID: "m3", Role: RoleUser, Parts: []ContentPart{{Kind: ContentKindText, Text: "now"}}},
		},
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
	// The last Message goes as committed: nothing is appended to it.
	if tail := request.Messages[2]; TextOf(tail) != "now" || len(tail.Parts) != 1 {
		t.Fatalf("tail = %+v", tail)
	}
	if len(hash) != 16 {
		t.Fatalf("hash = %q", hash)
	}
	// The message breakpoint is on the last Message, so the whole request is
	// cached for the next Turn.
	want := []CacheBreakpoint{{After: CacheAnchorAfterSystem}, {After: CacheAnchorAfterTools}, {After: CacheAnchorAfterMessage, Index: 2}}
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

func TestAssembleRequestCachesEvenTheFirstPrompt(t *testing.T) {
	built := ModelContext{Messages: []Message{UserMessage("first")}}
	request, _ := AssembleRequest(built, nil)
	want := CacheBreakpoint{After: CacheAnchorAfterMessage, Index: 0}
	if len(request.CacheBreakpoints) != 1 || request.CacheBreakpoints[0] != want {
		t.Fatalf("breakpoints for a one-message request = %+v, want %+v", request.CacheBreakpoints, want)
	}
	if empty, _ := AssembleRequest(ModelContext{}, nil); len(empty.CacheBreakpoints) != 0 {
		t.Fatalf("breakpoints for an empty request = %+v", empty.CacheBreakpoints)
	}
}

func TestAssembleRequestLeavesAToolResultAsTheToolReturnedIt(t *testing.T) {
	built := ModelContext{
		Messages: []Message{
			UserMessage("u"),
			{Role: RoleToolResult, ToolResult: &ToolResult{CallID: "c", Status: ToolResultStatusOK}, Parts: []ContentPart{{Kind: ContentKindJSON, Text: `{"r":1}`}}},
		},
	}
	request, _ := AssembleRequest(built, nil)
	if parts := request.Messages[1].Parts; len(parts) != 1 || parts[0].Text != `{"r":1}` {
		t.Fatalf("tool result parts = %+v", parts)
	}
}

func TestForModelKeepsSignatureAndToolResults(t *testing.T) {
	messages := []Message{
		{Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentKindReasoning, Text: "r", Signature: []byte{1}}}, ToolCalls: []ToolCall{{ID: "c", ToolID: "t", Arguments: []byte(`{}`)}}},
		{Role: RoleToolResult, ToolResult: &ToolResult{CallID: "c", Status: ToolResultStatusOK, Executed: true, Metadata: map[string]string{"k": "v"}, Content: []ContentPart{{Kind: ContentKindText, Text: "ok", Metadata: map[string]string{"z": "1"}}}}},
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

func TestForModelDeepCopiesToolResultSignature(t *testing.T) {
	signature := []byte("sig")
	message := Message{
		Role: RoleToolResult,
		ToolResult: &ToolResult{
			CallID:  "c1",
			Status:  ToolResultStatusOK,
			Content: []ContentPart{{Kind: ContentKindText, Text: "x", Signature: signature}},
		},
	}
	out := ForModel([]Message{message})
	out[0].ToolResult.Content[0].Signature[0] = 'X'
	if message.ToolResult.Content[0].Signature[0] != 's' {
		t.Fatal("ForModel shares ToolResult ContentPart.Signature with the input")
	}
}

func TestForModelIsolatesPartBytes(t *testing.T) {
	data := []byte("png")
	signature := []byte("sig")
	message := Message{Role: RoleUser, Parts: []ContentPart{{Kind: ContentKindImage, Data: data, Signature: signature}}}
	out := ForModel([]Message{message})
	out[0].Parts[0].Data[0] = 'X'
	out[0].Parts[0].Signature[0] = 'X'
	if message.Parts[0].Data[0] != 'p' || message.Parts[0].Signature[0] != 's' {
		t.Fatal("ForModel shares Part bytes with the input")
	}
}
