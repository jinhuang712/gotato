package modelctx

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// AutoCompactor is a gotato.TurnPreparer that applies a CompactPolicy to one
// Session. Install it with gotato.WithExtension next to WithTranscript(s).
//
// Before every model call it compacts the Session when its size is past the
// ceiling (why: past_ceiling), and when the provider refused the request as too
// long (why: overflow). A Turn compacts at most once, whatever the trigger. A
// Turn that is still past the ceiling after it ends the Run with
// an error of CodeLimitExceeded; a Turn whose request is still refused ends
// it with CodeContextOverflow.
type AutoCompactor struct {
	session *session.Record
	policy  CompactPolicy
	last    Result
	runs    int
	// turn is the Turn that has compacted, if any.
	turn      turnKey
	compacted bool
}

type turnKey struct {
	run  gotato.RunID
	turn gotato.TurnNumber
}

// AutoCompact creates an AutoCompactor.
func AutoCompact(s *session.Record, policy CompactPolicy) *AutoCompactor {
	return &AutoCompactor{session: s, policy: policy}
}

// PrepareTurn implements gotato.TurnPreparer. A compaction helps a Run but is
// not a step it needs, so one that cannot be written is dropped (its Events
// say so) and the Run goes on, unless the Context is still past the ceiling.
// A Record the compaction refuses, or a Run stopped during the compaction,
// ends the Run.
func (a *AutoCompactor) PrepareTurn(ctx context.Context, pending gotato.PendingTurn) (gotato.TurnPrepared, error) {
	if a.session == nil {
		return gotato.TurnPrepared{}, nil
	}
	if err := a.policy.Validate(); err != nil {
		return gotato.TurnPrepared{}, err
	}
	if key := (turnKey{pending.RunID, pending.Turn}); key != a.turn {
		a.turn, a.compacted = key, false
	}
	if pending.Overflow {
		if a.compacted {
			// The Turn compacted already: the refusal stands.
			return gotato.TurnPrepared{}, nil
		}
		prepared, _, err := a.compact(ctx, pending, gotato.CompactionWhyOverflow)
		return prepared, err
	}
	ceiling := a.CeilingTokens()
	overhead := requestOverhead(pending.Request)
	if ceiling <= 0 || a.size(a.session.Messages(), overhead) <= ceiling {
		if pending.Turn == 1 {
			// Not past the ceiling, but the Segments may be past their
			// budget: a merge that failed before is tried again here.
			return a.mergeOnly(ctx, pending, gotato.CompactionWhyPastBudget, a.policy.effectiveBudget())
		}
		return gotato.TurnPrepared{}, nil
	}
	prepared, dropped, err := a.compact(ctx, pending, gotato.CompactionWhyPastCeiling)
	if err != nil {
		return prepared, err
	}
	// After a compaction the old usage no longer counts: the size is
	// estimated afresh from what the Context is now.
	if fresh := EstimateTokens(a.session.Messages()) + overhead; fresh > ceiling {
		why := "the Context is still past the ceiling after its compaction"
		switch {
		case dropped:
			why = "the Context is past the ceiling and its compaction failed"
		case !prepared.Changed:
			why = "the Context is past the ceiling and there is nothing to compact"
		}
		return prepared, gotato.ErrorOf(gotato.CodeLimitExceeded, fmt.Sprintf("%s (about %d tokens, ceiling %d)", why, fresh, ceiling))
	}
	return prepared, nil
}

