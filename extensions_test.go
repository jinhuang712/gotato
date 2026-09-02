package gotato

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// trace records the order in which Extension stages ran.
type trace struct {
	mu    sync.Mutex
	steps []string
}

func (t *trace) add(step string) {
	t.mu.Lock()
	t.steps = append(t.steps, step)
	t.mu.Unlock()
}

func (t *trace) steps0() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.steps...)
}

type toolStages struct {
	name    string
	trace   *trace
	block   bool
	preErr  error
	postErr error
	tag     string
}

func (e *toolStages) Before(ctx context.Context, use ToolUse) (PreToolDecision, error) {
	e.trace.add("pre:" + e.name)
	if e.preErr != nil {
		return PreToolDecision{}, e.preErr
	}
	if e.block {
		return PreToolDecision{Block: true, Reason: "blocked by " + e.name}, nil
	}
	return PreToolDecision{}, nil
}

func (e *toolStages) After(ctx context.Context, result ToolResult) (ToolResult, error) {
	e.trace.add("post:" + e.name)
	if e.postErr != nil {
		return result, e.postErr
	}
	if e.tag != "" {
		if result.Metadata == nil {
			result.Metadata = map[string]string{}
		}
		result.Metadata[e.tag] = e.name
	}
	// A Post component must not be able to rewrite identity or Executed
	// truth; Core preserves both.
	result.CallID = "forged"
	result.Executed = !result.Executed
	return result, nil
}

type countingTool struct {
	mu    sync.Mutex
	calls int
}

