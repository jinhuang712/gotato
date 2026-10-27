package modelctx_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// refusal is how a provider says a request is longer than its window.
type refusal struct{}

func (refusal) Error() string         { return "refused: the prompt is too long" }
func (refusal) ContextOverflow() bool { return true }

// stepModel answers each call from steps: a refusal, or a Script. It records
// the requests it received.
type stepModel struct {
	mu       sync.Mutex
	steps    []any
	requests []gotato.ModelRequest
}

func (m *stepModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	request.Messages = append([]gotato.Message(nil), request.Messages...)
	m.requests = append(m.requests, request)
	index := len(m.requests) - 1
	if index >= len(m.steps) {
		return nil, errors.New("stepModel: no step left")
	}
	if err, ok := m.steps[index].(error); ok {
		return nil, err
	}
	return testkit.NewFakeModel(m.steps[index].(testkit.Script)).Stream(ctx, request)
}

func (m *stepModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// reported is a Script that calls tool c<n> and reports usage for its request.
func calling(n string, input, output uint64) testkit.Script {
	script := testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID("c" + n), ToolID: "demo.read", Arguments: []byte(`{}`)})
	script[len(script)-1].Usage = gotato.Usage{InputTokens: input, OutputTokens: output}
	return script
}

func answering(text string) testkit.Script {
	script := testkit.Text(text)
	script[len(script)-1].Usage = gotato.Usage{InputTokens: 100, OutputTokens: 10}
	return script
}

// live is a stored Session whose Runs a test plays one after another.
type live struct {
	t      *testing.T
	store  session.Store
	writer *session.Writer
	auto   *modelctx.AutoCompactor
}

