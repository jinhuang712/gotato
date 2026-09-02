package gotato

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingModel records the transcript it received on every call so a test can
// prove exactly which Turn a control Message reached the Model.
type recordingModel struct {
	mu       sync.Mutex
	requests [][]Message
	offered  [][]ToolSpec
	scripts  [][]ModelEvent
	started  chan int
	gate     chan struct{}
}

func (m *recordingModel) Stream(ctx context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	index := len(m.requests)
	m.requests = append(m.requests, cloneMessages(request.Messages))
	m.offered = append(m.offered, cloneToolSpecs(request.Tools))
	events := m.scripts[min(index, len(m.scripts)-1)]
	m.mu.Unlock()
	if m.started != nil {
		m.started <- index
	}
	if m.gate != nil {
		<-m.gate
	}
	return &testStream{events: events}, nil
}

func (m *recordingModel) transcript(call int) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call >= len(m.requests) {
		return nil
	}
	return m.requests[call]
}

func (m *recordingModel) toolIDs(call int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call >= len(m.offered) {
		return nil
	}
	ids := make([]string, 0, len(m.offered[call]))
	for _, spec := range m.offered[call] {
		ids = append(ids, spec.ID)
	}
	return ids
}

func (m *recordingModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func containsUserText(messages []Message, text string) bool {
	for _, message := range messages {
		if message.Role == RoleUser && strings.Contains(TextOf(message), text) {
			return true
		}
	}
	return false
}

func toolCallScript() []ModelEvent {
	return []ModelEvent{
		{Kind: ModelToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}},
		{Kind: ModelDone, StopReason: StopToolCalls},
	}
}

func finalScript(text string) []ModelEvent {
	return []ModelEvent{{Kind: ModelTextDelta, Text: text}, {Kind: ModelDone, StopReason: StopEndTurn}}
}

func TestContinueAppendsNoUserMessage(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("first"), finalScript("second")}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())

	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	controllable, ok := agent.(ControllableAgent)
	if !ok {
		t.Fatal("Agent does not expose control commands")
	}
	if _, err := controllable.Continue(context.Background()); !IsCode(err, ErrInvalidState) {
		t.Fatalf("Continue after an assistant Message = %v", err)
	}

	if err := controllable.FollowUp(UserMessage("carry on")); err != nil {
		t.Fatal(err)
	}
	// The follow-up settles inside its own Run, leaving the transcript ending
	// in an assistant Message again, so a second Continue must still refuse.
	if _, err := agent.Prompt(context.Background(), UserMessage("more")); err != nil {
		t.Fatal(err)
	}
}



