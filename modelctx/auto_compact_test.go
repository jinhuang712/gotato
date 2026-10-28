package modelctx_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// reported is a Script that calls tool c<n> and reports usage for its request.
func calling(n string, input, output uint64) testkit.Script {
	script := testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID("c" + n), ToolID: "demo.read", Arguments: []byte(`{}`)})
	script[len(script)-1].Usage = gotato.Usage{InputTokens: input, OutputTokens: output}
	return script
}

// A Run that reaches the ceiling between two Turns is compacted there and
// goes on: the Segment holds what came before, the latest Turn stays with its
// tool call and result, and another instance builds the same request.
func TestARunPastTheCeilingIsCompactedBetweenTurnsAndGoesOn(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{CeilingTokens: 5000})
	l.must(testkit.NewFakeModel(answering("earlier answer")), "an earlier question")
	model := &stepModel{steps: []any{calling("1", 400, 10), calling("2", 9000, 1000), answering("all done")}}
	result, err := l.run(model, "investigate", strings.Repeat("log line ", 350))
	if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != "all done" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if model.calls() != 3 || result.Metrics.Turns != 3 {
		t.Fatalf("%d model calls in %d Turns, want 3 and 3", model.calls(), result.Metrics.Turns)
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionWhyPastCeiling {
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
	if result, err := modelctx.Compact(context.Background(), l.writer.Record(), modelctx.CompactConfig{KeepMessages: 2}); err != nil || !result.Replaced {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	// Two Runs that read a large result each: a Segment keeps a line per call.
	for _, prompt := range []string{"four", "five"} {
		steps := &stepModel{steps: []any{calling(prompt, 300, 10), answering("reply to " + prompt)}}
		if result, err := l.run(steps, prompt, strings.Repeat("log line ", 350)); err != nil || result.Status != gotato.RunStatusCompleted {
			t.Fatalf("%q: %+v, %v", prompt, result, err)
		}
	}
	segmentOne := encode(t, l.writer.Record().Segments()[0].Messages)

	model := &stepModel{steps: []any{refusal{}, answering("fits now")}}
	result, err := l.run(model, "six", "ok")
	if err != nil || result.Status != gotato.RunStatusCompleted || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 2 || got[1] != gotato.CompactionWhyOverflow {
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
	if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Status != gotato.RunStatusFailed || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionWhyOverflow {
		t.Fatalf("compactions = %v, want one overflow", got)
	}
}

// A Turn compacts at most once: a refusal after the Turn compacted past the
// ceiling is not compacted again.
func TestATurnThatCompactedPastTheCeilingIsNotCompactedAgainOnAnOverflow(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{CeilingTokens: 5000})
	l.must(testkit.NewFakeModel(answering("earlier answer")), "an earlier question")
	model := &stepModel{steps: []any{calling("1", 9000, 1000), refusal{}, answering("never")}}
	result, err := l.run(model, "investigate", "ok")
	if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Status != gotato.RunStatusFailed || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionWhyPastCeiling {
		t.Fatalf("compactions = %v, want one past_ceiling", got)
	}
}

// Still past the ceiling after the Turn's compaction, the Run ends with
// limit_exceeded and no further call is made. One prompt too large for the
// ceiling is the cause here: a large tool result is spilled, so it is not.
func TestARunStillPastTheCeilingAfterItsCompactionEndsWithLimitExceeded(t *testing.T) {
	l := newLive(t, modelctx.CompactPolicy{CeilingTokens: 5000})
	l.must(testkit.NewFakeModel(answering("earlier answer")), "an earlier question")
	model := &stepModel{steps: []any{calling("1", 400, 10), answering("never")}}
	result, err := l.run(model, strings.Repeat("a very large prompt ", 2000), "ok")
	if !gotato.IsCode(err, gotato.CodeLimitExceeded) || result.Status != gotato.RunStatusFailed || model.calls() != 0 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if !strings.Contains(err.Error(), "past the ceiling") {
		t.Errorf("the error does not say why: %v", err)
	}
	if got := whyOf(l.writer.Record()); len(got) != 1 || got[0] != gotato.CompactionWhyPastCeiling {
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
		{"a 200K window", modelctx.CompactPolicy{WindowTokens: 200_000}, 180_000},
		{"a small window keeps the reserve", modelctx.CompactPolicy{WindowTokens: 64_000}, 48_000},
		{"a window too small to leave any", modelctx.CompactPolicy{WindowTokens: 10_000}, 0},
		{"the ceiling wins", modelctx.CompactPolicy{WindowTokens: 200_000, CeilingTokens: 1234}, 1234},
		{"a negative ceiling is off", modelctx.CompactPolicy{WindowTokens: 200_000, CeilingTokens: -1}, 0},
	} {
		if got := modelctx.AutoCompact(nil, tc.policy).CeilingTokens(); got != tc.want {
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
	if _, err := modelctx.Compact(context.Background(), l.writer.Record(), modelctx.CompactConfig{KeepMessages: 2}); err != nil {
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
	l := newLive(t, modelctx.CompactPolicy{CeilingTokens: 5000})
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
	l := newLive(t, modelctx.CompactPolicy{CeilingTokens: 500})
	l.must(testkit.NewFakeModel(testkit.Text("short")), "hi")
	if l.auto.PendingFor(gotato.ModelRequest{}) {
		t.Fatal("a short Session is pending")
	}
	request := gotato.ModelRequest{SystemInstructions: strings.Repeat("be careful ", 400)}
	if !l.auto.PendingFor(request) {
		t.Fatal("a long instruction does not make the Session pending")
	}
}

// The automatic compaction at the start of a Run closes a Segment, and the
// Run's request holds it.
func TestAutoCompactClosesASegmentAtRunStart(t *testing.T) {
	f := newFixture(t)
	big := strings.Repeat("log line ", 400)
	for i := range 10 {
		run := fmt.Sprintf("r%d", i)
		f.user(run, "question "+run, i*3)
		m := gotato.Message{Role: gotato.RoleAssistant, RunID: gotato.RunID(run), StopReason: gotato.StopReasonToolCalls,
			ToolCalls: []gotato.ToolCall{{ID: gotato.ToolCallID("c" + run), ToolID: "logs.read", Arguments: []byte(`{}`)}}}
		f.commit(m, i*3+1)
		f.commit(gotato.Message{Role: gotato.RoleToolResult, RunID: gotato.RunID(run), ToolResult: &gotato.ToolResult{CallID: gotato.ToolCallID("c" + run), Status: gotato.ToolResultStatusOK},
			Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: big}}}, i*3+1)
		f.reply(run, "answer "+run, i*3+2)
	}
	before := modelctx.EstimateTokens(f.rec.Messages())
	auto := modelctx.AutoCompact(f.rec, modelctx.CompactPolicy{CeilingTokens: before / 2})
	model := testkit.NewFakeModel(testkit.Text("ok"))
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(f.writer), gotato.WithExtension(auto))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("next")); err != nil {
		t.Fatal(err)
	}
	last, runs := auto.Last()
	if runs != 1 || !last.Replaced || last.Compaction.Segment != 1 || last.TokensAfter >= before {
		t.Fatalf("auto compaction: %+v (%d)", last, runs)
	}
	request, _ := model.LastRequest()
	if !strings.Contains(gotato.TextOf(request.Messages[0]), `<segment n="1"`) {
		t.Fatalf("the request does not start with the Segment: %q", gotato.TextOf(request.Messages[0]))
	}
	if !strings.Contains(request.SystemInstructions, gotato.TagsSentence) {
		t.Error("the request lacks the sentence about the tags")
	}
	// The latest Run stays as it was, tool call and result together.
	var kept []string
	for _, m := range f.rec.Tail() {
		if m.Role == gotato.RoleToolResult {
			kept = append(kept, "result")
		}
		if len(m.ToolCalls) > 0 {
			kept = append(kept, "call")
		}
	}
	if !slices.Equal(kept, []string{"call", "result"}) {
		t.Errorf("the kept tail holds %v", kept)
	}
	// Below the ceiling the next Run does nothing.
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if _, runs := auto.Last(); runs != 1 {
		t.Fatalf("compacted again below the ceiling: %d", runs)
	}
}

// limitedRefusal is a refusal that names the limit of the request.
type limitedRefusal struct{ limit int }

func (limitedRefusal) Error() string         { return "refused: the prompt is too long" }
func (limitedRefusal) ContextOverflow() bool { return true }
func (r limitedRefusal) ContextLimit() int   { return r.limit }

// segmented is a stored Session with n Segments of about 450 tokens each,
// closed on demand and never merged, whose later Runs compact by policy.
func segmented(t *testing.T, n int, policy func(segments int) modelctx.CompactPolicy) *live {
	t.Helper()
	l := newLive(t, modelctx.CompactPolicy{})
	for i := 1; i <= n; i++ {
		l.must(testkit.NewFakeModel(answering(strings.Repeat("y", 800))), fmt.Sprintf("question %d: %s", i, strings.Repeat("x", 800)))
		if result, err := modelctx.Compact(context.Background(), l.writer.Record(), modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{SegmentSettings: modelctx.SegmentSettings{TailTokens: 1}, SegmentBudgetTokens: 1 << 30}}); err != nil || !result.Replaced {
			t.Fatalf("Segment %d: %+v, %v", i, result, err)
		}
	}
	l.auto = modelctx.AutoCompact(l.writer.Record(), policy(l.segmentTokens()))
	return l
}