func newLive(t *testing.T, policy modelctx.CompactPolicy) *live {
	t.Helper()
	store := session.NewMemoryStore()
	writer, err := session.Create(context.Background(), store, session.New(session.WithID("long")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	return &live{t: t, store: store, writer: writer, auto: modelctx.AutoCompact(writer.Record(), policy)}
}

// run plays one prompt with model and a tool that returns result.
func (l *live) run(model gotato.Model, prompt, result string) (gotato.RunResult, error) {
	l.t.Helper()
	agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(l.writer),
		gotato.WithTool(testkit.NewFakeTool("demo.read", result)), gotato.WithExtension(l.auto))
	if err != nil {
		l.t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	return agent.Prompt(context.Background(), gotato.UserMessage(prompt))
}

func (l *live) must(model gotato.Model, prompt string) {
	l.t.Helper()
	if result, err := l.run(model, prompt, "ok"); err != nil || result.Status != gotato.RunCompleted {
		l.t.Fatalf("%q: %+v, %v", prompt, result, err)
	}
}

// whyOf lists why each compaction of the Record ran, in order.
func whyOf(record *session.Record) []gotato.CompactionWhy {
	var out []gotato.CompactionWhy
	for _, event := range record.Events() {
		if ended, ok := event.(gotato.CompactionEnded); ok {
			out = append(out, ended.Why)
		}
	}
	return out
}

func encode(t *testing.T, messages []gotato.Message) string {
	t.Helper()
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A Run that reaches the ceiling between two Turns is compacted there and
// goes on: the Segment holds what came before, the latest Turn stays with its
// tool call and result, and another instance builds the same request.
func TestARunPastTheCeilingIsCompactedBetweenTurnsAndGoesOn(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{Ceiling: 5000})
	l.must(testkit.NewFakeModel(answering("earlier answer")), "an earlier question")
	model := &stepModel{steps: []any{calling("1", 400, 10), calling("2", 9000, 1000), answering("all done")}}
	result, err := l.run(model, "investigate", strings.Repeat("log line ", 350))
	if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "all done" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if model.calls() != 3 || result.Metrics.Turns != 3 {
		t.Fatalf("%d model calls in %d Turns, want 3 and 3", model.calls(), result.Metrics.Turns)
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionPastCeiling {
		t.Fatalf("compactions = %v, want one past_ceiling", got)
	}
	// The two requests before the ceiling were not compacted; the third holds
	// the Segment, and ends with the latest Turn, the call with its result.
	if first := gotato.TextOf(model.requests[1].Messages[0]); strings.Contains(first, "<segment") {
		t.Fatalf("the second request already holds a Segment: %q", first)
	}
	third := model.requests[2].Messages
	if !strings.Contains(gotato.TextOf(third[0]), `<segment n="1"`) {
		t.Fatalf("the third request starts with %q, want the Segment", gotato.TextOf(third[0]))
	}
	n := len(third)
	if third[n-2].Role != gotato.RoleAssistant || len(third[n-2].ToolCalls) != 1 || third[n-2].ToolCalls[0].ID != "c2" ||
		third[n-1].Role != gotato.RoleToolResult || third[n-1].ToolResult.CallID != "c2" {
		t.Fatalf("the request does not end with the latest tool call and its result: %s", encode(t, third[n-2:]))
	}
	// The Segment keeps the Run's prompt word for word, and a line per tool call.
	segment := encode(t, l.writer.Record().Segments()[0].Messages)
	if !strings.Contains(segment, "investigate") || !strings.Contains(segment, "demo.read") {
		t.Errorf("the Segment lacks the Run's prompt or its tool call: %s", segment)
	}
	// Another instance loads the Session and builds the request the Run sent.
	if err := l.writer.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := session.View(context.Background(), l.store, "long")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := encode(t, gotato.ForModel(reloaded.Messages()[:len(third)])), encode(t, third); got != want {
		t.Fatalf("the reloaded Session builds another request\n got: %s\nwant: %s", got, want)
	}
}

// The provider refuses a request as too long: the Session is compacted and the
// call is sent again, once. The Segments before keep their bytes.
func TestARequestRefusedAsTooLongIsCompactedAndSentAgainOnce(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{})
	for _, prompt := range []string{"one", "two", "three"} {
		l.must(testkit.NewFakeModel(answering("reply to "+prompt)), prompt)
	}
	if result, err := modelctx.Compact(context.Background(), l.writer.Record(), modelctx.CompactOptions{Keep: 2}); err != nil || !result.Replaced {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	// Two Runs that read a large result each: a Segment keeps a line per call.
	for _, prompt := range []string{"four", "five"} {
		steps := &stepModel{steps: []any{calling(prompt, 300, 10), answering("reply to " + prompt)}}
		if result, err := l.run(steps, prompt, strings.Repeat("log line ", 350)); err != nil || result.Status != gotato.RunCompleted {
			t.Fatalf("%q: %+v, %v", prompt, result, err)
		}
	}
	segmentOne := encode(t, l.writer.Record().Segments()[0].Messages)

	model := &stepModel{steps: []any{refusal{}, answering("fits now")}}
	result, err := l.run(model, "six", "ok")
	if err != nil || result.Status != gotato.RunCompleted || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 2 || got[1] != gotato.CompactionOverflow {
		t.Fatalf("compactions = %v, want on_demand then overflow", got)
	}
	refused, sent := model.requests[0].Messages, model.requests[1].Messages
	if modelctx.EstimateTokens(sent) >= modelctx.EstimateTokens(refused) {
		t.Fatalf("the second request has %d tokens, the refused one %d", modelctx.EstimateTokens(sent), modelctx.EstimateTokens(refused))
	}
	segments := l.writer.Record().Segments()
	if len(segments) != 2 || encode(t, segments[0].Messages) != segmentOne {
		t.Fatalf("Segment 1 changed, or no Segment 2: %d Segments", len(segments))
	}
	// The request starts with Segment 1's bytes, then Segment 2.
	head, wantHead := encode(t, sent[:len(segments[0].Messages)]), encode(t, gotato.ForModel(segments[0].Messages))
	if head != wantHead {
		t.Fatalf("the request does not start with Segment 1 as it was\n got: %s\nwant: %s", head, wantHead)
	}
	if last := gotato.TextOf(sent[len(sent)-1]); last != "six" {
		t.Errorf("the request ends with %q, want the prompt of the Run", last)
	}
}

// A second refusal in the same Turn ends the Run with context_overflow, after
// one compaction and one retry.
func TestASecondRefusalEndsTheRunWithContextOverflow(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{})
	for _, prompt := range []string{"one", "two", "three"} {
		l.must(testkit.NewFakeModel(answering("reply to "+prompt)), prompt)
	}
	model := &stepModel{steps: []any{refusal{}, refusal{}, answering("never")}}
	result, err := l.run(model, "four", "ok")
	if !gotato.IsCode(err, gotato.ErrContextOverflow) || result.Status != gotato.RunFailed || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionOverflow {
		t.Fatalf("compactions = %v, want one overflow", got)
	}
}

// A Turn compacts at most once: a refusal after the Turn compacted past the
// ceiling is not compacted again.
func TestATurnThatCompactedPastTheCeilingIsNotCompactedAgainOnAnOverflow(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{Ceiling: 5000})
	l.must(testkit.NewFakeModel(answering("earlier answer")), "an earlier question")
	model := &stepModel{steps: []any{calling("1", 9000, 1000), refusal{}, answering("never")}}
	result, err := l.run(model, "investigate", "ok")
	if !gotato.IsCode(err, gotato.ErrContextOverflow) || result.Status != gotato.RunFailed || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionPastCeiling {
		t.Fatalf("compactions = %v, want one past_ceiling", got)
	}
}

// Still past the ceiling after the Turn's compaction, the Run ends with
// limit_exceeded and no further call is made. One prompt too large for the
// ceiling is the cause here: a large tool result is spilled, so it is not.
func TestARunStillPastTheCeilingAfterItsCompactionEndsWithLimitExceeded(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{Ceiling: 5000})
	l.must(testkit.NewFakeModel(answering("earlier answer")), "an earlier question")
	model := &stepModel{steps: []any{calling("1", 400, 10), answering("never")}}
	result, err := l.run(model, strings.Repeat("a very large prompt ", 2000), "ok")
	if !gotato.IsCode(err, gotato.ErrLimitExceeded) || result.Status != gotato.RunFailed || model.calls() != 0 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if !strings.Contains(err.Error(), "past the ceiling") {
		t.Errorf("the error does not say why: %v", err)
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionPastCeiling {
		t.Fatalf("compactions = %v, want one past_ceiling", got)
	}
}

// With no window and no ceiling, past-the-ceiling compaction is off; a window
// gives the default ceiling; a negative ceiling turns it off.
func TestTheCeilingComesFromTheWindowUnlessSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy modelctx.CompactPolicy
		want   int
	}{
		{"no window, no ceiling", modelctx.CompactPolicy{}, 0},
		{"a 200K window", modelctx.CompactPolicy{Window: 200_000}, 180_000},
		{"a small window keeps the reserve", modelctx.CompactPolicy{Window: 64_000}, 48_000},
		{"a window too small to leave any", modelctx.CompactPolicy{Window: 10_000}, 0},
		{"the ceiling wins", modelctx.CompactPolicy{Window: 200_000, Ceiling: 1234}, 1234},
		{"a negative ceiling is off", modelctx.CompactPolicy{Window: 200_000, Ceiling: -1}, 0},
	} {
		if got := modelctx.AutoCompact(nil, tc.policy).Ceiling(); got != tc.want {
			t.Errorf("%s: ceiling = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// After a compaction the usage of the old replies no longer counts: the kept
// Messages carry none, so the size is estimated afresh.
func TestACompactionClearsTheUsageOfTheMessagesItKeeps(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{})
	for _, prompt := range []string{"one", "two", "three"} {
		l.must(testkit.NewFakeModel(answering("reply to "+prompt)), prompt)
	}
	if _, err := modelctx.Compact(context.Background(), l.writer.Record(), modelctx.CompactOptions{Keep: 2}); err != nil {
		t.Fatal(err)
	}
	for _, message := range l.writer.Record().Tail() {
		if message.Usage != (gotato.Usage{}) {
			t.Fatalf("a kept Message still carries usage: %+v", message)
		}
	}
}

// Without a Run in flight the Record is below the ceiling by its size, and
// Pending says so by the same count the Loop uses.
func TestPendingFollowsTheReportedUsage(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{Ceiling: 5000})
	l.must(testkit.NewFakeModel(answering("short")), "hi")
	if l.auto.Pending() {
		t.Fatal("a short Session is pending")
	}
	big := answering("long")
	big[len(big)-1].Usage = gotato.Usage{InputTokens: 9000, OutputTokens: 1000}
	l.must(testkit.NewFakeModel(big), "again")
	if !l.auto.Pending() {
		t.Fatal("a Session whose last call reported 10000 tokens is not pending")
	}
}

// The instruction and the tools count toward the size before a model call, so
// a request whose Messages fit the ceiling can still be past it.
func TestPendingForCountsTheInstructionAndTheTools(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{Ceiling: 500})
	l.must(testkit.NewFakeModel(testkit.Text("short")), "hi")
	if l.auto.PendingFor(gotato.ModelRequest{}) {
		t.Fatal("a short Session is pending")
	}
	request := gotato.ModelRequest{SystemInstructions: strings.Repeat("be careful ", 400)}
	if !l.auto.PendingFor(request) {
		t.Fatal("a long instruction does not make the Session pending")
	}
}
