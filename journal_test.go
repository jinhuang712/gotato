package gotato

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// journalTranscript records each step with the number of Messages committed
// before it, and fails a step of kind failAt with failErr.
type journalTranscript struct {
	recordingTranscript
	mu      sync.Mutex
	steps   []Step
	before  []int
	failAt  StepKind
	failErr error
}

func (j *journalTranscript) Step(_ context.Context, step Step) error {
	committed := j.Len()
	j.mu.Lock()
	defer j.mu.Unlock()
	if step.Kind == j.failAt {
		return j.failErr
	}
	j.steps = append(j.steps, step)
	j.before = append(j.before, committed)
	return nil
}

func (j *journalTranscript) kinds() []StepKind {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]StepKind, len(j.steps))
	for i, step := range j.steps {
		out[i] = step.Kind
	}
	return out
}

func echoToolModel(calls ...string) *testModel {
	var first []ModelEvent
	for _, id := range calls {
		first = append(first, ModelEvent{Kind: ModelToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}})
	}
	first = append(first, ModelEvent{Kind: ModelDone, StopReason: StopToolCalls})
	return &testModel{scripts: [][]ModelEvent{first, {{Kind: ModelTextDelta, Text: "done"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
}

func TestJournalStepsInOrder(t *testing.T) {
	journal := &journalTranscript{}
	session, err := NewSession(WithModel(echoToolModel("call-1", "call-2")), WithTool(&scriptedTool{}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	want := []StepKind{
		StepPrompt,
		StepModelCall, StepReply,
		StepToolStart, StepToolResult, StepToolStart, StepToolResult,
		StepModelCall, StepReply,
	}
	got := journal.kinds()
	if strings.Join(stepNames(got), ",") != strings.Join(stepNames(want), ",") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	// A step comes after the Messages it makes durable: the prompt is the
	// first Message, each tool result one more, and so on.
	if want := []int{1, 1, 2, 2, 3, 3, 4, 4, 5}; !equalInts(journal.before, want) {
		t.Fatalf("Messages committed before each step = %v, want %v", journal.before, want)
	}
	if calls := journal.steps[3].Calls; len(calls) != 1 || calls[0] != "call-1" {
		t.Fatalf("first tool_start names %v, want call-1 alone", calls)
	}
}

func TestToolResultsCommitPerGroup(t *testing.T) {
	journal := &journalTranscript{}
	session, err := NewSession(WithModel(echoToolModel("call-1", "call-2")), WithTool(&scriptedTool{}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	// One Tool runs at a time by default, so the first call's result is
	// committed before the second call starts.
	messages := journal.Messages()
	if len(messages) != 5 || messages[2].ToolResult == nil || messages[2].ToolResult.CallID != "call-1" || messages[3].ToolResult.CallID != "call-2" {
		t.Fatalf("transcript = %+v", messages)
	}
	if journal.before[4] != 3 || journal.before[5] != 3 {
		t.Fatalf("the second tool_start came after %d Messages, want 3 (its predecessor's result committed)", journal.before[5])
	}
}

func TestReplyPartsMergeDeltas(t *testing.T) {
	delta := strings.Repeat("a", 100)
	var events []ModelEvent
	for range 100 {
		events = append(events, ModelEvent{Kind: ModelTextDelta, Text: delta})
	}
	events = append(events, ModelEvent{Kind: ModelDone, StopReason: StopEndTurn})
	journal := &journalTranscript{}
	session, err := NewSession(WithModel(&testModel{scripts: [][]ModelEvent{events}}), WithTranscript(journal))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var parts []Step
	for _, step := range journal.steps {
		if step.Kind == StepReplyPart {
			parts = append(parts, step)
		}
	}
	// 10,000 bytes in 100 deltas make two parts of at least 4 KiB each, not a
	// write per delta.
	if len(parts) != 2 {
		t.Fatalf("reply parts = %d, want 2", len(parts))
	}
	for _, part := range parts {
		if len(part.Text) < replyPartBytes || part.MessageID == "" {
			t.Fatalf("part = %d bytes for %q", len(part.Text), part.MessageID)
		}
	}
}

func TestJournalFailureStopsBeforeTheModelCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code ErrorCode
	}{
		{name: "conflict", err: ErrorOf(ErrConflict, "another writer"), code: ErrConflict},
		{name: "other failure", err: errors.New("disk full"), code: ErrStoreFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelTextDelta, Text: "hi"}, {Kind: ModelDone, StopReason: StopEndTurn}}}}
			journal := &journalTranscript{failAt: StepModelCall, failErr: tc.err}
			session, err := NewSession(WithModel(model), WithTranscript(journal))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), UserMessage("go"))
			if err == nil || result.Error == nil || result.Error.Code != tc.code {
				t.Fatalf("result = %+v, err = %v; want %s", result, err, tc.code)
			}
			model.mu.Lock()
			calls := model.calls
			model.mu.Unlock()
			if calls != 0 {
				t.Fatalf("the model was called %d times after its step failed", calls)
			}
		})
	}
}

func stepNames(kinds []StepKind) []string {
	out := make([]string, len(kinds))
	for i, kind := range kinds {
		out[i] = string(kind)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
