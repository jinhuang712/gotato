package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// toolPlan is one preflighted Tool Call. Preflight is always source ordered,
// so resolution, validation, and the Pre chain stay deterministic no matter
// how the batch executes.
type toolPlan struct {
	index      int
	call       ToolCall
	tool       Tool
	use        ToolUse
	sequential bool
	// blocked marks a call settled at preflight without executing: blocked
	// by a PreToolUse extension, or rejected as unknown or invalid.
	blocked bool
	result  ToolResult
}

// openBatch is the Tool batch of a committed assistant Message whose calls do
// not all have a committed result yet. Results commit in source order, so the
// calls from next on are the open ones. outcomes holds what is known of them
// by source index.
type openBatch struct {
	turn     TurnNumber
	calls    []ToolCall
	next     int
	outcomes map[int]ToolResult
}

// closeOpenCalls commits a closing result for every open call of a Run that
// ends with cause. Every provider refuses a request in which a Tool call has
// no result, so a Transcript left with an open call could never run again.
// The closing results are exempt from the Message and transcript limits,
// because the Run may be ending on one of them. A call keeps Executed true when
// its executor's outcome had already arrived.
func (a *Session) closeOpenCalls(runID RunID, batch *openBatch, cause *RuntimeError, status RunStatus) {
	if batch.next >= len(batch.calls) {
		*batch = openBatch{}
		return
	}
	resultStatus := ToolResultStatusFailed
	if status == RunStatusCanceled || status == RunStatusDeadlineExceeded {
		resultStatus = ToolResultStatusCanceled
	}
	text := fmt.Sprintf("%s: the Run ended before this call's result was committed", cause.Code)
	for i := batch.next; i < len(batch.calls); i++ {
		call := batch.calls[i]
		outcome, known := batch.outcomes[i]
		result := ToolResult{
			CallID:    call.ID,
			Status:    resultStatus,
			Content:   []ContentPart{{Kind: ContentKindText, Text: text}},
			SafeError: text,
			Executed:  known && outcome.Executed,
		}
		message := Message{ID: MessageID(nextID("message")), RunID: runID, Role: RoleToolResult, Parts: cloneParts(result.Content), ToolResult: cloneToolResultPtr(result)}
		if err := a.history.commit(a.def.limits, message, false); err != nil {
			break
		}
		a.stage(ToolResultCommitted{Header: NewHeader(a.def.id, runID, batch.turn), Message: message.Clone(), CallID: call.ID, Status: resultStatus})
	}
	*batch = openBatch{}
}

// rejectedPlan settles a call the Model got wrong as a failed, unexecuted
// Tool Result carrying a message the Model can act on. loaded lists the Tools
// the call loaded, if any.
func (a *Session) rejectedPlan(index int, call ToolCall, code ErrorCode, message string, loaded ...string) toolPlan {
	return toolPlan{
		index:   index,
		call:    call,
		blocked: true,
		result: ToolResult{
			CallID:      call.ID,
			Status:      ToolResultStatusFailed,
			Content:     []ContentPart{{Kind: ContentKindText, Text: message}},
			SafeError:   message,
			Code:        code,
			Executed:    false,
			LoadedTools: loaded,
		},
	}
}

// unresolvedToolText is the failed Result of a call to a Tool the Model cannot
// call. A Tool that exists but the Effect cap hides is said to be not allowed
// in this Session, and a Tool of a ToolSet that is unavailable now is said to
// be so; any other name is an unknown tool.
func (a *Session) unresolvedToolText(id string) string {
	if set := a.tools.registry.unavailableSet(id); set != "" {
		return fmt.Sprintf("%s: tool %q is in ToolSet %q, which is unavailable now; try again in a later Run", CodeToolResolutionFailure, id, set)
	}
	if effect, capped := a.tools.registry.cappedEffect(id); capped {
		if effect == ToolEffectUnspecified {
			effect = "unspecified, which counts as destructive"
		}
		return fmt.Sprintf("%s: tool %q is not allowed in this Session (its effect is %s; the Session's maximum is %s); available tools: %s",
			CodeToolResolutionFailure, id, effect, a.tools.registry.maxEffect, a.visibleToolIDs())
	}
	return fmt.Sprintf("%s: unknown tool %q; available tools: %s", CodeToolResolutionFailure, id, a.visibleToolIDs())
}

