package gotato

import (
	"maps"
	"time"
)

// turnRun is one Turn as it goes: what its reply cost and said.
type turnRun struct {
	turn      TurnNumber
	started   time.Time
	assistant Message
	usage     Usage
	text      uint64 // bytes of text in the reply
	reasoning uint64 // bytes of reasoning in the reply
}

// turnEnded is the TurnEnded Event of the Turn, which stopped for stop.
func (t *turnRun) turnEnded(st *runState, stop StopReason) TurnEnded {
	return TurnEnded{
		Header: st.header(t.turn), StopReason: stop, Usage: t.usage,
		ElapsedMS: millis(time.Since(t.started)), TextBytes: t.text, ReasoningBytes: t.reasoning,
	}
}

// afterModelError settles a Turn whose Model call failed: an Interrupt cut it
// short and the Run goes on, or the Run fails with the Model's error.
func (t *turnRun) afterModelError(st *runState, modelErr *RuntimeError) (bool, RunResult, error) {
	a, hd := st.a, st.header
	if modelErr.Code == CodeCanceled && st.runCtx.Err() == nil && a.queue.interrupting() {
		// An Interrupt cut the call short: the text it had sent is kept as
		// a Cut-off reply, and the Interrupt starts the next Turn.
		if t.assistant.StopReason == StopReasonCutOff && a.commitMessage(t.assistant) == nil {
			a.stage(ReplyCutOff{Header: hd(t.turn), Message: t.assistant.Clone(), Cause: modelErr.Code})
		}
		a.stage(t.turnEnded(st, StopReasonCutOff))
		if _, controlErr := a.consumeControl(st.runCtx, st.runID, t.turn, a.queue.takeBetweenTurns()); controlErr != nil {
			return st.failTurn(controlErr)
		}
		return true, RunResult{}, nil
	}
	// The text the Model sent before its stream broke is committed, so the
	// Transcript keeps it; the Run still fails as it did.
	if t.assistant.StopReason == StopReasonCutOff && a.commitMessage(t.assistant) == nil {
		// The Run fails with the model's error either way; a cut-off reply
		// that cannot be written fails the Run's last write.
		a.stage(ReplyCutOff{Header: hd(t.turn), Message: t.assistant.Clone(), Cause: modelErr.Code})
		_ = a.flush(st.runCtx)
	}
	return st.failTurn(modelErr)
}

// endAnswerTurn ends a Turn whose reply asked for no Tool: the Run settles on
// it unless a TurnStopper, a queued message or the output limit says
// otherwise. replyNumber is the number of the reply's Event.
func (t *turnRun) endAnswerTurn(st *runState, replyNumber uint64) (bool, RunResult, error) {
	a, turn, assistant := st.a, t.turn, t.assistant
	a.stage(t.turnEnded(st, assistant.StopReason))
	// A TurnStopper runs after TurnEnded and before continuation
	// selection. A stop preserves the Turn and settles the Run.
	decision, stopErr := t.stopTurn(st, nil, assistant.StopReason)
	if stopErr != nil {
		return st.failTurn(stopErr)
	}
	if decision.Stop {
		return st.settleTurn(turn, assistant, decision)
	}
	// Safe boundary at settlement: Steer and FollowUp both apply here.
	// Cancellation is checked first so a queued continuation is never
	// committed to a Run that is already ending.
	injected, controlErr := t.settlementBoundary(st)
	if controlErr != nil {
		return st.failTurn(controlErr)
	}
	if injected {
		return true, RunResult{}, nil
	}
	// A Turn cut off at the output limit before any Tool Call is not an
	// answer: the Model ran out of room, often while reasoning. Within the
	// configured bound the Run continues instead of settling on it.
	if assistant.StopReason == StopReasonMaxTokens && st.truncatedContinuations < a.def.limits.MaxTruncatedContinuations {
		st.truncatedContinuations++
		// A notice, not a user Message: it starts no Round, so the
		// reasoning of the cut reply stays in the request.
		notice := NewNotice(NoticeSourceGotato, NoticeContinue, TruncatedContinuationText)
		notice.Notice.CausedBy = replyNumber
		if err := a.commitNotice(st.runCtx, st.runID, turn, control{message: notice, source: MessageSourceNotice, kind: kindNotice}, nil); err != nil {
			return st.failTurn(err)
		}
		return true, RunResult{}, nil
	}
	return st.settleTurn(turn, assistant, StopDecision{})
}

// endToolTurn runs the Tool calls of a Turn's reply and ends the Turn: the Run
// goes on to the next Turn unless a TurnStopper stops it.
func (t *turnRun) endToolTurn(st *runState) (bool, RunResult, error) {
	a, turn, assistant := st.a, t.turn, t.assistant
	toolOutcomes, settledResults, failure := t.runToolBatch(st)
	if failure != nil {
		return st.failTurn(failure)
	}
	turnEnded := t.turnEnded(st, StopReasonToolCalls)
	turnEnded.ToolCalls, turnEnded.Tools = len(assistant.ToolCalls), toolOutcomes
	a.stage(turnEnded)
	decision, stopErr := t.stopTurn(st, settledResults, StopReasonToolCalls)
	if stopErr != nil {
		return st.failTurn(stopErr)
	}
	if decision.Stop {
		return st.settleTurn(turn, assistant, decision)
	}
	// Safe boundary between Turns: Steer applies, FollowUp waits for
	// settlement.
	if _, controlErr := t.boundary(st); controlErr != nil {
		return st.failTurn(controlErr)
	}
	return true, RunResult{}, nil
}

