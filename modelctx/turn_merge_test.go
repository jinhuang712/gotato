package modelctx_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

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
		if result, err := modelctx.Compact(context.Background(), l.writer.Record(), modelctx.CompactConfig{SegmentSettings: modelctx.SegmentSettings{TailTokens: 1}, SegmentBudgetTokens: 1 << 30}); err != nil || !result.Replaced {
			t.Fatalf("Segment %d: %+v, %v", i, result, err)
		}
	}
	l.auto = modelctx.AutoCompact(l.writer.Record(), policy(l.segmentTokens()))
	return l
}

// segmentTokens is what the Segments of the Context hold.
func (l *live) segmentTokens() int {
	total := 0
	for _, segment := range l.writer.Record().Segments() {
		total += modelctx.EstimateTokens(segment.Messages)
	}
	return total
}

func (l *live) segmentCount() int { return len(l.writer.Record().Segments()) }

func (l *live) merged() bool {
	segments := l.writer.Record().Segments()
	return len(segments) > 0 && len(segments[0].Merged) > 0
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
	if !strings.Contains(err.Error(), "nothing was merged") || !strings.Contains(err.Error(), "gateway down") {
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