// Segments past their budget at the start of a Run are merged before its first
// call, with no Segment to close, and the first request opens with the merged
// Segment. A Run under the budget merges nothing.
func TestSegmentsPastTheirBudgetAreMergedBeforeARunsFirstCall(t *testing.T) {
	l := segmented(t, 4, func(tokens int) modelctx.CompactPolicy {
		return modelctx.CompactPolicy{SegmentBudgetTokens: tokens - 100}
	})
	model := &stepModel{steps: []any{answering("hello again"), answering("and again")}}
	result, err := l.run(model, "a new question", "ok")
	if err != nil || result.Status != gotato.RunStatusCompleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if got := whyOf(l.writer.Record()); len(got) != 5 || got[4] != gotato.CompactionWhyPastBudget {
		t.Fatalf("compactions = %v, want four closings and one past_budget", got)
	}
	if !l.merged() || l.segmentCount() >= 4 {
		t.Fatalf("%d Segments, merged first: %v", l.segmentCount(), l.merged())
	}
	first := model.requests[0].Messages
	if !strings.HasPrefix(gotato.TextOf(first[0]), `<segment n="1-`) || gotato.TextOf(first[len(first)-1]) != "a new question" {
		t.Fatalf("the first request opens with %q and ends with %q", gotato.TextOf(first[0]), gotato.TextOf(first[len(first)-1]))
	}
	// Now under the budget: the next Run merges nothing.
	if _, err := l.run(model, "one more", "ok"); err != nil {
		t.Fatal(err)
	}
	if got := whyOf(l.writer.Record()); len(got) != 5 {
		t.Fatalf("compactions = %v, want no more", got)
	}
}

