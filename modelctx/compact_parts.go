package modelctx

import (
	"context"

	"github.com/jinhuang712/gotato/gotato"
)

// CeilingTrigger is the default Trigger: a compaction is due when the provider
// refused the request as too long (why: overflow), when the Context is past the
// ceiling in force (past_ceiling), and, on a Run's first Turn, when the Segments
// together are past their budget (past_budget, which merges the oldest and closes
// nothing). It is checked in that order.
type CeilingTrigger struct{}

// Due implements Trigger.
func (CeilingTrigger) Due(_ context.Context, state TurnState) (gotato.CompactionWhy, bool) {
	switch {
	case state.Overflow:
		return gotato.CompactionWhyOverflow, true
	case state.CeilingTokens > 0 && state.Tokens > state.CeilingTokens:
		return gotato.CompactionWhyPastCeiling, true
	case state.Turn == 1 && state.SegmentTokens > state.BudgetTokens:
		// A merge that failed before is tried again here.
		return gotato.CompactionWhyPastBudget, true
	}
	return "", false
}

// TailBudget is the default Cutter. With KeepMessages above 0 the tail is the
// last KeepMessages Messages. Without, it is the latest Run's Turns within
// TailTokens (20K tokens by default), and no more than a quarter of the window,
// or of the ceiling when no window is known.
type TailBudget struct {
	// KeepMessages is the number of most recent Messages to keep in the tail.
	KeepMessages int
	// TailTokens is the most the kept tail holds when KeepMessages is 0, before
	// the quarter of the window (or of the ceiling) caps it.
	TailTokens int
}

// Cut implements Cutter.
func (t TailBudget) Cut(stretch []gotato.Message, info CutInfo) int {
	if t.KeepMessages > 0 {
		return turnCut(stretch, len(stretch)-t.KeepMessages)
	}
	budget := orDefault(t.TailTokens, defaultTailTokens)
	base := info.WindowTokens
	if base <= 0 {
		base = info.CeilingTokens
	}
	if base > 0 {
		budget = min(budget, base/4)
	}
	return turnCut(stretch, withinBudget(stretch, budget, runStart(stretch, info.CurrentRun)))
}

// OldestToHalf is the default Picker: it takes the oldest Segments until the
// ones left fit in half the budget, and at least two, so a merge always
// shortens the Context.
type OldestToHalf struct{}

// Pick implements Picker.
func (OldestToHalf) Pick(segments []gotato.Segment, budgetTokens int) int {
	if len(segments) < 2 {
		return 0
	}
	sizes := make([]int, len(segments))
	total := 0
	for i, segment := range segments {
		sizes[i] = EstimateTokens(segment.Messages)
		total += sizes[i]
	}
	if total <= budgetTokens {
		return 0
	}
	count, left := len(segments), 0
	for count > 2 && left+sizes[count-1] <= budgetTokens/2 {
		count--
		left += sizes[count]
	}
	return count
}