// visibleToolIDs lists the Tools the Model was shown this Turn, for the
// message of a call that did not resolve.
func (a *Session) visibleToolIDs() string {
	specs := a.tools.registry.visibleSpecs()
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}

// toolSignal carries one worker's report back to the Session goroutine, which
// is the only place Events are staged. A report is a progress text (update) or
// the final result of the call at plan index index.
type toolSignal struct {
	index  int
	update bool
	text   string
	result ToolResult
}

// parallelWorkers turns the configured bound into a worker count. Zero and one
// both mean sequential.
func (a *Session) parallelWorkers() int {
	if a.def.limits.MaxParallelTools <= 1 {
		return 1
	}
	return int(a.def.limits.MaxParallelTools)
}

// groupPlans splits a batch into execution groups. A Tool declared Sequential
// runs alone; consecutive parallel Tools share a group bounded by the worker
// limit. A plan settled at preflight (blocked) is in no group. The groups hold
// plan indexes.
func groupPlans(plans []toolPlan, workers int) [][]int {
	groups := make([][]int, 0, len(plans))
	current := make([]int, 0, workers)
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = make([]int, 0, workers)
		}
	}
	for i := range plans {
		if plans[i].blocked {
			continue
		}
		if plans[i].sequential || workers == 1 {
			flush()
			groups = append(groups, []int{i})
			continue
		}
		current = append(current, i)
		if len(current) == workers {
			flush()
		}
	}
	flush()
	return groups
}

// executeToolGroup runs one group of admitted Tools and returns their outcomes
// by plan index. A ToolEnded is staged as each outcome becomes known, so they
// follow actual completion order while the Session goroutine waits here;
// results commit in source order later.
func (a *Session) executeToolGroup(
	ctx context.Context,
	runID RunID,
	turn TurnNumber,
	plans []toolPlan,
	group []int,
) map[int]ToolResult {
	run := &toolGroupRun{
		a: a, ctx: ctx, runID: runID, turn: turn, plans: plans, group: group,
		outcomes: make(map[int]ToolResult, len(group)),
		launched: time.Now(),
	}
	// A Run canceled before this group started never launches it: a Tool that
	// ignores its context would still carry out its side effect.
	if ctx.Err() != nil {
		return run.cancelRest(false)
	}
	signals := make(chan toolSignal, len(group)*4+4)
	// abandon is closed when executeToolGroup returns. A worker that finishes
	// after the batch was canceled or abandoned uses it so its final send
	// never blocks forever, which removes the need for a drainer goroutine
	// that could outlive the Run when a Tool ignores cancellation.
	abandon := make(chan struct{})
	defer close(abandon)
	for _, index := range group {
		go a.runTool(ctx, plans[index], signals, abandon)
	}
	return run.collect(signals)
}

// toolGroupRun is the Session goroutine's side of one group: it settles
// outcomes, stages their Events, and waits for the workers' signals.
type toolGroupRun struct {
	a        *Session
	ctx      context.Context
	runID    RunID
	turn     TurnNumber
	plans    []toolPlan
	group    []int
	outcomes map[int]ToolResult
	launched time.Time
}

// settle records one outcome and stages its ToolEnded as the outcome becomes
// known. Commitment happens later, in assistant source order.
func (g *toolGroupRun) settle(index int, result ToolResult) {
	g.outcomes[index] = result
	call := g.plans[index].call
	g.a.stage(ToolEnded{Header: NewHeader(g.a.def.id, g.runID, g.turn), CallID: call.ID, ToolID: call.ToolID, Status: result.Status, Code: result.Code, Executed: result.Executed, ElapsedMS: millis(time.Since(g.launched))})
}

// cancelRest settles every call of the group that has no outcome yet as
// canceled. started says whether their executors were started: a started Tool
// may have carried out its side effect before it saw the cancellation, so it
// is recorded as executed, with its outcome unknown.
func (g *toolGroupRun) cancelRest(started bool) map[int]ToolResult {
	for _, index := range g.group {
		if _, done := g.outcomes[index]; done {
			continue
		}
		g.settle(index, ToolResult{CallID: g.plans[index].call.ID, Status: ToolResultStatusCanceled, SafeError: safeError(g.ctx.Err()), Executed: started})
	}
	return g.outcomes
}