// A merge that fails at the start of a Run is not a step the Run needs: the Run
// goes on with the Segments as they were, and the next Run tries again.
func TestAFailedMergeAtTheStartOfARunLetsTheRunGoOn(t *testing.T) {
	broken := modelctx.ModelRecap{Model: errModel{errors.New("gateway down")}}
	l := segmented(t, 4, func(tokens int) modelctx.CompactPolicy {
		return modelctx.CompactPolicy{SegmentBudgetTokens: tokens - 100, Recap: broken}
	})
	result, err := l.run(testkit.NewFakeModel(answering("fine")), "go on", "ok")
	if err != nil || result.Status != gotato.RunStatusCompleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if l.merged() || l.segmentCount() != 4 {
		t.Fatalf("%d Segments, merged: %v; a failed merge must leave them", l.segmentCount(), l.merged())
	}
	failed := func() (n int) {
		for _, event := range l.writer.Record().Events() {
			if _, ok := event.(gotato.CompactionFailed); ok {
				n++
			}
		}
		return n
	}
	if failed() != 1 {
		t.Errorf("%d compaction_failed Events, want 1", failed())
	}
	if _, err := l.run(testkit.NewFakeModel(answering("still fine")), "and again", "ok"); err != nil || failed() != 2 {
		t.Errorf("the next Run did not try the merge again: err %v, %d failures", err, failed())
	}
}

