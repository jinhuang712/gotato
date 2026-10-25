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

func TestSessionCommitsToExternalTranscript(t *testing.T) {
	transcript := &recordingTranscript{}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "hello"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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
	// A second Session against the same Transcript continues the history.
	second, err := NewSession(WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
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
			// A builder key that names a count must not replace the count.
			Metadata: map[string]string{"strategy": "last_only", "source_messages": "99", "dropped_messages": strconv.Itoa(len(snapshot.Messages) - 1)},
		}, nil
	})
	transcript := &recordingTranscript{messages: []Message{UserMessage("old"), AssistantMessage("older answer")}}
	agent, err := NewSession(WithModel(model), WithInstruction("base"), WithTranscript(transcript), WithContextBuilder(builder))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	stream, err := agent.Subscribe(context.Background())
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
	var built *ContextBuilt
	for {
		event, err := stream.Next(context.Background())
		if err != nil {
			break
		}
		if e, ok := event.(ContextBuilt); ok {
			built = &e
		}
		if _, ok := event.(RunEnded); ok {
			break
		}
	}
	if built == nil {
		t.Fatal("no context_built event")
	}
	if built.Strategy != "last_only" || built.Messages != 1 || built.SourceMessages != 3 || built.SelectedMessages != 1 {
		t.Fatalf("ContextBuilt = %+v", *built)
	}
	if built.Metadata["source_messages"] != "99" || built.Metadata["dropped_messages"] != "2" || built.Metadata["strategy"] != "last_only" {
		t.Fatalf("ContextBuilt metadata = %v", built.Metadata)
	}
}

func TestContextBuilderFailureSettlesRun(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelDone, StopReason: StopEndTurn}}}}
	builder := ContextBuilderFunc(func(context.Context, ContextSnapshot) (ModelContext, error) {
		return ModelContext{}, errors.New("boom")
	})
	agent, err := NewSession(WithModel(model), WithContextBuilder(builder))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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
	agent, err := NewSession(WithModel(model), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if got := agent.Tools(); len(got) != 0 {
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
	if got := agent.Tools(); len(got) != 1 || got[0].ID != "demo.echo" {
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
	agent, err := NewSession(WithModel(&toolModel{}), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
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
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	runtime.GC()
	before := runtime.NumGoroutine()
	for range 500 {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := agent.Subscribe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
		cancel()
	}
	// Close removes every subscription deterministically; this cannot depend on
	// goroutine scheduling.
	agent.feed.mu.Lock()
	remaining := len(agent.feed.subs)
	agent.feed.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("the Feed retained %d subscriptions after Close", remaining)
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
	limits.MaxSteerMessages = 3
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "ok"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatalf("prompt with partial override failed: %v", err)
	}
}

// A Context that starts with rendered Segments counts them toward MaxMessages,
// as a load reads them, so the default leaves room for a long Session.
func TestDefaultMaxMessagesLeavesRoomForSegments(t *testing.T) {
	history := &memoryTranscript{}
	for range 1500 {
		history.messages = append(history.messages, UserMessage("earlier"))
	}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "ok"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model), WithTranscript(history))
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
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: strings.Repeat("x", 100)}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
	agent, err := NewSession(WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	var lastErr error
	for i := 0; i < 10 && lastErr == nil; i++ {
		_, lastErr = agent.Prompt(context.Background(), UserMessage("hi"))
	}
	if !IsCode(lastErr, ErrLimitExceeded) {
		t.Fatalf("expected limit_exceeded, got %v", lastErr)
	}
}
