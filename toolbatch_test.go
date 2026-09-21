package gotato

import (
	"context"
	"testing"
	"time"
)

type stalledTool struct {
	blocked <-chan struct{}
}

func (t stalledTool) Spec() ToolSpec {
	return ToolSpec{ID: "slow", Name: "slow", Description: "stalls", InputSchema: []byte(`{"type":"object"}`)}
}

func (t stalledTool) Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error) {
	<-t.blocked
	return ToolResult{Status: ToolResultOK}, nil
}

// A Tool that ignores its context must not pin the Agent goroutine: Abort has
// to settle the Run even though the worker never returns.
func TestAbortSettlesRunWithStalledTool(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	call := ToolCall{ID: "c1", ToolID: "slow", Arguments: []byte(`{}`)}
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelToolCall, ToolCall: &call}, {Kind: ModelDone, StopReason: StopToolCalls}},
		{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
	}}
	agent, err := NewAgent(WithModel(model), WithTool(stalledTool{blocked: blocked}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	events, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	resultCh := make(chan error, 1)
	go func() {
		_, promptErr := agent.Prompt(context.Background(), UserMessage("go"))
		resultCh <- promptErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		event, nextErr := events.Next(ctx)
		if nextErr != nil {
			t.Fatalf("waiting for the Tool to start: %v", nextErr)
		}
		if event.Kind == EventToolExecutionStart {
			break
		}
	}

	agent.(ControllableAgent).Abort()
	select {
	case promptErr := <-resultCh:
		if !IsCode(promptErr, ErrCancelled) {
			t.Fatalf("prompt error = %v", promptErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not settle after Abort with a stalled Tool")
	}
}