// collect waits for the workers' signals until every call of the group has an
// outcome, or the Run is canceled.
func (g *toolGroupRun) collect(signals chan toolSignal) map[int]ToolResult {
	for len(g.outcomes) < len(g.group) {
		select {
		case <-g.ctx.Done():
			// Cancellation must settle the Run even when a Tool ignores its
			// context. A worker that already reported keeps its outcome; the
			// rest are canceled, as executed, because their executors were
			// started. abandon (closed on return) releases any worker still
			// trying to report a late result.
			g.drain(signals)
			return g.cancelRest(true)
		case signal := <-signals:
			g.handle(signal)
		}
	}
	return g.outcomes
}

// drain settles the results already waiting in signals, without blocking.
func (g *toolGroupRun) drain(signals chan toolSignal) {
	for range cap(signals) {
		select {
		case signal := <-signals:
			if !signal.update {
				g.settle(signal.index, canceledOutcome(g.ctx, signal.result))
			}
		default:
			return
		}
	}
}

// handle publishes one progress report, or settles one result.
func (g *toolGroupRun) handle(signal toolSignal) {
	if signal.update {
		g.a.send(ToolProgress{Header: g.a.deltaHeader(g.runID, g.turn), CallID: g.plans[signal.index].call.ID, Text: signal.text})
		return
	}
	g.settle(signal.index, canceledOutcome(g.ctx, signal.result))
}

// canceledOutcome marks an outcome that arrived after the Run was canceled.
// The Tool ran, so Executed stays true.
func canceledOutcome(ctx context.Context, result ToolResult) ToolResult {
	if ctx.Err() != nil {
		result.Status = ToolResultStatusCanceled
		if result.SafeError == "" {
			result.SafeError = safeError(ctx.Err())
		}
	}
	return result
}

