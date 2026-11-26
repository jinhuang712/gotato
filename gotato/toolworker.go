package gotato

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// runTool is one worker: it executes one planned Tool call and reports its
// progress and its result on signals. abandon releases a send nobody will
// receive. At the call's ToolCallDeadline it stops waiting and reports the
// call as timed out, with its outcome unknown.
func (a *Session) runTool(ctx context.Context, plan toolPlan, signals chan<- toolSignal, abandon <-chan struct{}) {
	toolCtx, toolCancel := boundedContext(ctx, a.def.limits.ToolCallDeadline)
	defer toolCancel()
	progress := newToolProgress(func(text string) {
		select {
		case signals <- toolSignal{index: plan.index, update: true, text: text}:
		case <-abandon:
		case <-toolCtx.Done():
		}
	})
	done := make(chan toolExecution, 1)
	// [LIMITATION] Go cannot stop a goroutine: a Tool that ignores ctx runs on until Execute returns, as after an Abort.
	go func() {
		result, err := executeToolSafely(toolCtx, plan.tool, plan.use, progress)
		done <- toolExecution{result, err}
	}()
	executed, ok := awaitTool(ctx, toolCtx, done)
	if !ok {
		return
	}
	result := a.toolCallResult(ctx, toolCtx, plan, executed)
	select {
	case signals <- toolSignal{index: plan.index, result: result}:
	case <-abandon:
	}
}

// toolExecution is what a Tool's Execute returned.
type toolExecution struct {
	result ToolResult
	err    error
}

// awaitTool waits for the execution of a Tool on done, or until toolCtx ends.
// ok is false when the Run is canceled, and collect settles the call.
func awaitTool(ctx, toolCtx context.Context, done <-chan toolExecution) (executed toolExecution, ok bool) {
	select {
	case executed = <-done:
	// [LIVENESS] a Tool that ignores its ctx must not hold the Run past the call's deadline.
	case <-toolCtx.Done():
		select {
		case executed = <-done:
		default:
			if ctx.Err() != nil {
				// The Run is canceled: collect settles this call.
				return executed, false
			}
			executed.err = toolCtx.Err()
		}
	}
	return executed, true
}

// toolCallResult is the result of the call of plan, which executed: a call
// past its ToolCallDeadline, a failed call, or the Tool's own result.
func (a *Session) toolCallResult(ctx, toolCtx context.Context, plan toolPlan, executed toolExecution) ToolResult {
	switch {
	case executed.err != nil && ctx.Err() == nil && errors.Is(toolCtx.Err(), context.DeadlineExceeded):
		text := fmt.Sprintf("%s: the tool did not finish within ToolCallDeadline (%s); its outcome is unknown", CodeDeadlineExceeded, a.def.limits.ToolCallDeadline)
		return ToolResult{CallID: plan.call.ID, Status: ToolResultStatusFailed, SafeError: text, Code: CodeDeadlineExceeded, Executed: true}
	case executed.err != nil:
		return ToolResult{CallID: plan.call.ID, Status: ToolResultStatusFailed, SafeError: toolErrorText(executed.err), Code: toolErrorCode(executed.err), Executed: true}
	}
	owned := ToolResult{CallID: plan.call.ID, Executed: true}
	if _, builtin := plan.tool.(*searchTool); builtin {
		owned.LoadedTools = executed.result.LoadedTools
	}
	result := ownResult(executed.result, owned)
	if result.Status == "" {
		result.Status = ToolResultStatusOK
	}
	return result
}

// newToolProgress wraps send in the bounds on a Tool's progress reports. A
// Tool may report from any goroutine, so the counters that bound the reports
// are guarded.
func newToolProgress(send func(text string)) func(text string) {
	var mu sync.Mutex
	var bytes uint64
	var updates uint32
	return func(text string) {
		mu.Lock()
		if updates >= maxToolProgressUpdates || bytes+uint64(len(text)) > maxToolProgressBytes {
			mu.Unlock()
			return
		}
		updates++
		bytes += uint64(len(text))
		mu.Unlock()
		send(text)
	}
}

// executeToolSafely runs a Tool and turns a panic into a failure of the call.
// [LIVENESS] a panicking Tool would end the process instead of failing its call.
func executeToolSafely(ctx context.Context, tool Tool, use ToolUse, progress ToolProgressFunc) (result ToolResult, err error) {
	err = callHost("tool", func() (failure error) {
		result, failure = tool.Execute(ctx, use, progress)
		return failure
	})
	if panicked, ok := err.(*panicError); ok {
		// Match the Extension guard: a fixed, client-safe Message. What the
		// Tool panicked with, and the stack, stay on the Cause for logs; the
		// code stays in the text of the failed Result.
		return ToolResult{}, runtimeError(CodeToolExecutionFailure, "Tool", panicked.Public(), errors.New(panicked.Error()))
	}
	return result, err
}

// toolErrorCode is the Code of a failed Tool Result for an error a Tool
// returned: the Code of a RuntimeError it carries, otherwise
// CodeToolExecutionFailure, the code its text starts with.
func toolErrorCode(err error) ErrorCode {
	var re *RuntimeError
	if errors.As(err, &re) && re != nil && re.Code != "" {
		return re.Code
	}
	return CodeToolExecutionFailure
}

// toolErrorText is the text of an error a Tool returned, for its failed
// Result. The text goes into the Transcript, to every subscriber and to the
// Model, and a foreign error may carry a host, a query or a secret, so only
// what the error chooses to make public is kept: its PublicError text, or a
// RuntimeError, whose Message is written to be read. Any other error is
// "tool failed" with its code.
// [SECURITY] a Tool error can carry a host, a query or a secret, and this text goes to the Model and every subscriber.
func toolErrorText(err error) string {
	if err == nil {
		return ""
	}
	fallback := string(CodeToolExecutionFailure) + ": tool failed"
	var re *RuntimeError
	if errors.As(err, &re) && re != nil {
		// Its Message is text its maker wrote to be read; a foreign error
		// it wraps stays on the Cause, which Error does not print.
		fallback = re.Error()
	}
	return PublicText(err, fallback)
}