// A Run past the ceiling closes a Segment and, the Segments being past their
// budget, merges the oldest, in the same Turn, and goes on.
func TestARunPastTheCeilingMergesTheOldestSegmentsToo(t *testing.T) {
	l := segmented(t, 3, func(tokens int) modelctx.CompactPolicy {
		return modelctx.CompactPolicy{CeilingTokens: 5000, SegmentBudgetTokens: tokens + 30}
	})
	model := &stepModel{steps: []any{calling("1", 400, 10), calling("2", 9000, 1000), answering("all done")}}
	result, err := l.run(model, "investigate", strings.Repeat("log line ", 350))
	if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != "all done" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	got := whyOf(l.writer.Record())
	if len(got) != 5 || got[3] != gotato.CompactionWhyPastCeiling || got[4] != gotato.CompactionWhyPastCeiling {
		t.Fatalf("compactions = %v, want three on_demand closings, then a closing and a merge, both past_ceiling", got)
	}
	third := model.requests[2].Messages
	if !strings.HasPrefix(gotato.TextOf(third[0]), `<segment n="1-`) {
		t.Fatalf("the third request opens with %q, want the merged Segment", gotato.TextOf(third[0]))
	}
	n := len(third)
	if third[n-1].Role != gotato.RoleToolResult || third[n-1].ToolResult.CallID != "c2" {
		t.Fatalf("the request does not end with the latest tool result")
	}
}

// overflowFixture is a Session with three Segments under their budget and two
// finished Runs that are not closed yet: the compaction an overflow brings
// closes the first of them, which takes the Segments past their budget.
func overflowFixture(t *testing.T, recap modelctx.RecapWriter) *live {
	t.Helper()
	l := segmented(t, 3, func(tokens int) modelctx.CompactPolicy {
		return modelctx.CompactPolicy{SegmentBudgetTokens: tokens + 60, Recap: recap}
	})
	l.must(testkit.NewFakeModel(answering("reply")), "question 4: "+strings.Repeat("x", 800))
	l.must(testkit.NewFakeModel(answering("reply")), "question 5: "+strings.Repeat("x", 800))
	return l
}

// The provider refuses a request as too long: the Session is compacted, the
// Segments past their budget merge in that same Turn, and the call is sent
// again, once, from a request that opens with the merged Segment.
func TestARefusalMergesTheOldestSegmentsAndSendsTheCallAgain(t *testing.T) {
	l := overflowFixture(t, nil)
	model := &stepModel{steps: []any{refusal{}, answering("fits now")}}
	result, err := l.run(model, "six", "ok")
	if err != nil || result.Status != gotato.RunStatusCompleted || model.calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	got := whyOf(l.writer.Record())
	if len(got) != 5 || got[3] != gotato.CompactionWhyOverflow || got[4] != gotato.CompactionWhyOverflow {
		t.Fatalf("compactions = %v, want three closings, then an overflow closing and an overflow merge", got)
	}
	refused, sent := model.requests[0].Messages, model.requests[1].Messages
	if modelctx.EstimateTokens(sent) >= modelctx.EstimateTokens(refused) || !strings.HasPrefix(gotato.TextOf(sent[0]), `<segment n="1-`) {
		t.Fatalf("the second request has %d tokens against %d, and opens with %q", modelctx.EstimateTokens(sent), modelctx.EstimateTokens(refused), gotato.TextOf(sent[0]))
	}
	if last := gotato.TextOf(sent[len(sent)-1]); last != "six" {
		t.Errorf("the request ends with %q, want the prompt of the Run", last)
	}
}

