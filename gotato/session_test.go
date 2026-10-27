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
	s.mu.Lock()
	block := s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
			s.mu.Lock()
			s.block = nil
			s.mu.Unlock()
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
	last    ModelRequest
}

func (m *testModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	index := m.calls
	m.calls++
	m.last = request
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

func TestSessionPromptEventsAndClose(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "hello"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := agent.Subscribe(context.Background())
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
	if got := agent.Status(); got != SessionStatusIdle {
		t.Fatalf("status after Run = %s", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := agent.Status(); got != SessionStatusClosed {
		t.Fatalf("status after Close = %s", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("closed")); !IsCode(err, ErrSessionClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}

	// Close ended the stream after the Events queued for it, so the drain is
	// deterministic. Each Run must end with exactly one RunEnded as its final
	// Event: a duplicate or late terminal Event must fail this test.
	var kinds []EventKind
	var turnEnded *TurnEnded
	terminals := map[RunID]int{}
	lastKind := map[RunID]EventKind{}
	for {
		event, nextErr := stream.Next(context.Background())
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		kinds = append(kinds, event.Kind())
		if run := event.Head().RunID; run != "" {
			lastKind[run] = event.Kind()
		}
		if ended, ok := event.(TurnEnded); ok && turnEnded == nil {
			turnEnded = &ended
		}
		if _, ok := event.(RunEnded); ok {
			terminals[event.Head().RunID]++
		}
	}
	if turnEnded == nil || turnEnded.ToolCalls != 0 || turnEnded.TextBytes != uint64(len("hello")) {
		t.Fatalf("unexpected TurnEnded = %#v", turnEnded)
	}
	if len(terminals) != 2 {
		t.Fatalf("Runs with a terminal Event = %d in %v", len(terminals), kinds)
	}
	for run, count := range terminals {
		if count != 1 {
			t.Fatalf("run_ended count = %d for run %s in %v", count, run, kinds)
		}
		if lastKind[run] != KindRunEnded {
			t.Fatalf("run %s ended with %s, want run_ended", run, lastKind[run])
		}
	}
	// The Session says it left: SessionUnloaded is the last Event of the stream.
	if len(kinds) == 0 || kinds[len(kinds)-1] != KindSessionUnloaded {
		t.Fatalf("final Event = %v", kinds)
	}
	replyStarted, textDelta := -1, -1
	for i, kind := range kinds {
		if kind == KindReplyStarted && replyStarted < 0 {
			replyStarted = i
		}
		if kind == KindTextDelta && textDelta < 0 {
			textDelta = i
		}
	}
	if replyStarted < 0 || textDelta < 0 || replyStarted > textDelta {
		t.Fatalf("reply event order = %v", kinds)
	}
	_ = stream.Close()
}

func TestSessionAbortRun(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	model := &testModel{started: started, block: block, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()

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
		if _, ok := event.(RunStarted); ok {
			runID = event.Head().RunID
		}
	}
	if err := agent.AbortRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resultCh:
		if !IsCode(err, ErrCanceled) {
			t.Fatalf("prompt error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Run did not return")
	}
}

func TestSessionSingleFlight(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	model := &testModel{started: started, block: release, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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

func TestSessionCloseWhileBusyIsBounded(t *testing.T) {
	release := make(chan struct{})
	model := &testModel{block: release, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	promptDone := make(chan error, 1)
	go func() { _, runErr := agent.Prompt(context.Background(), UserMessage("blocking")); promptDone <- runErr }()
	deadline := time.Now().Add(10 * time.Second)
	for agent.Status() != SessionStatusBusy {
		if time.Now().After(deadline) {
			t.Fatal("Session did not become Busy")
		}
		time.Sleep(time.Millisecond)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := agent.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded Close timeout, got %v", err)
	}
	if got := agent.Status(); got != SessionStatusClosing {
		t.Fatalf("status after timeout = %s", got)
	}
	close(release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	if err := agent.Close(waitCtx); err != nil {
		t.Fatal(err)
	}
	if got := agent.Status(); got != SessionStatusClosed {
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
func (t *scriptedTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
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

// A Tool Call's Signature is the adapter's: the Session stores it with the
// call and sends it back on the next Turn unchanged.
func TestToolCallSignatureIsStoredAndReplayed(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`), Signature: []byte("item-1")}}, {Kind: ModelDone, StopReason: StopToolCalls}},
		{{Kind: ModelTextDelta, Text: "final"}, {Kind: ModelDone, StopReason: StopEndTurn}},
	}}
	transcript := &recordingTranscript{}
	agent, err := NewSession(WithModel(model), WithTools(&scriptedTool{}), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("use tool")); err != nil {
		t.Fatal(err)
	}
	messages := transcript.Messages()
	if len(messages) < 2 || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("transcript = %+v", messages)
	}
	if stored := messages[1].ToolCalls[0]; stored.ID != "call-1" || string(stored.Signature) != "item-1" {
		t.Fatalf("stored call = %+v", stored)
	}
	request := model.lastRequest()
	if len(request.Messages) < 2 || len(request.Messages[1].ToolCalls) != 1 || string(request.Messages[1].ToolCalls[0].Signature) != "item-1" {
		t.Fatalf("replayed request = %+v", request.Messages)
	}
}

func TestSessionToolLoop(t *testing.T) {
	tool := &scriptedTool{}
	agent, err := NewSession(WithModel(&toolModel{}), WithTools(tool))
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

func TestSchemaIntegerTypeRejectsFraction(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"count":{"type":"integer"}},"additionalProperties":false}`)
	if err := validateToolSchema(schema, []byte(`{"count":3}`)); err != nil {
		t.Fatalf("whole number rejected for an integer schema: %v", err)
	}
	if err := validateToolSchema(schema, []byte(`{"count":1.5}`)); err == nil {
		t.Fatal("fractional value accepted for an integer schema")
	}
	if err := validateToolSchema([]byte(`{"type":"number"}`), []byte(`1.5`)); err != nil {
		t.Fatalf("fractional value rejected for a number schema: %v", err)
	}
}

func TestRuntimeErrorTypedNilTargetsDoNotPanic(t *testing.T) {
	var typedNil *RuntimeError
	if errors.Is(ErrorOf(ErrBusy, "x"), typedNil) {
		t.Fatal("a typed-nil target matched")
	}
	if IsCode(typedNil, ErrBusy) {
		t.Fatal("a typed-nil error reported a code")
	}
}

func TestForModelDeepCopiesToolResultSignature(t *testing.T) {
	signature := []byte("sig")
	message := Message{
		Role: RoleToolResult,
		ToolResult: &ToolResult{
			CallID:  "c1",
			Status:  ToolResultOK,
			Content: []ContentPart{{Kind: ContentText, Text: "x", Signature: signature}},
		},
	}
	out := ForModel([]Message{message})
	out[0].ToolResult.Content[0].Signature[0] = 'X'
	if message.ToolResult.Content[0].Signature[0] != 's' {
		t.Fatal("ForModel shares ToolResult ContentPart.Signature with the input")
	}
}
