package modelctx

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jinhuang712/gotato/gotato"
)

// AutoCompactor is a gotato.TurnPreparer that runs a Compactor over one
// SegmentStore. Install it with gotato.WithExtension next to WithTranscript(s).
//
// Before every model call it asks the Compactor's Trigger whether a compaction
// is due: by default past the ceiling (why: past_ceiling), when the provider
// refused the request as too long (why: overflow), and, on a Run's first Turn,
// when the Segments are past their budget (why: past_budget). A Turn compacts at
// most once, whatever the Trigger says: once a compaction or a merge has
// started in it, even one that was dropped. A Turn that is still past the
// ceiling after its compaction ends the Run with an error of
// CodeLimitExceeded; a Turn whose request is still refused ends it with
// CodeContextOverflow.
type AutoCompactor struct {
	store     SegmentStore
	compactor Compactor
	last      Result
	runs      int
	// turn is the Turn being prepared.
	turn turnKey
	// started says a compaction or a merge started in turn (it emitted
	// compaction_started), whether or not it was written. A check that found
	// nothing to close or merge did not start one.
	started bool
}

type turnKey struct {
	run  gotato.RunID
	turn gotato.TurnNumber
}

// AutoCompact creates an AutoCompactor that runs c over store.
func AutoCompact(store SegmentStore, c Compactor) *AutoCompactor {
	return &AutoCompactor{store: store, compactor: c}
}

