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

func (t *countingTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "tool-ok"}}}, nil
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
	agent, err := NewSession(WithModel(model), WithTools(tool), WithExtension(first), WithExtension(second))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

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

	// A Post component may forge identity and invert Executed; Core must
	// restore both before the Tool Result is committed.
	var committed *ToolResult
	for _, message := range agent.Transcript().Messages() {
		if message.ToolResult != nil {
			committed = message.ToolResult
		}
	}
	if committed == nil {
		t.Fatal("no Tool Result was committed")
	}
	if committed.CallID != "call-1" {
		t.Fatalf("committed CallID = %q, want call-1", committed.CallID)
	}
	if !committed.Executed {
		t.Fatal("committed Executed = false, want true")
	}
}

func TestPreToolUseBlockSkipsTheExecutor(t *testing.T) {
	recorder := &trace{}
	tool := &countingTool{}
	blocker := &toolStages{name: "blocker", trace: recorder, block: true}
	after := &toolStages{name: "after", trace: recorder}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithTools(tool), WithExtension(blocker), WithExtension(after))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

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
	agent, err := NewSession(WithModel(model), WithExtension(shaper))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	sent := model.transcript(0)
	if !containsUserText(sent, "injected context") || !containsUserText(sent, "converted") {
		t.Fatalf("Model view = %+v", sent)
	}
}

// The Session's Events are read through a subscription, never through an
// Extension: a Run's Events come in production order, and the Record numbers
// them without a gap.
func TestSubscriberSeesProductionOrder(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	kinds := seen.kinds()
	if len(kinds) == 0 || kinds[0] != EventKindRunStarted || kinds[len(kinds)-1] != EventKindRunEnded {
		t.Fatalf("observed order = %v", kinds)
	}
	var last uint64
	for _, event := range seen.events() {
		if event.Kind().Class() != EventClassStored {
			continue
		}
		if event.Head().Number != last+1 {
			t.Fatalf("stored Event numbers have a gap: %d after %d (%v)", event.Head().Number, last, kinds)
		}
		last = event.Head().Number
	}
}

// Events are values: what one subscriber does to its copy of an Event is not
// seen by another subscriber of the same Session.
func TestSubscriberEditsOfItsEventCopyAreIsolated(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	first := recordAll(t, agent)
	second := recordAll(t, agent)
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	for _, event := range first.events() {
		if ended, ok := event.(TurnEnded); ok {
			ended.ElapsedMS = -1
			ended.TextBytes = 12345
		}
	}
	found := false
	for _, event := range second.events() {
		if ended, ok := event.(TurnEnded); ok {
			found = true
			if ended.ElapsedMS == -1 || ended.TextBytes == 12345 {
				t.Fatal("one subscriber's edit leaked into another's Event")
			}
		}
	}
	if !found {
		t.Fatal("no TurnEnded Event")
	}
}

// extensionFault is a ContextTransformer that fails or panics on every call.
// It is blocking unless advisory is set.
type extensionFault struct {
	advisory bool
	panics   bool
}

func (f extensionFault) Transform(ctx context.Context, snapshot ContextSnapshot) ([]Message, error) {
	if f.panics {
		panic("extension exploded")
	}
	return nil, errors.New("extension refused")
}

func (f extensionFault) Advisory() bool { return f.advisory }

