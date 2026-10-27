package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
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
		message := Message{ID: MessageID(nextID("message")), RunID: runID, Role: RoleToolResult, Parts: cloneContent(result.Content), ToolResult: ptrToolResult(result)}
		if err := a.appendMessage(message, false); err != nil {
			break
		}
		a.stage(ToolResultCommitted{Header: NewHeader(a.id, runID, batch.turn), Message: message.Clone(), CallID: call.ID, Status: resultStatus})
	}
	*batch = openBatch{}
}

// rejectedPlan settles a call the Model got wrong as a failed, unexecuted
// Tool Result carrying a message the Model can act on.
func (a *Session) rejectedPlan(index int, call ToolCall, message string, loaded ...string) toolPlan {
	return toolPlan{
		index:   index,
		call:    call,
		blocked: true,
		result: ToolResult{
			CallID:      call.ID,
			Status:      ToolResultStatusFailed,
			Content:     []ContentPart{{Kind: ContentKindText, Text: message}},
			SafeError:   message,
			Executed:    false,
			LoadedTools: loaded,
		},
	}
}

// visibleToolIDs lists the Tools the Model was shown this Turn, for an
// unknown-tool message.
func (a *Session) visibleToolIDs() string {
	specs := a.registry.visibleSpecs()
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
// is the only place Events are staged.
type toolSignal struct {
	index  int
	update bool
	text   string
	result ToolResult
}

// parallelWorkers turns the configured bound into a worker count. Zero and one
// both mean sequential.
func (a *Session) parallelWorkers() int {
	if a.limits.MaxParallelTools <= 1 {
		return 1
	}
	return int(a.limits.MaxParallelTools)
}

// groupPlans splits a batch into execution groups. A Tool declared Sequential
// runs alone; consecutive parallel Tools share a group bounded by the worker
// limit.
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
	outcomes := make(map[int]ToolResult, len(group))
	launched := time.Now()
	// settle records one outcome and stages its ToolEnded as the outcome
	// becomes known. Commitment happens later, in assistant source order.
	settle := func(index int, result ToolResult) {
		outcomes[index] = result
		elapsed := millis(time.Since(launched))
		a.stage(ToolEnded{Header: NewHeader(a.id, runID, turn), CallID: plans[index].call.ID, ToolID: plans[index].call.ToolID, Status: result.Status, Executed: result.Executed, ElapsedMS: elapsed})
	}
	// cancelRest settles every call of the group that has no outcome yet as
	// canceled. started says whether their executors were started: a started
	// Tool may have carried out its side effect before it saw the
	// cancellation, so it is recorded as executed, with its outcome unknown.
	cancelRest := func(started bool) map[int]ToolResult {
		for _, index := range group {
			if _, done := outcomes[index]; done {
				continue
			}
			settle(index, ToolResult{CallID: plans[index].call.ID, Status: ToolResultStatusCanceled, SafeError: safeError(ctx.Err()), Executed: started})
		}
		return outcomes
	}
	// A Run canceled before this group started never launches it: a Tool that
	// ignores its context would still carry out its side effect.
	if ctx.Err() != nil {
		return cancelRest(false)
	}

	signals := make(chan toolSignal, len(group)*4+4)
	// abandon is closed when executeToolGroup returns. A worker that finishes
	// after the batch was canceled or abandoned uses it so its final send
	// never blocks forever, which removes the need for a drainer goroutine
	// that could outlive the Run when a Tool ignores cancellation.
	abandon := make(chan struct{})
	defer close(abandon)
	for _, index := range group {
		plan := plans[index]
		go func(plan toolPlan) {
			toolCtx, toolCancel := boundedContext(ctx, a.limits.ToolCallDeadline)
			defer toolCancel()
			// A Tool may report progress from any goroutine, so the counters
			// that bound the reports are guarded.
			var progressMu sync.Mutex
			var progressBytes uint64
			var progressUpdates uint32
			progress := func(text string) {
				progressMu.Lock()
				if progressUpdates >= maxToolProgressUpdates {
					progressMu.Unlock()
					return
				}
				if progressBytes+uint64(len(text)) > maxToolProgressBytes {
					progressMu.Unlock()
					return
				}
				progressUpdates++
				progressBytes += uint64(len(text))
				progressMu.Unlock()
				select {
				case signals <- toolSignal{index: plan.index, update: true, text: text}:
				case <-abandon:
				case <-toolCtx.Done():
				}
			}
			executed, toolErr := executeToolSafely(plan.tool, toolCtx, plan.use, progress)
			var result ToolResult
			if toolErr != nil {
				result = ToolResult{CallID: plan.call.ID, Status: ToolResultStatusFailed, SafeError: safeError(toolErr), Executed: true}
			} else {
				result = executed
				result.CallID = plan.call.ID
				result.Executed = true
				if result.Status == "" {
					result.Status = ToolResultStatusOK
				}
			}
			select {
			case signals <- toolSignal{index: plan.index, result: result}:
			case <-abandon:
			}
		}(plan)
	}

	for len(outcomes) < len(group) {
		select {
		case <-ctx.Done():
			// Cancellation must settle the Run even when a Tool ignores its
			// context. A worker that already reported keeps its outcome; the
			// rest are canceled, as executed, because their executors were
			// started. abandon (closed on return) releases any worker still
			// trying to report a late result.
		drain:
			for range cap(signals) {
				select {
				case signal := <-signals:
					if signal.update {
						continue
					}
					settle(signal.index, canceledOutcome(ctx, signal.result))
				default:
					break drain
				}
			}
			return cancelRest(true)
		case signal := <-signals:
			if signal.update {
				a.send(ToolProgress{Header: a.deltaHeader(runID, turn), CallID: plans[signal.index].call.ID, Text: signal.text})
				continue
			}
			settle(signal.index, canceledOutcome(ctx, signal.result))
		}
	}
	return outcomes
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
		// A call the Model got wrong (an unknown Tool, malformed or
		// schema-invalid arguments) settles as a failed Tool Result the Model
		// can read and correct; it never ends the Run (GOALS.md G-G04: every
		// failed tool call returns its reason to the model).
		tool := a.registry.lookup(call.ToolID)
		if tool == nil {
			// A catalog Tool the Model has not loaded does not run: it is
			// loaded now, and the Model calls it again.
			if a.registry.stageCall(call.ToolID) {
				plans = append(plans, a.rejectedPlan(sourceIndex, call, fmt.Sprintf("%s was not loaded, so this call did not run. It is loaded now; call it again.", call.ToolID), call.ToolID))
				continue
			}
			plans = append(plans, a.rejectedPlan(sourceIndex, call, fmt.Sprintf("%s: unknown tool %q; available tools: %s", CodeToolResolutionFailure, call.ToolID, a.visibleToolIDs())))
			continue
		}
		if !json.Valid(call.Arguments) {
			plans = append(plans, a.rejectedPlan(sourceIndex, call, fmt.Sprintf("%s: arguments for %s are not valid JSON", CodeToolArgumentFailure, call.ToolID)))
			continue
		}
		spec := tool.Spec()
		if err := validateToolSchema(spec.InputSchema, call.Arguments); err != nil {
			plans = append(plans, a.rejectedPlan(sourceIndex, call, fmt.Sprintf("%s: %s: %v", CodeToolArgumentFailure, call.ToolID, err)))
			continue
		}
		plan := toolPlan{
			index:      sourceIndex,
			call:       call,
			tool:       tool,
			sequential: spec.Sequential,
			use: ToolUse{
				RunID:       runID,
				Turn:        turn,
				CallID:      call.ID,
				ToolID:      call.ToolID,
				Arguments:   slices.Clone(call.Arguments),
				SourceIndex: uint32(sourceIndex),
				Effect:      spec.Effect,
			},
		}
		decision, preErr := a.extensions.beforeTool(ctx, plan.use)
		if preErr != nil {
			return nil, preErr
		}
		if decision.Block {
			plan.blocked = true
			plan.result = ToolResult{CallID: call.ID, Status: ToolResultStatusBlocked, SafeError: decision.Reason, Executed: false}
			if decision.Result != nil {
				replacement := decision.Result.Clone()
				replacement.CallID = call.ID
				replacement.Status = ToolResultStatusBlocked
				replacement.Executed = false
				if replacement.SafeError == "" {
					replacement.SafeError = decision.Reason
				}
				plan.result = replacement
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}