// compact closes a Segment before the model call, once per Turn. dropped says
// the compaction was attempted and could not be written.
func (a *AutoCompactor) compact(ctx context.Context, pending gotato.PendingTurn, why gotato.CompactionWhy) (prepared gotato.TurnPrepared, dropped bool, err error) {
	// What the Run has staged is written first, so the compaction's Events
	// follow it.
	if pending.Flush != nil {
		if err := pending.Flush(ctx); err != nil {
			return prepared, false, err
		}
	}
	a.compacted = true
	options := compactCall{
		CompactConfig: CompactConfig{CompactPolicy: a.policy},
		why:           why, keepLastTurn: true,
	}
	// A Recap read from the request the Run is about to send is read from
	// the provider's cache. On an overflow that request is the one just
	// refused, and the ask would only make it longer: the writer gets no
	// Context, and a Recap that needs one is skipped (Compaction.RecapError).
	if why != gotato.CompactionWhyOverflow {
		options.context = func(context.Context) (gotato.ModelRequest, error) { return pending.Request, nil }
	}
	if pending.Turn == 1 {
		options.currentRun = pending.RunID
	}
	// The budget of the Segments is half the ceiling, or the default. On an
	// overflow the limit the refusal names, if any, sizes it too.
	budget := a.policy.effectiveBudget()
	if limit := refusalLimit(pending.Refusal); why == gotato.CompactionWhyOverflow && limit > 0 {
		budget = min(budget, limit)
	}
	options.SegmentBudgetTokens = budget
	result, err := runCompact(ctx, a.session, options)
	if err != nil {
		if ctx.Err() != nil {
			return prepared, false, err
		}
		return prepared, true, nil
	}
	if result.Replaced {
		a.last = result
		a.runs++
	}
	prepared.Changed = result.Replaced
	merge := result.Merge
	if !result.Replaced {
		// Nothing to close, but the Segments may still pass their budget.
		merged, err := mergeOldest(ctx, a.session, MergeConfig{
			BudgetTokens: budget, CeilingTokens: a.CeilingTokens(), Recap: a.policy.Recap, Why: why, Location: a.policy.Location,
		})
		if err != nil {
			return prepared, false, err
		}
		merge = &merged
		prepared.Changed = merged.Merged
	}
	if why == gotato.CompactionWhyOverflow && merge != nil && merge.Error != "" {
		// A merge that could not be written leaves the far past as it was:
		// the caller is told, not left with a request that is refused again.
		return prepared, false, gotato.ErrorOf(gotato.CodeContextOverflow,
			fmt.Sprintf("the provider refused the request as too long, the Segments alone pass their limit (about %d tokens, limit %d), and merging the oldest failed, so nothing was merged: %s",
				merge.SegmentTokens, merge.BudgetTokens, merge.Error))
	}
	return prepared, false, nil
}

// mergeOnly merges the oldest Segments when they pass budget, with no Segment
// to close. A merge that fails leaves them as they are and the Run goes on.
func (a *AutoCompactor) mergeOnly(ctx context.Context, pending gotato.PendingTurn, why gotato.CompactionWhy, budget int) (gotato.TurnPrepared, error) {
	var tokens int
	for _, segment := range a.session.Segments() {
		tokens += EstimateTokens(segment.Messages)
	}
	if tokens <= budget {
		return gotato.TurnPrepared{}, nil
	}
	if pending.Flush != nil {
		if err := pending.Flush(ctx); err != nil {
			return gotato.TurnPrepared{}, err
		}
	}
	a.compacted = true
	merge, err := mergeOldest(ctx, a.session, MergeConfig{
		BudgetTokens: budget, CeilingTokens: a.CeilingTokens(), Recap: a.policy.Recap, Why: why, Location: a.policy.Location,
	})
	return gotato.TurnPrepared{Changed: merge.Merged}, err
}

// refusalLimit returns the limit in tokens that the error refusing a request
// names, or 0 when it names none. The gateway's Error reads it from the
// provider's wording (ContextLimit).
func refusalLimit(refusal error) int {
	var named interface{ ContextLimit() int }
	if refusal != nil && errors.As(refusal, &named) {
		return max(named.ContextLimit(), 0)
	}
	return 0
}

// Last returns the most recent automatic compaction and how many happened.
func (a *AutoCompactor) Last() (Result, int) { return a.last, a.runs }

// Pending reports whether the next Run compacts the Session before it sends
// its first request, because the Session's size is past the ceiling. It counts
// the Messages alone; PendingFor counts the request as the Loop does.
func (a *AutoCompactor) Pending() bool {
	return a.PendingFor(gotato.ModelRequest{})
}

// PendingFor is Pending for the request the next model call would send, whose
// instruction and tools count toward the size as they do before a model call.
func (a *AutoCompactor) PendingFor(request gotato.ModelRequest) bool {
	ceiling := a.CeilingTokens()
	return a.session != nil && ceiling > 0 && a.size(a.session.Messages(), requestOverhead(request)) > ceiling
}

// CeilingTokens returns the estimated-token size past which a Session compacts
// first (CompactPolicy.EffectiveCeilingTokens); 0 means this trigger is off.
func (a *AutoCompactor) CeilingTokens() int { return a.policy.EffectiveCeilingTokens() }

// size is the Context's size in tokens: the input and output tokens the last
// model call reported, plus an estimate of what came after it. With no reported
// usage it is an estimate of everything, and overhead, the instruction and the
// tools, which a reported input count already holds. A compaction clears the
// usage of the Messages it keeps, so after it the size is estimated afresh.
func (a *AutoCompactor) size(messages []gotato.Message, overhead int) int {
	for i, message := range slices.Backward(messages) {
		usage := message.Usage
		if message.Role == gotato.RoleAssistant && usage.InputTokens+usage.OutputTokens > 0 {
			return int(usage.InputTokens+usage.OutputTokens) + EstimateTokens(messages[i+1:])
		}
	}
	return EstimateTokens(messages) + overhead
}
