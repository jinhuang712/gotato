package gotato

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingTranscript is an external Transcript that records appends.
type recordingTranscript struct {
	mu       sync.Mutex
	messages []Message
	appends  int
}

func (t *recordingTranscript) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.messages)
}

func (t *recordingTranscript) Messages() []Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneMessages(t.messages)
}

func (t *recordingTranscript) Append(message Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messages = append(t.messages, message)
	t.appends++
	return nil
}

func TestAgentCommitsToExternalTranscript(t *testing.T) {
	transcript := &recordingTranscript{}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "hello"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	messages := transcript.Messages()
	if len(messages) != 2 || messages[0].Role != RoleUser || messages[1].Role != RoleAssistant {
		t.Fatalf("transcript = %+v", messages)
	}
	if transcript.appends != 2 {
		t.Fatalf("appends = %d", transcript.appends)
	}
	// A second Agent against the same Transcript continues the history.
	second, err := NewAgent(WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if _, err := second.Prompt(context.Background(), UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if got := len(transcript.Messages()); got != 4 {
		t.Fatalf("transcript length after second agent = %d", got)
	}
	request := model.lastRequest()
	if len(request.Messages) != 3 {
		t.Fatalf("second agent saw %d messages, want 3 (history + new prompt)", len(request.Messages))
	}
}

func (m *testModel) lastRequest() ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func TestContextBuilderShapesModelViewAndEmitsEvent(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "ok"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	builder := ContextBuilderFunc(func(_ context.Context, snapshot ContextSnapshot) (ModelContext, error) {
		// Keep only the last Message.
		return ModelContext{
			SystemInstructions: "override",
			Messages:           snapshot.Messages[len(snapshot.Messages)-1:],
			Metadata:           map[string]string{"strategy": "last_only", "dropped_messages": strconv.Itoa(len(snapshot.Messages) - 1)},
		}, nil
	})
	transcript := &recordingTranscript{messages: []Message{UserMessage("old"), AssistantMessage("older answer")}}
	agent, err := NewAgent(WithModel(model), WithInstruction("base"), WithTranscript(transcript), WithContextBuilder(builder))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	stream, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("new")); err != nil {
		t.Fatal(err)
	}
	request := model.lastRequest()
	if request.SystemInstructions != "override" || len(request.Messages) != 1 || TextOf(request.Messages[0]) != "new" {
		t.Fatalf("model saw %+v", request)
	}
	// The Session still has everything: Context is a projection.
	if got := len(transcript.Messages()); got != 4 {
		t.Fatalf("transcript length = %d, want 4", got)
	}
	var built *Event
	for {
		event, err := stream.Next(context.Background())
		if err != nil {
			break
		}
		if event.Kind == EventContextBuilt {
			copied := event
			built = &copied
		}
		if event.Kind == EventAgentEnd {
			break
		}
	}
	if built == nil {
		t.Fatal("no context_built event")
	}
	if built.Payload["strategy"] != "last_only" || built.Payload["messages"] != 1 || built.Payload["source_messages"] != 3 {
		t.Fatalf("context_built payload = %v", built.Payload)
	}
}

func TestContextBuilderFailureSettlesRun(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	builder := ContextBuilderFunc(func(context.Context, ContextSnapshot) (ModelContext, error) {
		return ModelContext{}, errors.New("boom")
	})
	agent, err := NewAgent(WithModel(model), WithContextBuilder(builder))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	result, err := agent.Prompt(context.Background(), UserMessage("hi"))
	if !IsCode(err, ErrExtensionFailure) || result.Status != RunFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type dynamicSource struct {
	mu    sync.Mutex
	tools []Tool
}

func (s *dynamicSource) Tools() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Tool(nil), s.tools...)
}

func (s *dynamicSource) set(tools ...Tool) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

func TestToolSourceRefreshesAtTurnBoundary(t *testing.T) {
	source := &dynamicSource{}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if got := agent.(ToolInspector).Tools(); len(got) != 0 {
		t.Fatalf("tools before registration = %v", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if got := len(model.lastRequest().Tools); got != 0 {
		t.Fatalf("model saw %d tools, want 0", got)
	}
	source.set(&scriptedTool{})
	if _, err := agent.Prompt(context.Background(), UserMessage("two")); err != nil {
		t.Fatal(err)
	}
	request := model.lastRequest()
	if len(request.Tools) != 1 || request.Tools[0].ID != "demo.echo" {
		t.Fatalf("model saw tools %+v", request.Tools)
	}
	if got := agent.(ToolInspector).Tools(); len(got) != 1 || got[0].ID != "demo.echo" {
		t.Fatalf("inspector = %+v", got)
	}
	source.set()
	if _, err := agent.Prompt(context.Background(), UserMessage("three")); err != nil {
		t.Fatal(err)
	}
	if got := len(model.lastRequest().Tools); got != 0 {
		t.Fatalf("model saw %d tools after removal, want 0", got)
	}
}

func TestToolSourceToolIsCallable(t *testing.T) {
	tool := &scriptedTool{}
	source := &dynamicSource{tools: []Tool{tool}}
	agent, err := NewAgent(WithModel(&toolModel{}), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	result, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	if TextOf(*result.FinalMessage) != "final" {
		t.Fatalf("final = %+v", result.FinalMessage)
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d", tool.calls)
	}
}

func TestPromptRejectsBlankParts(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	for _, message := range []Message{UserMessage(""), UserMessage("   "), {Role: RoleUser}} {
		if _, err := agent.Prompt(context.Background(), message); !IsCode(err, ErrInvalidArgument) {
			t.Fatalf("Prompt(%+v) err = %v, want invalid_argument", message, err)
		}
	}
	image := Message{Role: RoleUser, Parts: []ContentPart{{Kind: ContentImage, Data: []byte{1, 2, 3}, MIMEType: "image/png"}}}
	if _, err := agent.Prompt(context.Background(), image); err != nil {
		t.Fatalf("image-only prompt rejected: %v", err)
	}
}

func TestSubscribeDoesNotLeakGoroutines(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 500; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := agent.(EventSource).Subscribe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
		cancel()
	}
	// Close removes every subscription deterministically; this cannot depend on
	// goroutine scheduling.
	core := agent.(*coreAgent)
	core.events.mu.Lock()
	remaining := len(core.events.subs)
	core.events.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("event hub retained %d subscriptions after Close", remaining)
	}
	// The per-subscription waiter goroutines must exit too. Poll to the exact
	// baseline (no slack) so a real leak fails, tolerating transient runtime
	// goroutines until the deadline.
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		if runtime.NumGoroutine() <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines before=%d after=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDefaultLimitsAllowPartialOverride(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTurns = 3
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "ok"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatalf("prompt with partial override failed: %v", err)
	}
}

func TestTranscriptByteLimitTrackedIncrementally(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTranscriptBytes = 400
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: strings.Repeat("x", 100)}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewAgent(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	var lastErr error
	for i := 0; i < 10 && lastErr == nil; i++ {
		_, lastErr = agent.Prompt(context.Background(), UserMessage("hi"))
	}
	if !IsCode(lastErr, ErrLimitExceeded) {
		t.Fatalf("expected limit_exceeded, got %v", lastErr)
	}
}