func TestSteerIsConsumedAtTheNextTurnBoundary(t *testing.T) {
	tool := &scriptedTool{}
	model := &recordingModel{
		scripts: [][]ModelEvent{toolCallScript(), finalScript("done")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewAgent(WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	controllable := agent.(ControllableAgent)

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("use tool"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()

	waitForCall(t, model.started, 0)
	if err := controllable.Steer(UserMessage("prefer metric units")); err != nil {
		t.Fatal(err)
	}
	// The Steer must not reach the Model call already in flight.
	if containsUserText(model.transcript(0), "prefer metric units") {
		t.Fatal("Steer reached the Model call in flight")
	}
	model.gate <- struct{}{}

	waitForCall(t, model.started, 1)
	if !containsUserText(model.transcript(1), "prefer metric units") {
		t.Fatalf("Steer missing from the next Turn: %+v", model.transcript(1))
	}
	model.gate <- struct{}{}

	select {
	case result := <-done:
		if result.Status != RunCompleted {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steered Run did not settle")
	}
	if model.calls() != 2 {
		t.Fatalf("Model calls = %d", model.calls())
	}
}

func TestSteerAtSettlementKeepsTheRunGoing(t *testing.T) {
	model := &recordingModel{
		scripts: [][]ModelEvent{finalScript("first"), finalScript("second")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	controllable := agent.(ControllableAgent)
	events, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("hello"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()

	waitForCall(t, model.started, 0)
	if err := controllable.Steer(UserMessage("keep going")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}
	waitForCall(t, model.started, 1)
	model.gate <- struct{}{}

	var result RunResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("steered Run did not settle")
	}
	if TextOf(*result.FinalMessage) != "second" {
		t.Fatalf("final message = %q", TextOf(*result.FinalMessage))
	}

	terminals, steered := 0, 0
	for {
		event, nextErr := events.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if event.Kind == EventMessageEnd && event.Payload["source"] == "steer" {
			steered++
		}
		if event.Kind == EventAgentEnd {
			terminals++
			break
		}
	}
	if terminals != 1 || steered != 1 {
		t.Fatalf("terminal events = %d, steer commits = %d", terminals, steered)
	}
}

func TestFollowUpWaitsForSettlement(t *testing.T) {
	tool := &scriptedTool{}
	model := &recordingModel{
		scripts: [][]ModelEvent{toolCallScript(), finalScript("done"), finalScript("after follow-up")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewAgent(WithModel(model), WithTool(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	controllable := agent.(ControllableAgent)

	done := make(chan RunResult, 1)
	go func() {
		result, runErr := agent.Prompt(context.Background(), UserMessage("use tool"))
		if runErr != nil {
			t.Error(runErr)
		}
		done <- result
	}()

	waitForCall(t, model.started, 0)
	if err := controllable.FollowUp(UserMessage("and then summarize")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}

	waitForCall(t, model.started, 1)
	if containsUserText(model.transcript(1), "and then summarize") {
		t.Fatal("FollowUp was consumed at a Turn boundary instead of settlement")
	}
	model.gate <- struct{}{}

	waitForCall(t, model.started, 2)
	if !containsUserText(model.transcript(2), "and then summarize") {
		t.Fatalf("FollowUp missing after settlement: %+v", model.transcript(2))
	}
	model.gate <- struct{}{}

	select {
	case result := <-done:
		if TextOf(*result.FinalMessage) != "after follow-up" {
			t.Fatalf("final message = %q", TextOf(*result.FinalMessage))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run with a follow-up did not settle")
	}
}

func TestControlBuffersAreBounded(t *testing.T) {
	limits := defaultLimits()
	limits.MaxFollowUpMessages = 1
	limits.MaxSteerMessages = 0
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	controllable := agent.(ControllableAgent)

	if err := controllable.FollowUp(UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := controllable.FollowUp(UserMessage("two")); !IsCode(err, ErrLimitExceeded) {
		t.Fatalf("full follow-up buffer = %v", err)
	}
	if err := controllable.Steer(UserMessage("nope")); !IsCode(err, ErrLimitExceeded) {
		t.Fatalf("disabled Steer = %v", err)
	}
	if err := controllable.Steer(Message{Role: RoleAssistant}); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("non-user control message = %v", err)
	}
}

func TestControlCommandsRejectedAfterClose(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	controllable := agent.(ControllableAgent)
	if err := agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controllable.Steer(UserMessage("late")); !IsCode(err, ErrAgentClosed) {
		t.Fatalf("Steer after Close = %v", err)
	}
	if err := controllable.FollowUp(UserMessage("late")); !IsCode(err, ErrAgentClosed) {
		t.Fatalf("FollowUp after Close = %v", err)
	}
	if _, err := controllable.Continue(context.Background()); !IsCode(err, ErrAgentClosed) {
		t.Fatalf("Continue after Close = %v", err)
	}
}

func TestAbortCancelsTheCurrentRun(t *testing.T) {
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	model := &testModel{started: started, block: block, scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	controllable := agent.(ControllableAgent)
	controllable.Abort() // no active Run: a no-op, not a panic

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("abort me"))
		done <- runErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Model was not called")
	}
	controllable.Abort()
	select {
	case err := <-done:
		if !IsCode(err, ErrCancelled) {
			t.Fatalf("aborted Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aborted Run did not return")
	}
	if got := agent.(AgentLifecycle).Status(); got != AgentIdle {
		t.Fatalf("status after Abort = %s", got)
	}
}

func TestControlMessagesDoNotSurviveAFailedRun(t *testing.T) {
	model := &recordingModel{
		scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}, finalScript("second")},
		started: make(chan int, 8),
		gate:    make(chan struct{}, 8),
	}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	controllable := agent.(ControllableAgent)

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("cancel me"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	if err := controllable.FollowUp(UserMessage("leftover")); err != nil {
		t.Fatal(err)
	}
	controllable.Abort()
	model.gate <- struct{}{}
	select {
	case err := <-done:
		if !IsCode(err, ErrCancelled) {
			t.Fatalf("aborted Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aborted Run did not return")
	}

	model.gate <- struct{}{}
	if _, err := agent.Prompt(context.Background(), UserMessage("fresh")); err != nil {
		t.Fatal(err)
	}
	for call := 1; call < model.calls(); call++ {
		if containsUserText(model.transcript(call), "leftover") {
			t.Fatalf("control message survived the failed Run: %+v", model.transcript(call))
		}
	}
}

func waitForCall(t *testing.T, started chan int, want int) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("Model call index = %d, want %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Model call %d never started", want)
	}
}