func (t *countingTool) Spec() ToolSpec {
	return ToolSpec{ID: "demo.echo", InputSchema: []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`)}
}

func (t *countingTool) Execute(ctx context.Context, use ToolUse, progress ToolProgress) (ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: "tool-ok"}}}, nil
}

func (t *countingTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func TestToolStagesRunInOrderAndReverseOrder(t *testing.T) {
	recorder := &trace{}
	tool := &countingTool{}
	first := &toolStages{name: "first", trace: recorder, tag: "first"}
	second := &toolStages{name: "second", trace: recorder, tag: "second"}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithTool(tool), WithExtensions(first, second))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())

	if _, err := agent.Prompt(context.Background(), UserMessage("use tool")); err != nil {
		t.Fatal(err)
	}
	want := []string{"pre:first", "pre:second", "post:second", "post:first"}
	got := recorder.steps0()
	if len(got) != len(want) {
		t.Fatalf("stage order = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stage order = %v, want %v", got, want)
		}
	}
}

func TestPreToolUseBlockSkipsTheExecutor(t *testing.T) {
	recorder := &trace{}
	tool := &countingTool{}
	blocker := &toolStages{name: "blocker", trace: recorder, block: true}
	after := &toolStages{name: "after", trace: recorder}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithTool(tool), WithExtensions(blocker, after))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())

	if _, err := agent.Prompt(context.Background(), UserMessage("use tool")); err != nil {
		t.Fatal(err)
	}
	if tool.count() != 0 {
		t.Fatalf("blocked Tool executed %d times", tool.count())
	}
	steps := recorder.steps0()
	for _, step := range steps {
		if step == "pre:after" {
			t.Fatalf("Pre chain continued past a block: %v", steps)
		}
	}
}

type contextShaper struct {
	extra   string
	convert func([]Message) []Message
}

func (s contextShaper) Transform(ctx context.Context, snapshot ContextSnapshot) ([]Message, error) {
	return append(snapshot.Messages, UserMessage(s.extra)), nil
}

func (s contextShaper) Convert(ctx context.Context, messages []Message) ([]Message, error) {
	if s.convert == nil {
		return messages, nil
	}
	return s.convert(messages), nil
}

func TestTransformersShapeTheModelViewOnly(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	shaper := contextShaper{
		extra: "injected context",
		convert: func(messages []Message) []Message {
			return append(messages, UserMessage("converted"))
		},
	}
	agent, err := NewAgent(WithModel(model), WithExtension(shaper))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())

	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	sent := model.transcript(0)
	if !containsUserText(sent, "injected context") || !containsUserText(sent, "converted") {
		t.Fatalf("Model view = %+v", sent)
	}
}

type eventObserver struct {
	mu       sync.Mutex
	kinds    []EventKind
	failOn   EventKind
	advisory bool
	panicOn  EventKind
}

func (o *eventObserver) Observe(ctx context.Context, event Event) error {
	o.mu.Lock()
	o.kinds = append(o.kinds, event.Kind)
	o.mu.Unlock()
	if o.panicOn != "" && event.Kind == o.panicOn {
		panic("observer exploded")
	}
	if o.failOn != "" && event.Kind == o.failOn {
		return errors.New("observer refused " + string(event.Kind))
	}
	return nil
}

func (o *eventObserver) Advisory() bool { return o.advisory }

func (o *eventObserver) seen() []EventKind {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]EventKind(nil), o.kinds...)
}

func TestObserverSeesProductionOrder(t *testing.T) {
	observer := &eventObserver{}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithExtension(observer))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	kinds := observer.seen()
	if len(kinds) == 0 || kinds[0] != EventAgentStart || kinds[len(kinds)-1] != EventAgentEnd {
		t.Fatalf("observed order = %v", kinds)
	}
}

func TestBlockingObserverFailureSettlesTheRun(t *testing.T) {
	observer := &eventObserver{failOn: EventTurnStart}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithExtension(observer))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	result, err := agent.Prompt(context.Background(), UserMessage("hello"))
	if !IsCode(err, ErrExtensionFailure) {
		t.Fatalf("blocking observer failure = %v", err)
	}
	if result.Status != RunFailed {
		t.Fatalf("result = %+v", result)
	}
	if model.calls() != 0 {
		t.Fatalf("Model was called %d times after a blocking failure", model.calls())
	}
	terminal := 0
	for _, kind := range observer.seen() {
		if kind == EventAgentEnd {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events = %d", terminal)
	}
}

func TestAdvisoryObserverFailureDoesNotSettleTheRun(t *testing.T) {
	observer := &eventObserver{failOn: EventTurnStart, advisory: true}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithExtension(observer))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	result, err := agent.Prompt(context.Background(), UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("result = %+v", result)
	}
}

func TestExtensionPanicIsRecovered(t *testing.T) {
	observer := &eventObserver{panicOn: EventTurnStart}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithExtension(observer))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); !IsCode(err, ErrExtensionFailure) {
		t.Fatalf("panicking extension = %v", err)
	}
	if got := agent.(AgentLifecycle).Status(); got != AgentIdle {
		t.Fatalf("status after a panicking extension = %s", got)
	}
}

type turnStopper struct {
	after  TurnNumber
	reason string
}

func (s turnStopper) Stop(ctx context.Context, snapshot TurnSnapshot) (StopDecision, error) {
	if snapshot.Turn >= s.after {
		return StopDecision{Stop: true, Reason: s.reason}, nil
	}
	return StopDecision{}, nil
}

func TestTurnStopperPreventsTheNextModelCall(t *testing.T) {
	tool := &countingTool{}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("never reached")}}
	agent, err := NewAgent(WithModel(model), WithTool(tool), WithExtension(turnStopper{after: 1, reason: "budget"}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	events, err := agent.(EventSource).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	result, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("stopped Run = %+v", result)
	}
	if model.calls() != 1 {
		t.Fatalf("Model calls after a stop = %d", model.calls())
	}
	if tool.count() != 1 {
		t.Fatalf("the stopped Turn was not preserved: tool calls = %d", tool.count())
	}
	var terminal Event
	for {
		event, nextErr := events.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if event.Kind == EventAgentEnd {
			terminal = event
			break
		}
	}
	if terminal.Payload["stopped_by_extension"] != true || terminal.Payload["stop_reason"] != "budget" {
		t.Fatalf("terminal payload = %+v", terminal.Payload)
	}
}

type reentrantObserver struct {
	agent Agent
	err   error
	once  sync.Once
}

func (o *reentrantObserver) Observe(ctx context.Context, event Event) error {
	if event.Kind != EventTurnStart {
		return nil
	}
	o.once.Do(func() {
		_, o.err = o.agent.Prompt(context.Background(), UserMessage("reentrant"))
	})
	return nil
}

func (o *reentrantObserver) Advisory() bool { return true }

func TestExtensionCannotReenterTheSameAgent(t *testing.T) {
	observer := &reentrantObserver{}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewAgent(WithModel(model), WithExtension(observer))
	if err != nil {
		t.Fatal(err)
	}
	observer.agent = agent
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if !IsCode(observer.err, ErrBusy) {
		t.Fatalf("reentrant Prompt = %v", observer.err)
	}
}

func TestWithExtensionRejectsUnknownValues(t *testing.T) {
	if _, err := NewAgent(WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}), WithExtension(struct{}{})); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("value implementing no Extension interface = %v", err)
	}
	if _, err := NewAgent(WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}), WithExtension(nil)); !IsCode(err, ErrInvalidArgument) {
		t.Fatalf("nil extension = %v", err)
	}
}