// The limit a refusal names sizes the merge: Segments well under the default
// budget are merged when they are past the limit the provider named, and a
// refusal that names none, or a bigger limit, leaves them, so the refusal
// stands.
func TestTheLimitARefusalNamesSizesTheMerge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		refusal    error
		wantMerged bool
	}{
		{"names a limit", limitedRefusal{limit: 600}, true},
		{"names none", refusal{}, false},
		{"names a limit above the budget", limitedRefusal{limit: 100000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := segmented(t, 3, func(int) modelctx.CompactPolicy { return modelctx.CompactPolicy{} })
			model := &stepModel{steps: []any{tc.refusal, answering("fits now")}}
			result, err := l.run(model, "four", "ok")
			if tc.wantMerged {
				if err != nil || result.Status != gotato.RunStatusCompleted || model.calls() != 2 {
					t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
				}
			} else if !gotato.IsCode(err, gotato.CodeContextOverflow) || model.calls() != 1 {
				t.Fatalf("err = %v, %d calls; the refusal should stand", err, model.calls())
			}
			if l.merged() != tc.wantMerged {
				t.Fatalf("merged = %v, want %v (%d Segments)", l.merged(), tc.wantMerged, l.segmentCount())
			}
		})
	}
}

// When the Recap of the merge on an overflow fails, nothing is merged and the
// Run ends with context_overflow, so a caller is told rather than left with a
// merge that kept nothing of the far past.
func TestAFailedMergeRecapOnAnOverflowEndsTheRunWithContextOverflow(t *testing.T) {
	l := overflowFixture(t, modelctx.ModelRecap{Model: errModel{errors.New("gateway down")}})
	model := &stepModel{steps: []any{refusal{}, answering("never")}}
	result, err := l.run(model, "six", "ok")
	if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Status != gotato.RunStatusFailed || model.calls() != 1 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if !strings.Contains(err.Error(), "nothing was merged") || !strings.Contains(err.Error(), "the Recap failed") || strings.Contains(err.Error(), "gateway down") {
		t.Errorf("the error does not say why: %v", err)
	}
	for _, segment := range l.writer.Record().Segments() {
		if len(segment.Merged) > 0 {
			t.Errorf("Segment %d is merged although its Recap failed", segment.Number)
		}
	}
	if l.segmentCount() != 4 {
		t.Errorf("%d Segments, want the three and the one the compaction closed", l.segmentCount())
	}
}

// With nothing to close, an overflow still merges the Segments that are past
// the limit.
func TestAnOverflowWithNothingToCloseStillMergesTheSegments(t *testing.T) {
	l := segmented(t, 3, func(int) modelctx.CompactPolicy { return modelctx.CompactPolicy{} })
	model := &stepModel{steps: []any{limitedRefusal{limit: 600}, answering("fits now")}}
	result, err := l.run(model, "four", "ok")
	if err != nil || result.Status != gotato.RunStatusCompleted || !l.merged() {
		t.Fatalf("result = %+v, err = %v, merged %v", result, err, l.merged())
	}
	if got := whyOf(l.writer.Record()); len(got) != 4 || got[3] != gotato.CompactionWhyOverflow {
		t.Fatalf("compactions = %v, want three closings and one overflow merge", got)
	}
}

// A Turn compacts at most once: a Turn that merged at its start is not
// compacted again by an overflow after it.
func TestATurnThatMergedIsNotCompactedAgainOnAnOverflow(t *testing.T) {
	l := segmented(t, 4, func(tokens int) modelctx.CompactPolicy {
		return modelctx.CompactPolicy{SegmentBudgetTokens: tokens - 100}
	})
	model := &stepModel{steps: []any{refusal{}, answering("never")}}
	result, err := l.run(model, "five", "ok")
	if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Status != gotato.RunStatusFailed || model.calls() != 1 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
	}
	if got := whyOf(l.writer.Record()); len(got) != 5 || got[4] != gotato.CompactionWhyPastBudget {
		t.Fatalf("compactions = %v, want four closings and one past_budget merge", got)
	}
}

