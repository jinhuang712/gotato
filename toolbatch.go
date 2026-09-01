package gotato

import (
	"context"
	"encoding/json"
	"slices"
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
	blocked    bool
	result     ToolResult
}

// toolSignal carries one worker's Event back to the Agent goroutine, which is
// the only place Events are sequenced and observers are awaited.
type toolSignal struct {
	index  int
	update bool
	text   string
	result ToolResult
}

// parallelWorkers turns the configured bound into a worker count. Zero and one
// both mean sequential.
func (a *coreAgent) parallelWorkers() int {
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
// by plan index. Completion Events are emitted in actual completion order
// while the Agent goroutine waits here; results commit in source order later.
func (a *coreAgent) executeToolGroup(
	ctx context.Context,
	runID RunID,
	sequence *uint64,
	turn TurnNumber,
	messageID MessageID,
	plans []toolPlan,
	group []int,
) (map[int]ToolResult, *RuntimeError) {
	signals := make(chan toolSignal, len(group)*4+4)
	for _, index := range group {
		plan := plans[index]
		go func(plan toolPlan) {
			toolCtx, toolCancel := boundedContext(ctx, a.limits.ToolCallDeadline)
			defer toolCancel()
			var progressBytes uint64
			var progressUpdates uint32
			progress := func(text string) {
				if (a.limitsSet && a.limits.MaxToolProgressUpdates == 0) || (a.limits.MaxToolProgressUpdates > 0 && progressUpdates >= a.limits.MaxToolProgressUpdates) {
					return
				}
				if (a.limitsSet && a.limits.MaxToolProgressBytes == 0) || (a.limits.MaxToolProgressBytes > 0 && progressBytes+uint64(len(text)) > a.limits.MaxToolProgressBytes) {
					return
				}
				progressUpdates++
				progressBytes += uint64(len(text))
				signals <- toolSignal{index: plan.index, update: true, text: text}
			}
			executed, toolErr := executeToolSafely(plan.tool, toolCtx, plan.use, progress)
			var result ToolResult
			if toolErr != nil {
				result = ToolResult{CallID: plan.call.ID, Status: ToolResultFailed, SafeError: safeError(toolErr), Executed: true}
			} else {
				result = executed
				result.CallID = plan.call.ID
				result.Executed = true
				if result.Status == "" {
					result.Status = ToolResultOK
				}
			}
			signals <- toolSignal{index: plan.index, result: result}
		}(plan)
	}

	outcomes := make(map[int]ToolResult, len(group))
	for len(outcomes) < len(group) {
		signal := <-signals
		if signal.update {
			if err := a.emit(runID, sequence, EventToolExecutionUpdate, EventCoalescable, turn, messageID, plans[signal.index].call.ID, map[string]any{"text": signal.text}); err != nil {
				// Drain the remaining workers so no goroutine outlives the Run.
				go drainSignals(signals, len(group)-len(outcomes))
				return nil, err
			}
			continue
		}
		result := signal.result
		if ctx.Err() != nil {
			result.Status = ToolResultCanceled
			if result.SafeError == "" {
				result.SafeError = safeError(ctx.Err())
			}
		}
		outcomes[signal.index] = result
		// The completion Event is emitted here, as the outcome arrives, so
		// it reflects actual completion order. Commitment happens later, in
		// assistant source order.
		if err := a.emit(runID, sequence, EventToolExecutionEnd, EventProtected, turn, messageID, plans[signal.index].call.ID, map[string]any{"status": result.Status, "executed": result.Executed}); err != nil {
			go drainSignals(signals, len(group)-len(outcomes))
			return nil, err
		}
	}
	return outcomes, nil
}

func drainSignals(signals chan toolSignal, remaining int) {
	for remaining > 0 {
		signal := <-signals
		if !signal.update {
			remaining--
		}
	}
}

// preflightTools resolves, validates, and runs the Pre chain over every Tool
// Call in source order.
func (a *coreAgent) preflightTools(ctx context.Context, runID RunID, turn TurnNumber, calls []ToolCall, toolCalls *uint32) ([]toolPlan, *RuntimeError) {
	plans := make([]toolPlan, 0, len(calls))
	for sourceIndex, call := range calls {
		*toolCalls++
		if limitExceededUint32(a.limitsSet, a.limits.MaxToolCalls, *toolCalls) {
			return nil, runtimeError(ErrLimitExceeded, "Tool", "maximum Tool Calls exceeded", nil)
		}
		tool := a.registry.lookup(call.ToolID)
		if tool == nil {
			return nil, runtimeError(ErrToolResolutionFailure, "Tool", "unknown Tool: "+call.ToolID, nil)
		}
		if !json.Valid(call.Arguments) {
			return nil, runtimeError(ErrToolArgumentFailure, "Tool", "Tool arguments are not valid JSON", nil)
		}
		spec := tool.Spec()
		if err := validateToolSchema(spec.InputSchema, call.Arguments); err != nil {
			return nil, runtimeError(ErrToolArgumentFailure, "Tool", err.Error(), err)
		}
		plan := toolPlan{
			index:      sourceIndex,
			call:       call,
			tool:       tool,
			sequential: spec.Sequential,
			use: ToolUse{
				RunID:         runID,
				Turn:          turn,
				CallID:        call.ID,
				QualifiedID:   call.ToolID,
				ArgumentsJSON: slices.Clone(call.Arguments),
				SourceIndex:   uint32(sourceIndex),
			},
		}
		decision, preErr := a.extensions.beforeTool(ctx, plan.use)
		if preErr != nil {
			return nil, preErr
		}
		if decision.Block {
			plan.blocked = true
			plan.result = ToolResult{CallID: call.ID, Status: ToolResultBlocked, SafeError: decision.Reason, Executed: false}
			if decision.Result != nil {
				replacement := decision.Result.Clone()
				replacement.CallID = call.ID
				replacement.Status = ToolResultBlocked
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