// preflightTools resolves, validates, and runs the Pre chain over every Tool
// Call in source order.
func (a *Session) preflightTools(ctx context.Context, runID RunID, turn TurnNumber, calls []ToolCall, toolCalls *uint32) ([]toolPlan, *RuntimeError) {
	plans := make([]toolPlan, 0, len(calls))
	for sourceIndex, call := range calls {
		*toolCalls++
		plan, valid := a.planCall(runID, turn, sourceIndex, call)
		if !valid {
			plans = append(plans, plan)
			continue
		}
		decision, preErr := a.def.extensions.beforeTool(ctx, plan.use)
		if preErr != nil {
			return nil, preErr
		}
		if decision.Block {
			plan.blocked = true
			plan.result = blockedResult(call.ID, decision)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// planCall resolves the Tool of call and validates its arguments, and returns
// its plan. When the call cannot run, the plan is the rejected one and valid
// is false.
func (a *Session) planCall(runID RunID, turn TurnNumber, sourceIndex int, call ToolCall) (plan toolPlan, valid bool) {
	// A call the Model got wrong (an unknown Tool, malformed or
	// schema-invalid arguments) settles as a failed Tool Result the Model
	// can read and correct; it never ends the Run (GOALS.md O8: every
	// failed tool call returns its reason to the model).
	entry, found := a.tools.registry.lookup(call.ToolID)
	if !found {
		// A catalog Tool the Model has not loaded does not run: it is
		// loaded now, and the Model calls it again.
		if status := a.tools.registry.loadable(call.ToolID); status == loadOK || status == loadAlready {
			return a.rejectedPlan(sourceIndex, call, CodeToolNotLoaded, fmt.Sprintf("%s was not loaded, so this call did not run. It is loaded now; call it again.", call.ToolID), call.ToolID), false
		}
		return a.rejectedPlan(sourceIndex, call, CodeToolResolutionFailure, a.unresolvedToolText(call.ToolID)), false
	}
	if !json.Valid(call.Arguments) {
		return a.rejectedPlan(sourceIndex, call, CodeToolArgumentFailure, fmt.Sprintf("%s: arguments for %s are not valid JSON", CodeToolArgumentFailure, call.ToolID)), false
	}
	spec := entry.spec
	if err := validateToolSchema(spec.InputSchema, call.Arguments); err != nil {
		return a.rejectedPlan(sourceIndex, call, CodeToolArgumentFailure, fmt.Sprintf("%s: %s: %v", CodeToolArgumentFailure, call.ToolID, err)), false
	}
	return toolPlan{
		index:      sourceIndex,
		call:       call,
		tool:       entry.tool,
		sequential: spec.Sequential,
		use: ToolUse{
			SessionID:   a.def.id,
			RunID:       runID,
			Turn:        turn,
			CallID:      call.ID,
			ToolID:      call.ToolID,
			Arguments:   slices.Clone(call.Arguments),
			SourceIndex: uint32(sourceIndex),
			Effect:      spec.Effect,
		},
	}, true
}

// safeError is the text of a context error for a Tool Result, or "" for nil.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// commitResult commits the settled result of one call of a Turn's batch: the
// call at position index, whose Tool outcome is outcome. A result is a chain of
// stages, each its own call so that each can be read, and fails alone: what the
// extensions make of it (afterToolUse), what is spilled (spillResult), its
// Message in the history (commitResultMessage), its Events (stageResult) and
// their write (flushResult). It returns the result as it was committed.
func (st *runState) commitResult(turn TurnNumber, index int, call ToolCall, outcome ToolResult) (ToolResult, *RuntimeError) {
	result, err := st.afterToolUse(outcome)
	if err != nil {
		return ToolResult{}, err
	}
	if result, err = st.spillResult(turn, call, result); err != nil {
		return ToolResult{}, err
	}
	message, result, err := st.commitResultMessage(result)
	if err != nil {
		return ToolResult{}, err
	}
	st.batch.next = index + 1
	st.stageResult(turn, call, message, result)
	if err := st.flushResult(); err != nil {
		return ToolResult{}, err
	}
	return result, nil
}

// afterToolUse runs the Post-Tool-Use extensions, which see executed, blocked,
// failed and canceled outcomes alike, in reverse installation order.
func (st *runState) afterToolUse(outcome ToolResult) (ToolResult, *RuntimeError) {
	return st.a.def.extensions.afterTool(st.runCtx, outcome)
}

// spillResult moves a result too large for the Model's context out to the
// Record (decision 040). The Tool already ran: a canceled Run still stores its
// body.
func (st *runState) spillResult(turn TurnNumber, call ToolCall, result ToolResult) (ToolResult, *RuntimeError) {
	spillCtx, cancel := finalContext(st.runCtx)
	defer cancel()
	return st.a.spill(spillCtx, st.runID, turn, call.ToolID, result)
}

// commitResultMessage commits the Tool Result Message to the history. A result
// too large for one Message is replaced by one that says why its output is
// missing, and the Run goes on.
func (st *runState) commitResultMessage(result ToolResult) (Message, ToolResult, *RuntimeError) {
	a := st.a
	message := Message{ID: MessageID(nextID("message")), RunID: st.runID, Role: RoleToolResult, Parts: cloneParts(result.Content), ToolResult: cloneToolResultPtr(result)}
	message, result, err := a.history.commitToolResult(a.def.limits, message, result)
	if err != nil {
		return Message{}, ToolResult{}, asRuntimeError(err)
	}
	return message, result, nil
}

// stageResult stages the Events of a committed result: its ToolResultCommitted,
// and a ToolLoaded for each Tool it lists. A Tool loads as the Tool Result that
// lists it commits, and its ToolLoaded goes in the same write, so the Record
// never says "Loaded" for a Tool it does not hold as loaded. The tools lock
// serializes the visibility write with Session.Tools.
func (st *runState) stageResult(turn TurnNumber, call ToolCall, message Message, result ToolResult) {
	a, hd := st.a, st.header
	a.stage(ToolResultCommitted{Header: hd(turn), Message: message.Clone(), CallID: call.ID, Status: result.Status})
	for _, id := range result.LoadedTools {
		a.tools.mu.Lock()
		loadedNow := a.tools.registry.load(id)
		a.tools.mu.Unlock()
		if loadedNow {
			a.stage(ToolLoaded{Header: hd(turn), ToolID: id, Why: loadWhy(call, id)})
		}
	}
}

// flushResult writes the Events of the result. It is committed in memory
// already, so a canceled Run still stores it.
func (st *runState) flushResult() *RuntimeError {
	flushCtx, cancel := finalContext(st.runCtx)
	defer cancel()
	return st.a.flush(flushCtx)
}
