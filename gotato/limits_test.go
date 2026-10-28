package gotato

import (
	"context"
	"strings"
	"testing"
)

// A zero field of CoreLimits is its default, so a caller sets only what it
// means to change: a Session still admits a Message, and a Tool's progress
// still reaches the Run.
func TestZeroLimitFieldsKeepTheirDefaults(t *testing.T) {
	call := ToolCall{ID: "c1", ToolID: "progress", Arguments: []byte(`{}`)}
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelEventKindToolCall, ToolCall: &call}, {Kind: ModelEventKindDone, StopReason: StopReasonToolCalls}},
		{{Kind: ModelEventKindTextDelta, Text: "ok"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}},
	}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(&concurrentProgressTool{workers: 1, updates: 3}), WithLimits(CoreLimits{MaxParallelTools: 4}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatalf("prompt with one field set failed: %v", err)
	}
	progress := 0
	for _, kind := range seen.kinds() {
		if kind == EventKindToolProgress {
			progress++
		}
	}
	if progress != 3 {
		t.Fatalf("progress events = %d, want 3 (kinds %v)", progress, seen.kinds())
	}
	if err := agent.FollowUp(context.Background(), UserMessage("more")); err != nil {
		t.Fatalf("FollowUp with one field set: %v", err)
	}
	if _, err := agent.Notice(context.Background(), "k", "a notice"); err != nil {
		t.Fatalf("Notice with one field set: %v", err)
	}
}

// A Context that starts with rendered Segments counts them toward MaxMessages,
// as a load reads them, so the default leaves room for a long Session.
func TestDefaultMaxMessagesLeavesRoomForSegments(t *testing.T) {
	history := &memoryTranscript{}
	for range 1500 {
		history.messages = append(history.messages, UserMessage("earlier"))
	}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: "ok"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTranscript(history))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatalf("a Session with 1,500 Messages was refused by the default limits: %v", err)
	}
}

func TestTranscriptByteLimitTrackedIncrementally(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTranscriptBytes = 400
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindTextDelta, Text: strings.Repeat("x", 100)}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	var lastErr error
	for i := 0; i < 10 && lastErr == nil; i++ {
		_, lastErr = agent.Prompt(context.Background(), UserMessage("hi"))
	}
	if !IsCode(lastErr, CodeLimitExceeded) {
		t.Fatalf("expected limit_exceeded, got %v", lastErr)
	}
}
