package gotato

import (
	"context"
	"sync"
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

// concurrentProgressTool reports progress from several goroutines at once.
type concurrentProgressTool struct {
	workers int
	updates int
}

func (t *concurrentProgressTool) Spec() ToolSpec {
	return ToolSpec{ID: "progress", Name: "progress", InputSchema: []byte(`{"type":"object"}`)}
}

func (t *concurrentProgressTool) Execute(_ context.Context, _ ToolUse, progress ToolProgress) (ToolResult, error) {
	var wg sync.WaitGroup
	for i := 0; i < t.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < t.updates; j++ {
				progress("x")
			}
		}()
	}
	wg.Wait()
	return ToolResult{Status: ToolResultOK}, nil
}

// A Tool may report progress from any goroutine. Every report under the limit
// must be counted exactly once; with -race this also exercises the counter and
// byte-limit guards under concurrent senders.
func TestConcurrentProgressReportsAreCounted(t *testing.T) {
	const workers, perWorker = 8, 8
	want := workers * perWorker
	call := ToolCall{ID: "c1", ToolID: "progress", Arguments: []byte(`{}`)}
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelToolCall, ToolCall: &call}, {Kind: ModelDone, StopReason: StopToolCalls}},
		{{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}},
	}}
	agent, err := NewAgent(WithModel(model), WithTool(&concurrentProgressTool{workers: workers, updates: perWorker}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	events, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	updates := 0
	observed := make(chan error, 1)
	go func() {
		for {
			event, nextErr := events.Next(context.Background())
			if nextErr != nil {
				observed <- nextErr
				return
			}
			if event.Kind == EventToolExecutionUpdate {
				updates++
			}
			if event.Kind == EventAgentEnd {
				observed <- nil
				return
			}
		}
	}()

	if _, err := agent.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event stream did not reach agent_end")
	}
	if updates != want {
		t.Fatalf("progress updates = %d, want %d", updates, want)
	}
}