// boundary is the safe boundary after a Turn when the Run goes on: Steer
// applies. Cancellation is checked first so a queued continuation is never
// committed to a Run that is already ending.
func (t *turnRun) boundary(st *runState) (bool, *RuntimeError) {
	if err := contextFailure(st.runCtx, "continuation"); err != nil {
		return false, err
	}
	return st.a.consumeControl(st.runCtx, st.runID, t.turn, st.a.queue.takeBetweenTurns())
}

// settlementBoundary is boundary when the Run is settling: Steer and FollowUp
// apply.
func (t *turnRun) settlementBoundary(st *runState) (bool, *RuntimeError) {
	if err := contextFailure(st.runCtx, "continuation"); err != nil {
		return false, err
	}
	return st.a.consumeControl(st.runCtx, st.runID, t.turn, st.a.queue.takeAtSettlement())
}

// runToolBatch runs the Tool calls of the Turn's reply: it checks them in
// source order, runs them in groups, commits their results in source order
// and commits the Tools they loaded. It returns what the calls came to. A
// non-nil failure is an error of the Run.
func (t *turnRun) runToolBatch(st *runState) (toolOutcomes []ToolOutcome, settledResults []ToolResult, failure *RuntimeError) {
	a := st.a
	// Preflight is source ordered: resolve, validate, and run the Pre
	// chain before any executor starts.
	plans, planErr := a.preflightTools(st.runCtx, st.runID, t.turn, t.assistant.ToolCalls, &st.toolCalls)
	if planErr != nil {
		return nil, nil, planErr
	}
	// Results commit in source order, as soon as every call before them
	// has settled: after each group runs, and for blocked calls, which
	// settled at preflight. So a crash loses at most the group in flight.
	commits := toolCommits{
		plans:          plans,
		outcomes:       t.settleBlocked(st, plans),
		toolOutcomes:   make([]ToolOutcome, 0, len(plans)),
		settledResults: make([]ToolResult, 0, len(plans)),
	}
	// Execution may be sequential or bounded parallel. Completion Events
	// follow actual completion order; commitment stays source ordered.
	for _, group := range groupPlans(plans, a.parallelWorkers()) {
		if err := t.runGroup(st, plans, group, commits.outcomes); err != nil {
			return nil, nil, err
		}
		if err := commits.upTo(st, t.turn, group[len(group)-1]+1); err != nil {
			return nil, nil, err
		}
	}
	if err := commits.upTo(st, t.turn, len(plans)); err != nil {
		return nil, nil, err
	}
	st.batch = openBatch{}
	return commits.toolOutcomes, commits.settledResults, nil
}

// settleBlocked returns the outcomes of the batch, holding those of the
// blocked calls of plans, and stages their ToolEnded.
func (t *turnRun) settleBlocked(st *runState, plans []toolPlan) map[int]ToolResult {
	a, hd, turn := st.a, st.header, t.turn
	// A blocked Tool is already settled at preflight: it ends without
	// having started.
	outcomes := make(map[int]ToolResult, len(plans))
	st.batch.outcomes = outcomes
	for i := range plans {
		if plans[i].blocked {
			outcomes[i] = plans[i].result
			a.stage(ToolEnded{Header: hd(turn), CallID: plans[i].call.ID, ToolID: plans[i].call.ToolID, Status: plans[i].result.Status, Code: plans[i].result.Code, Executed: plans[i].result.Executed})
		}
	}
	return outcomes
}

// runGroup runs one group of the calls of plans and adds what they came to
// to outcomes.
func (t *turnRun) runGroup(st *runState, plans []toolPlan, group []int, outcomes map[int]ToolResult) *RuntimeError {
	a, runCtx, hd, turn := st.a, st.runCtx, st.header, t.turn
	// The calls are written before they run, so a crash during them
	// leaves calls that are known to have started.
	for _, index := range group {
		a.stage(ToolStarted{Header: hd(turn), MessageID: t.assistant.ID, CallID: plans[index].call.ID, ToolID: plans[index].call.ToolID})
	}
	if err := a.flush(runCtx); err != nil {
		return err
	}
	completed := a.executeToolGroup(runCtx, st.runID, turn, plans, group)
	// A canceled group still reports what it knows, so the closing
	// results of the failed Run keep Executed true for started Tools.
	maps.Copy(outcomes, completed)
	return nil
}

// toolCommits commits the results of a Tool batch in source order, and
// gathers what they came to.
type toolCommits struct {
	plans          []toolPlan
	outcomes       map[int]ToolResult
	committed      int // the calls whose results are committed
	toolOutcomes   []ToolOutcome
	settledResults []ToolResult
}

// upTo commits the results of the calls of the Turn turn before end that
// are not committed yet.
func (c *toolCommits) upTo(st *runState, turn TurnNumber, end int) *RuntimeError {
	for ; c.committed < end; c.committed++ {
		toolResult, err := st.commitResult(turn, c.committed, c.plans[c.committed].call, c.outcomes[c.committed])
		if err != nil {
			return err
		}
		call := c.plans[c.committed].call
		c.settledResults = append(c.settledResults, toolResult.Clone())
		c.toolOutcomes = append(c.toolOutcomes, ToolOutcome{ToolID: call.ToolID, CallID: call.ID, Status: toolResult.Status, Executed: toolResult.Executed})
	}
	return nil
}

// stopTurn asks the TurnStoppers whether the Run stops after this Turn.
func (t *turnRun) stopTurn(st *runState, results []ToolResult, reason StopReason) (StopDecision, *RuntimeError) {
	return st.a.def.extensions.stopTurn(st.runCtx, TurnSnapshot{
		SessionID: st.a.def.id, RunID: st.runID, Turn: t.turn, Assistant: t.assistant.Clone(), ToolResults: results, StopReason: reason,
	})
}
