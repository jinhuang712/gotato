package gotato

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type testStream struct {
	mu     sync.Mutex
	events []ModelEvent
	index  int
	block  <-chan struct{}
}

func (s *testStream) Recv(ctx context.Context) (ModelEvent, error) {
	if s.block != nil {
		select {
		case <-s.block:
			s.block = nil
		case <-ctx.Done():
			return ModelEvent{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index >= len(s.events) {
		return ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}
func (s *testStream) Close() error { return nil }

type testModel struct {
	mu      sync.Mutex
	calls   int
	scripts [][]ModelEvent
	started chan struct{}
	block   <-chan struct{}
}

func (m *testModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	index := m.calls
	m.calls++
	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	var events []ModelEvent
	if len(m.scripts) > 0 {
		events = m.scripts[min(index, len(m.scripts)-1)]
	}
	block := m.block
	m.mu.Unlock()
	return &testStream{events: events, block: block}, nil
}

func TestAgentPromptEventsAndClose(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "hello"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Prompt(context.Background(), UserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted || TextOf(*result.FinalMessage) != "hello" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Metrics.Turns != 1 || result.Metrics.TextBytes != uint64(len("hello")) {
		t.Fatalf("run metrics = %+v", result.Metrics)
	}
	var kinds []EventKind
	var turnSummary map[string]any
	for {
		event, nextErr := stream.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		kinds = append(kinds, event.Kind)
		if event.Kind == EventTurnEnd {
			var ok bool
			turnSummary, ok = event.Payload["summary"].(map[string]any)
			if !ok {
				t.Fatalf("turn summary = %#v", event.Payload["summary"])
			}
		}
		if event.Kind == EventAgentEnd {
			break
		}
	}
	if turnSummary == nil || turnSummary["tool_calls"] != 0 {
		t.Fatalf("unexpected turn summary = %#v", turnSummary)
	}
	if len(kinds) == 0 || kinds[len(kinds)-1] != EventAgentEnd {
		t.Fatalf("events did not settle: %v", kinds)
	}
	messageStart, messageUpdate := -1, -1
	for i, kind := range kinds {
		if kind == EventMessageStart && messageStart < 0 {
			messageStart = i
		}
		if kind == EventMessageUpdate && messageUpdate < 0 {
			messageUpdate = i
		}
	}
	if messageStart < 0 || messageUpdate < 0 || messageStart > messageUpdate {
		t.Fatalf("message event order = %v", kinds)
	}
	if got := agent.(interface{ Status() AgentStatus }).Status(); got != AgentIdle {
		t.Fatalf("status after Run = %s", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := agent.(interface{ Status() AgentStatus }).Status(); got != AgentClosed {
		t.Fatalf("status after Close = %s", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("closed")); !IsCode(err, ErrAgentClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}
	_ = stream.Close()
}



func TestAgentCancelRun(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	model := &testModel{started: started, block: block, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
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
		_, promptErr := agent.Prompt(context.Background(), UserMessage("cancel me"))
		resultCh <- promptErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Model was not called")
	}
	var runID RunID
	for runID == "" {
		event, nextErr := events.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if event.Kind == EventAgentStart {
			runID = event.RunID
		}
	}
	canceler, ok := agent.(RunCanceler)
	if !ok {
		t.Fatal("Agent does not support Run cancellation")
	}
	if err := canceler.CancelRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resultCh:
		if !IsCode(err, ErrCancelled) {
			t.Fatalf("prompt error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Run did not return")
	}
}

func TestAgentSingleFlight(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	model := &testModel{started: started, block: release, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	first := make(chan error, 1)
	go func() { _, runErr := agent.Prompt(context.Background(), UserMessage("first")); first <- runErr }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Model was not called")
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("second")); !IsCode(err, ErrBusy) {
		t.Fatalf("expected busy, got %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestAgentCloseWhileBusyIsBounded(t *testing.T) {
	release := make(chan struct{})
	model := &testModel{block: release, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	promptDone := make(chan error, 1)
	go func() { _, runErr := agent.Prompt(context.Background(), UserMessage("blocking")); promptDone <- runErr }()
	deadline := time.Now().Add(time.Second)
	for agent.(interface{ Status() AgentStatus }).Status() != AgentBusy {
		if time.Now().After(deadline) {
			t.Fatal("Agent did not become Busy")
		}
		time.Sleep(time.Millisecond)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := agent.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded Close timeout, got %v", err)
	}
	if got := agent.(interface{ Status() AgentStatus }).Status(); got != AgentClosing {
		t.Fatalf("status after timeout = %s", got)
	}
	close(release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := agent.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
	if got := agent.(interface{ Status() AgentStatus }).Status(); got != AgentClosed {
		t.Fatalf("status after close = %s", got)
	}
}

type scriptedTool struct {
	calls int
	mu    sync.Mutex
}

func (t *scriptedTool) Spec() ToolSpec {
	return ToolSpec{ID: "demo.echo", InputSchema: []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`)}
}
func (t *scriptedTool) Execute(ctx context.Context, use ToolUse, progress ToolProgress) (ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: "tool-ok"}}}, nil
}

type toolModel struct {
	calls int
	mu    sync.Mutex
}

func (m *toolModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	call := m.calls
	m.calls++
	m.mu.Unlock()
	if call == 0 {
		return &testStream{events: []ModelEvent{{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}}, {Kind: ModelDone, StopReason: StopToolCalls}}}, nil
	}
	return &testStream{events: []ModelEvent{{Kind: ModelTextDelta, Text: "final"}, {Kind: ModelDone, StopReason: StopEndTurn}}}, nil
}

func TestAgentToolLoop(t *testing.T) {
	tool := &scriptedTool{}
	agent, err := NewAgent(WithModel(&toolModel{}), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	if TextOf(*result.FinalMessage) != "final" {
		t.Fatalf("unexpected final message: %+v", result.FinalMessage)
	}
	tool.mu.Lock()
	calls := tool.calls
	tool.mu.Unlock()
	if calls != 1 {
		t.Fatalf("tool calls = %d", calls)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
