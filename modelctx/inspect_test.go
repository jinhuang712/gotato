package modelctx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
)

// toolConversation builds: user, assistant(tool_call), tool_result, assistant,
// user, assistant — six Messages with a tool exchange in the middle.
func toolConversation() []gotato.Message {
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}
	call.StopReason = gotato.StopReasonToolCalls
	result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusOK}, Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "r"}}}
	messages := []gotato.Message{
		gotato.UserMessage("u1"), call, result, gotato.AssistantMessage("a1"),
		gotato.UserMessage("u2"), gotato.AssistantMessage("a2"),
	}
	for i := range messages {
		messages[i].ID = gotato.MessageID("m" + string(rune('1'+i)))
	}
	return messages
}

func snapshot(messages []gotato.Message) gotato.ContextSnapshot {
	return gotato.ContextSnapshot{SystemInstructions: "sys", Messages: messages}
}

func TestPrefixHashStableAcrossAppendOnlyTurns(t *testing.T) {
	messages := toolConversation()
	builder := gotato.FullHistoryContext()
	tools := []gotato.ToolSpec{{ID: "t", InputSchema: []byte(`{"type":"object"}`)}}
	first, _ := modelctx.Inspect(context.Background(), builder, snapshot(messages), tools)
	// Next Turn: the tail Message changed (a new assistant answer appended and
	// a new user prompt). Everything before the new tail is identical.
	next := append(append([]gotato.Message{}, messages...), gotato.UserMessage("u3"))
	second, _ := modelctx.Inspect(context.Background(), builder, snapshot(next), tools)
	if first.PrefixHash == second.PrefixHash {
		t.Fatal("prefix must change when a message is appended before the tail")
	}
	// Runtime fields never reach the prompt: IDs and usage differ, hash equal.
	altered := make([]gotato.Message, len(next))
	for i, message := range next {
		altered[i] = message.Clone()
		altered[i].ID = gotato.MessageID("other-" + string(rune('a'+i)))
		altered[i].Usage = gotato.Usage{TotalTokens: uint64(i)}
	}
	fourth, _ := modelctx.Inspect(context.Background(), builder, snapshot(altered), tools)
	if fourth.PrefixHash != second.PrefixHash {
		t.Fatal("runtime fields perturbed the prompt bytes")
	}
	wantBreakpoints := []gotato.CacheBreakpoint{{After: gotato.CacheAnchorAfterSystem}, {After: gotato.CacheAnchorAfterTools}, {After: gotato.CacheAnchorAfterMessage, Index: len(next) - 1}}
	if len(second.Request.CacheBreakpoints) != 3 {
		t.Fatalf("breakpoints = %+v", second.Request.CacheBreakpoints)
	}
	for i, want := range wantBreakpoints {
		if second.Request.CacheBreakpoints[i] != want {
			t.Fatalf("breakpoint %d = %+v, want %+v", i, second.Request.CacheBreakpoints[i], want)
		}
	}
}

func (nilStreamModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, nil
}

func (failingRecap) Name() string { return "failing" }
func (failingRecap) WriteRecap(context.Context, modelctx.RecapRequest) (gotato.Message, error) {
	return gotato.Message{}, errors.New("recap writer down")
}