func thinking(label string) gotato.ContentPart {
	return gotato.ContentPart{Kind: gotato.ContentKindReasoning, Text: "think-" + label, Signature: []byte(`{"type":"thinking","signature":"sig-` + label + `"}`)}
}

// reasoningSession holds two Rounds whose replies all carry reasoning. A
// compaction that keeps the last four Messages keeps the second Round.
func reasoningSession(t *testing.T) *session.Record {
	t.Helper()
	call := gotato.Message{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{thinking("r2")},
		ToolCalls: []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}, StopReason: gotato.StopReasonToolCalls}
	result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusOK},
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "done"}}}
	answer := func(label, text string) gotato.Message {
		return gotato.Message{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{thinking(label), {Kind: gotato.ContentKindText, Text: text}}, StopReason: gotato.StopReasonEndTurn}
	}
	s := session.New()
	for _, message := range []gotato.Message{
		gotato.UserMessage("u1"), answer("r1", "a1"),
		gotato.UserMessage("u2"), call, result, answer("r3", "a2"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func reasoningParts(messages []gotato.Message) int {
	count := 0
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Kind == gotato.ContentKindReasoning {
				count++
			}
		}
	}
	return count
}

// The summary replaces the prefix the kept replies' reasoning was written
// under. A provider that checks that prefix (Claude) may refuse the
// reasoning, and one that is sent only the current Round's reasoning (Claude,
// OpenAI) never needs it again, so the Checkpoint leaves it out. Without
// DropReasoning, as for DeepSeek, which needs every reasoning artifact back,
// it stays.
func TestCompactLeavesTheReasoningOutOfTheKeptMessagesWhenAsked(t *testing.T) {
	ctx := context.Background()
	for _, drop := range []bool{false, true} {
		store := session.NewMemoryStore()
		s := reasoningSession(t)
		writer, err := session.CreateWriter(ctx, store, s, "test")
		if err != nil {
			t.Fatal(err)
		}
		result, err := modelctx.Compact(ctx, writer.Record(), modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{DropReasoning: drop}, KeepMessages: 4})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Release(ctx); err != nil {
			t.Fatal(err)
		}
		if !result.Replaced {
			t.Fatalf("drop=%v: nothing was compacted", drop)
		}
		kept := s.Tail()
		want := 2
		if drop {
			want = 0
		}
		if got := reasoningParts(kept); got != want {
			t.Errorf("drop=%v: the kept Messages hold %d reasoning Parts, want %d", drop, got, want)
		}
		last := kept[len(kept)-1]
		if gotato.TextOf(last) != "a2" || len(kept) != 4 || len(kept[1].ToolCalls) != 1 {
			t.Errorf("drop=%v: compaction changed more than the reasoning: %+v", drop, kept)
		}
		reloaded, err := session.Open(ctx, store, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		if got := reasoningParts(reloaded.Messages()); got != want {
			t.Errorf("drop=%v: after a reload the kept Messages hold %d reasoning Parts, want %d", drop, got, want)
		}
	}
}

func TestAutoCompactLeavesTheReasoningOutWhenItsPolicySays(t *testing.T) {
	s := reasoningSession(t)
	auto := modelctx.AutoCompact(s, modelctx.CompactPolicy{CeilingTokens: 1, DropReasoning: true})
	// A ceiling of one token is never met again, so the Run would end; the
	// compaction itself is what this checks.
	prepared, err := auto.PrepareTurn(context.Background(), gotato.PendingTurn{RunID: "r", Turn: 2})
	if !gotato.IsCode(err, gotato.CodeLimitExceeded) || !prepared.Changed {
		t.Fatalf("prepared = %+v, err = %v", prepared, err)
	}
	if _, runs := auto.Last(); runs != 1 {
		t.Fatalf("auto compactions = %d, want 1", runs)
	}
	if got := reasoningParts(s.Tail()); got != 0 {
		t.Fatalf("the kept Messages hold %d reasoning Parts, want 0", got)
	}
}