// PrepareTurn implements gotato.TurnPreparer. A compaction helps a Run but is
// not a step it needs, so one that cannot be written, or whose part fails or
// panics, is dropped (its Events say so) and the Run goes on, unless the
// Context is still past the ceiling. A Record the compaction refuses, or a Run
// stopped during the compaction, ends the Run.
func (a *AutoCompactor) PrepareTurn(ctx context.Context, pending gotato.PendingTurn) (gotato.TurnPrepared, error) {
	if isNilStore(a.store) {
		return gotato.TurnPrepared{}, nil
	}
	if err := a.compactor.Validate(); err != nil {
		return gotato.TurnPrepared{}, err
	}
	if key := (turnKey{pending.RunID, pending.Turn}); key != a.turn {
		a.turn, a.started = key, false
	}
	if a.started {
		// [CONTRACT] a Turn compacts at most once (specs/compaction.md): the refusal stands.
		return gotato.TurnPrepared{}, nil
	}
	ceiling := a.CeilingTokens()
	overhead := requestOverhead(pending.Request)
	segments := a.store.Segments()
	state := TurnState{
		Run: pending.RunID, Turn: pending.Turn, Overflow: pending.Overflow,
		Tokens:        a.size(slices.Concat(flatten(segments), a.store.Tail()), overhead),
		CeilingTokens: ceiling, BudgetTokens: a.compactor.effectiveBudget(),
	}
	if pending.Turn == 1 {
		state.SegmentTokens = EstimateTokens(flatten(segments))
	}
	why, due := a.due(ctx, state)
	if !due {
		return gotato.TurnPrepared{}, nil
	}
	if why == gotato.CompactionWhyPastBudget {
		// Not past the ceiling, but the Segments are past their budget: a
		// merge that failed before is tried again here.
		return a.mergeOnly(ctx, pending, why)
	}
	prepared, dropped, err := a.compact(ctx, pending, why)
	if err != nil || pending.Overflow || ceiling <= 0 {
		return prepared, err
	}
	// After a compaction the old usage no longer counts: the size is
	// estimated afresh from what the Context is now.
	if fresh := EstimateTokens(slices.Concat(flatten(a.store.Segments()), a.store.Tail())) + overhead; fresh > ceiling {
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

// due asks the Trigger. One that panics, or says a compaction is due without
// saying why, drops the Turn's compaction: an extension_failed Event says so and
// the Run goes on.
func (a *AutoCompactor) due(ctx context.Context, state TurnState) (why gotato.CompactionWhy, due bool) {
	trigger := a.compactor.trigger()
	err := guardPanic("Trigger", func() { why, due = trigger.Due(ctx, state) })
	if err == nil && due && why == "" {
		err = fmt.Errorf("%w: the Trigger says a compaction is due and not why", ErrBadPart)
	}
	if err == nil {
		return why, due
	}
	a.started = true
	a.store.Emit(gotato.ExtensionFailed{Header: sessionHeader(a.store), Stage: "compaction_trigger", Code: causeOf(err)})
	return "", false
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
	call := compactCall{Compactor: a.compactor, why: why, keepLastTurn: true, run: pending.RunID}
	// A Recap read from the request the Run is about to send is read from
	// the provider's cache. On an overflow that request is the one just
	// refused, and the ask would only make it longer: the writer gets no
	// Context, and a Recap that needs one is skipped (Compaction.RecapError).
	if !pending.Overflow && why != gotato.CompactionWhyOverflow {
		call.context = func(context.Context) (gotato.ModelRequest, error) { return pending.Request, nil }
	}
	if pending.Turn == 1 {
		call.currentRun = pending.RunID
	}
	// The budget of the Segments is half the ceiling, or the default. On an
	// overflow the limit the refusal names, if any, sizes it too.
	budget := a.compactor.effectiveBudget()
	if limit := refusalLimit(pending.Refusal); pending.Overflow && limit > 0 {
		budget = min(budget, limit)
	}
	call.SegmentBudgetTokens = budget
	result, err := runCompact(ctx, a.store, call)
	// A compaction that was dropped counts as the Turn's; one that found
	// nothing to close does not.
	a.started = err != nil || result.Replaced
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
		merged, err := mergeOldest(ctx, a.store, call.Compactor, budget, why)
		a.started = a.started || merged.Merged || merged.Error != ""
		if err != nil {
			return prepared, false, err
		}
		merge = &merged
		prepared.Changed = merged.Merged
	}
	if pending.Overflow && merge != nil && merge.Error != "" {
		// A merge that could not be written leaves the far past as it was:
		// the caller is told, not left with a request that is refused again.
		return prepared, false, gotato.ErrorOf(gotato.CodeContextOverflow,
			fmt.Sprintf("the provider refused the request as too long, the Segments alone pass their limit (about %d tokens, limit %d), and merging the oldest failed, so nothing was merged: %s",
				merge.SegmentTokens, merge.BudgetTokens, merge.Error))
	}
	return prepared, false, nil
}

// mergeOnly merges the oldest Segments when they pass their budget, with no
// Segment to close. A merge that fails leaves them as they are and the Run goes
// on.
func (a *AutoCompactor) mergeOnly(ctx context.Context, pending gotato.PendingTurn, why gotato.CompactionWhy) (gotato.TurnPrepared, error) {
	if pending.Flush != nil {
		if err := pending.Flush(ctx); err != nil {
			return gotato.TurnPrepared{}, err
		}
	}
	merge, err := mergeOldest(ctx, a.store, a.compactor, a.compactor.effectiveBudget(), why)
	// A merge that found nothing to merge did not start one.
	a.started = merge.Merged || merge.Error != ""
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
// It asks the Trigger as a Turn after the first would, so a merge of Segments
// past their budget is not pending.
func (a *AutoCompactor) PendingFor(request gotato.ModelRequest) bool {
	ceiling := a.CeilingTokens()
	if isNilStore(a.store) || ceiling <= 0 {
		return false
	}
	state := TurnState{
		Turn: 2, CeilingTokens: ceiling, BudgetTokens: a.compactor.effectiveBudget(),
		Tokens: a.size(slices.Concat(flatten(a.store.Segments()), a.store.Tail()), requestOverhead(request)),
	}
	trigger := a.compactor.trigger()
	var why gotato.CompactionWhy
	var due bool
	if err := guardPanic("Trigger", func() { why, due = trigger.Due(context.Background(), state) }); err != nil {
		return false
	}
	return due && why != gotato.CompactionWhyPastBudget
}

// CeilingTokens returns the estimated-token size past which a Session compacts
// first (CompactPolicy.EffectiveCeilingTokens); 0 means this trigger is off.
func (a *AutoCompactor) CeilingTokens() int { return a.compactor.EffectiveCeilingTokens() }

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
