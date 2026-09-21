package gotato

import (
	"errors"
	"testing"
)

func TestRuntimeErrorMatchesByCode(t *testing.T) {
	base := ErrorOf(ErrBusy, "service: session has a run in flight")
	if !errors.Is(ErrorOf(ErrBusy, "different message"), base) {
		t.Fatal("errors.Is did not match two errors with the same code")
	}
	if errors.Is(ErrorOf(ErrInvalidState, "x"), base) {
		t.Fatal("errors.Is matched a different code")
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

func TestForModelIsolatesPartBytes(t *testing.T) {
	data := []byte("png")
	signature := []byte("sig")
	message := Message{Role: RoleUser, Parts: []ContentPart{{Kind: ContentImage, Data: data, Signature: signature}}}
	out := ForModel([]Message{message})
	out[0].Parts[0].Data[0] = 'X'
	out[0].Parts[0].Signature[0] = 'X'
	if message.Parts[0].Data[0] != 'p' || message.Parts[0].Signature[0] != 's' {
		t.Fatal("ForModel shares Part bytes with the input")
	}
}

func TestToolResultCloneIsolatesSignature(t *testing.T) {
	signature := []byte("sig")
	result := ToolResult{Status: ToolResultOK, Content: []ContentPart{{Kind: ContentText, Text: "x", Signature: signature}}}
	clone := result.Clone()
	clone.Content[0].Signature[0] = 'X'
	if result.Content[0].Signature[0] != 's' {
		t.Fatal("ToolResult.Clone shares ContentPart.Signature")
	}
}