// A blocking extension that fails settles the Run before the Model is called,
// and the Record holds exactly one RunEnded.
func TestBlockingExtensionFailureSettlesTheRun(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithExtension(extensionFault{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)
	result, err := agent.Prompt(context.Background(), UserMessage("hello"))
	if !IsCode(err, CodeExtensionFailure) {
		t.Fatalf("blocking extension failure = %v", err)
	}
	if result.Status != RunStatusFailed {
		t.Fatalf("result = %+v", result)
	}
	if model.calls() != 0 {
		t.Fatalf("Model was called %d times after a blocking failure", model.calls())
	}
	terminal := 0
	for _, event := range seen.events() {
		if ended, ok := event.(RunEnded); ok {
			terminal++
			if ended.Status != RunStatusFailed || ended.Cause != CodeExtensionFailure {
				t.Fatalf("RunEnded = %+v", ended)
			}
		}
		if _, ok := event.(ExtensionFailed); ok {
			t.Fatal("a blocking failure settles the Run; it is not an advisory ExtensionFailed")
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events = %d", terminal)
	}
}

// An advisory extension that fails is recorded as a stored ExtensionFailed
// Event, and the Run goes on.
func TestAdvisoryExtensionFailureIsRecordedAndTheRunGoesOn(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithExtension(extensionFault{advisory: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)
	result, err := agent.Prompt(context.Background(), UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunStatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	var failed []ExtensionFailed
	for _, event := range seen.events() {
		if e, ok := event.(ExtensionFailed); ok {
			failed = append(failed, e)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("ExtensionFailed Events = %+v", failed)
	}
	if failed[0].Stage != "ContextTransformer" || failed[0].Code != CodeExtensionFailure || failed[0].Number == 0 {
		t.Fatalf("ExtensionFailed = %+v", failed[0])
	}
	if failed[0].Kind().Class() != EventClassStored {
		t.Fatalf("ExtensionFailed class = %s", failed[0].Kind().Class())
	}
}

func TestExtensionPanicIsRecovered(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithExtension(extensionFault{panics: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); !IsCode(err, CodeExtensionFailure) {
		t.Fatalf("panicking extension = %v", err)
	}
	if got := agent.Status(); got != SessionStatusIdle {
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

// snapshotRecorder keeps every turn's snapshot.
type snapshotRecorder struct {
	mu    sync.Mutex
	snaps []TurnSnapshot
}

func (r *snapshotRecorder) Stop(_ context.Context, snapshot TurnSnapshot) (StopDecision, error) {
	r.mu.Lock()
	r.snaps = append(r.snaps, snapshot)
	r.mu.Unlock()
	return StopDecision{}, nil
}

// A reply that the output limit cut off after a finished Tool call still runs
// the call, and its Message keeps StopReasonMaxTokens, so a TurnStopper sees the
// cut-off. The Turn itself ends with StopReasonToolCalls.
func TestACutOffReplyKeepsMaxTokensNextToItsToolCalls(t *testing.T) {
	tool := &scriptedTool{}
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}},
			{Kind: ModelEventKindDone, StopReason: StopReasonMaxTokens}},
		{{Kind: ModelEventKindTextDelta, Text: "final"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}},
	}}
	recorder := &snapshotRecorder{}
	agent, err := NewSession(WithModel(model), WithTools(tool), WithExtension(recorder))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("use tool")); err != nil {
		t.Fatal(err)
	}
	tool.mu.Lock()
	calls := tool.calls
	tool.mu.Unlock()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if calls != 1 || len(recorder.snaps) == 0 {
		t.Fatalf("tool calls %d, snapshots %d", calls, len(recorder.snaps))
	}
	first := recorder.snaps[0]
	if first.Assistant.StopReason != StopReasonMaxTokens || first.StopReason != StopReasonToolCalls {
		t.Fatalf("message stop %q, turn stop %q; want %q and %q", first.Assistant.StopReason, first.StopReason,
			StopReasonMaxTokens, StopReasonToolCalls)
	}
}

func TestTurnStopperPreventsTheNextModelCall(t *testing.T) {
	tool := &countingTool{}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("never reached")}}
	agent, err := NewSession(WithModel(model), WithTools(tool), WithExtension(turnStopper{after: 1, reason: "budget"}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)

	result, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != RunStatusCompleted {
		t.Fatalf("stopped Run = %+v", result)
	}
	if model.calls() != 1 {
		t.Fatalf("Model calls after a stop = %d", model.calls())
	}
	if tool.count() != 1 {
		t.Fatalf("the stopped Turn was not preserved: tool calls = %d", tool.count())
	}
	var terminal *RunEnded
	for _, event := range seen.events() {
		if ended, ok := event.(RunEnded); ok {
			terminal = &ended
		}
	}
	if terminal == nil {
		t.Fatal("no RunEnded Event")
	}
	if !terminal.StoppedByExtension || terminal.StopReason != "budget" {
		t.Fatalf("RunEnded = %+v", *terminal)
	}
}

// reentrantExtension tries to Prompt the Session that is calling it.
type reentrantExtension struct {
	agent *Session
	err   error
	once  sync.Once
}

func (o *reentrantExtension) Transform(ctx context.Context, snapshot ContextSnapshot) ([]Message, error) {
	o.once.Do(func() {
		_, o.err = o.agent.Prompt(context.Background(), UserMessage("reentrant"))
	})
	return snapshot.Messages, nil
}

func TestExtensionCannotReenterTheSameSession(t *testing.T) {
	extension := &reentrantExtension{}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(WithModel(model), WithExtension(extension))
	if err != nil {
		t.Fatal(err)
	}
	extension.agent = agent
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), UserMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if !IsCode(extension.err, CodeBusy) {
		t.Fatalf("reentrant Prompt = %v", extension.err)
	}
}

func TestWithExtensionRejectsUnknownValues(t *testing.T) {
	if _, err := NewSession(WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}), WithExtension(struct{}{})); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("value implementing no Extension interface = %v", err)
	}
	if _, err := NewSession(WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}), WithExtension(nil)); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("nil extension = %v", err)
	}
}
