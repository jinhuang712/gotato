package gotato

import (
	"context"
	"errors"
	"strings"
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

func TestToolStagesRunInOrderAndReverseOrder(t *testing.T) {
	recorder := &trace{}
	tool := &countingTool{}
	first := &toolStages{name: "first", trace: recorder, tag: "first"}
	second := &toolStages{name: "second", trace: recorder, tag: "second"}
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool), WithExtension(first), WithExtension(second))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool), WithExtension(blocker), WithExtension(after))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithExtension(shaper))
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
	agent, err := NewSession(context.Background(), WithModel(model))
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
	agent, err := NewSession(context.Background(), WithModel(model))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithExtension(extensionFault{}))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithExtension(extensionFault{advisory: true}))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithExtension(extensionFault{panics: true}))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool), WithExtension(recorder))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tool), WithExtension(turnStopper{after: 1, reason: "budget"}))
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
	agent, err := NewSession(context.Background(), WithModel(model), WithExtension(extension))
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
	if _, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}), WithExtension(struct{}{})); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("value implementing no Extension interface = %v", err)
	}
	if _, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}), WithExtension(nil)); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("nil extension = %v", err)
	}
}

// typoHook is a ContextTransformer whose permission hook has the wrong
// signature: it must not install silently as a transformer alone.
type typoHook struct{}

func (typoHook) Transform(context.Context, ContextSnapshot) ([]Message, error) { return nil, nil }
func (typoHook) Before(ToolUse) (PreToolDecision, error)                       { return PreToolDecision{}, nil }

type noHook struct{}

func (noHook) Frobnicate() {}

func TestWithExtensionFailsLoudlyOnAMismatchedHook(t *testing.T) {
	model := &testModel{}
	for name, ext := range map[string]any{"wrong Before signature": typoHook{}, "no hook at all": noHook{}} {
		_, err := NewSession(context.Background(), WithModel(model), WithExtension(ext))
		if !IsCode(err, CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
}

type publicErr struct{}

func (publicErr) Error() string  { return "dial tcp 10.0.3.7:5432: password=hunter2" }
func (publicErr) Public() string { return "the order service is unavailable" }

// failingTool returns err from Execute.
type failingTool struct{ err error }

func (t failingTool) Spec() ToolSpec { return (&scriptedTool{}).Spec() }
func (t failingTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{}, t.err
}

func failedToolText(t *testing.T, err error) string {
	t.Helper()
	session, serr := NewSession(context.Background(), WithModel(echoToolModel("call-1")), WithTools(failingTool{err: err}))
	if serr != nil {
		t.Fatal(serr)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, perr := session.Prompt(context.Background(), UserMessage("go")); perr != nil {
		t.Fatal(perr)
	}
	for _, m := range session.Transcript().Messages() {
		if m.ToolResult != nil {
			return m.ToolResult.SafeError
		}
	}
	t.Fatal("no tool result")
	return ""
}

// SafeError does not carry the text of an error the Tool returned, unless the
// error says it is public.
func TestSafeErrorKeepsOnlyPublicText(t *testing.T) {
	raw := failedToolText(t, errors.New("dial tcp 10.0.3.7:5432: password=hunter2"))
	if strings.Contains(raw, "hunter2") || !strings.Contains(raw, "tool failed") {
		t.Fatalf("SafeError = %q", raw)
	}
	wrapped := failedToolText(t, runtimeError(CodeStoreFailure, "db", "the store is down", errors.New("password=hunter2")))
	if strings.Contains(wrapped, "hunter2") {
		t.Fatalf("SafeError = %q", wrapped)
	}
	if got := failedToolText(t, publicErr{}); got != "the order service is unavailable" {
		t.Fatalf("SafeError = %q, want the public text", got)
	}
}

// The Message of an extension failure is fixed; the extension's text stays on
// the Cause.
func TestGuardDoesNotCarryRawErrorText(t *testing.T) {
	failure := guard("stage", func() error { return errors.New("password=hunter2") })
	if failure == nil || strings.Contains(failure.Error(), "hunter2") || failure.Cause == nil {
		t.Fatalf("failure = %v", failure)
	}
}
