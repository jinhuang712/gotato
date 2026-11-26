package gotato

import (
	"context"
	"errors"
	"time"
)

// runState is what one Run counts as it goes, with the methods that end it.
// It lives for the one executeRun call and is used from its goroutine only.
type runState struct {
	a       *Session
	ctx     context.Context // the caller's, which the Run's last write uses
	runCtx  context.Context // the Run's own, canceled by Abort
	runID   RunID
	started time.Time

	usage                  Usage
	toolCalls              uint32
	turns                  uint32
	truncatedContinuations uint32
	textBytes              uint64
	reasoningBytes         uint64
	// batch is the Tool batch of the last committed assistant Message, while
	// some of its calls still have no committed result.
	batch openBatch
}

func (st *runState) metrics() RunMetrics {
	return RunMetrics{
		ElapsedMS:      millis(time.Since(st.started)),
		Turns:          st.turns,
		ToolCalls:      st.toolCalls,
		TextBytes:      st.textBytes,
		ReasoningBytes: st.reasoningBytes,
	}
}

func (st *runState) setTurn(turn TurnNumber) {
	st.a.run.mu.Lock()
	st.a.run.turn = turn
	st.a.run.mu.Unlock()
}

func (st *runState) currentTurn() TurnNumber {
	st.a.run.mu.Lock()
	defer st.a.run.mu.Unlock()
	return st.a.run.turn
}

func (st *runState) header(turn TurnNumber) Header { return NewHeader(st.a.def.id, st.runID, turn) }

// end stores the Run's last Events, RunEnded among them. A Run may not
// end without exactly one RunEnded, so it is staged even when the Run
// failed on a write: a Journal that has stopped refuses it too, and then
// nothing was stored, and nothing is sent.
func (st *runState) end(turn TurnNumber, result RunResult, extra func(*RunEnded)) *RuntimeError {
	a := st.a
	// From here on no Abort can start a write, and no Steer, FollowUp or
	// Interrupt is taken into a queue (see sendControl): what is queued now is
	// dropped below, and nothing slips in behind that.
	a.run.mu.Lock()
	a.run.ending = true
	a.run.keeps = a.keepsQueued(result.Status)
	a.run.mu.Unlock()
	if !a.run.keeps {
		a.discardControl(st.runID, turn)
	}
	// The Aborts that did start a write have finished: an abort_requested
	// never follows run_ended.
	a.run.aborts.Wait()
	if a.run.endingHook != nil {
		a.run.endingHook()
	}
	metrics := st.metrics()
	ended := RunEnded{
		Header:         st.header(turn),
		Status:         result.Status,
		Turns:          metrics.Turns,
		ToolCalls:      metrics.ToolCalls,
		Usage:          result.Usage,
		ElapsedMS:      metrics.ElapsedMS,
		TextBytes:      metrics.TextBytes,
		ReasoningBytes: metrics.ReasoningBytes,
	}
	if result.Error != nil {
		ended.Cause = result.Error.Code
	}
	if extra != nil {
		extra(&ended)
	}
	a.stage(ended)
	// The caller's ctx may have ended, which is why the Run is ending.
	ctx, cancel := finalContext(st.ctx)
	defer cancel()
	return a.flush(ctx)
}

// fail ends the Run with err: it closes the Tool calls left open, writes
// RunEnded, and returns the failed result with err.
func (st *runState) fail(err *RuntimeError) (RunResult, error) {
	if err == nil {
		err = runtimeError(CodeInternalInvariant, "Run", "nil terminal error", nil)
	}
	result := RunResult{RunID: st.runID, Status: RunStatusFailed, Usage: st.usage, Metrics: st.metrics(), Error: err}
	if errors.Is(err.Cause, context.Canceled) || err.Code == CodeCanceled {
		result.Status = RunStatusCanceled
	}
	if err.Code == CodeDeadlineExceeded {
		result.Status = RunStatusDeadlineExceeded
	}
	turn := st.currentTurn()
	st.a.closeOpenCalls(st.runID, &st.batch, err, result.Status)
	_ = st.end(turn, result, nil)
	return result, err
}

// settle ends the Run as completed with assistant as its final Message, and
// says so when a TurnStopper stopped it (decision). When the write that ends
// it fails, the Run failed instead.
func (st *runState) settle(turn TurnNumber, assistant Message, decision StopDecision) (RunResult, error) {
	final := assistant.Clone()
	result := RunResult{RunID: st.runID, Status: RunStatusCompleted, FinalMessage: &final, Usage: st.usage, Metrics: st.metrics()}
	writeErr := st.end(turn, result, func(e *RunEnded) {
		if decision.Stop {
			e.StoppedByExtension = true
			e.StopReason = decision.Reason
		}
	})
	if writeErr != nil {
		// The write that ends the Run failed, so the Record does not
		// say the Run ended: the Run failed, whatever the reply was.
		result.Status = RunStatusFailed
		result.Error = writeErr
		return result, writeErr
	}
	return result, nil
}

// callModel builds the Turn's request from the Transcript, calls the Model and
// reads its reply. The Model is called once, and once more when it refuses the
// request as too long before any output and a TurnPreparer shortened it. A
// non-nil failure is an error of the Run itself; modelErr is the Model's.
func (st *runState) callModel(turn TurnNumber, runStart int) (assistant Message, usage Usage, modelErr *RuntimeError, failure *RuntimeError) {
	current, assembleErr := st.buildTurn(turn)
	if assembleErr != nil {
		return Message{}, Usage{}, nil, assembleErr
	}
	// The Model is called here, once, and once more when it refuses the
	// request as too long before any output and a TurnPreparer shortened it.
	if _, failure = st.prepareCall(turn, runStart, &current, nil); failure != nil {
		return Message{}, Usage{}, nil, failure
	}
	assistant, usage, modelErr, failure = st.callOnce(turn, &current)
	// A refusal before any output is the one failure a shorter request
	// can mend, once per Turn.
	if failure != nil || !refusedBeforeOutput(modelErr, assistant) {
		return assistant, usage, modelErr, failure
	}
	refused := modelErr
	changed, failure := st.prepareCall(turn, runStart, &current, refused)
	if failure != nil {
		return Message{}, Usage{}, nil, failure
	}
	if !changed {
		// Nothing shortened the request: the refusal stands.
		return Message{}, Usage{}, refused, nil
	}
	return st.callOnce(turn, &current)
}

// refusedBeforeOutput reports whether modelErr refused the request as too long
// before the reply had any output.
func refusedBeforeOutput(modelErr *RuntimeError, assistant Message) bool {
	return modelErr != nil && modelErr.Code == CodeContextOverflow &&
		len(assistant.Parts) == 0 && len(assistant.ToolCalls) == 0
}

// prepareCall gives the TurnPreparers their chance before a model call, with
// the refusal of the last call if any. When one rewrote the Transcript, it
// builds current again and reports changed.
func (st *runState) prepareCall(turn TurnNumber, runStart int, current *turnBuild, refused *RuntimeError) (changed bool, failure *RuntimeError) {
	a := st.a
	changed, prepareErr := st.prepareTurn(turn, runStart, current.request, asError(refused))
	if prepareErr != nil || !changed {
		return false, prepareErr
	}
	if err := a.history.measure(); err != nil {
		return false, asRuntimeError(err)
	}
	if err := a.syncLoadedTools(); err != nil {
		return false, asRuntimeError(err)
	}
	if err := a.refreshToolsInRun(st.runCtx); err != nil {
		return false, err
	}
	rebuilt, assembleErr := st.buildTurn(turn)
	if assembleErr != nil {
		return false, assembleErr
	}
	*current = rebuilt
	return true, nil
}

// callOnce writes the model call of current and calls the Model once. A
// non-nil failure is an error of the Run itself; modelErr is the Model's.
func (st *runState) callOnce(turn TurnNumber, current *turnBuild) (assistant Message, usage Usage, modelErr *RuntimeError, failure *RuntimeError) {
	a := st.a
	contextBuilt := current.contextBuilt(st.header(turn))
	// The model call is written before the Model is called, so a writer
	// that can no longer write stops here, and the store's time stays out
	// of the model's.
	assistantID := MessageID(nextID("message"))
	a.stage(contextBuilt, ReplyStarted{Header: st.header(turn), MessageID: assistantID})
	if err := a.flush(st.runCtx); err != nil {
		return Message{}, Usage{}, nil, err
	}
	assistant, usage, modelErr = a.readAssistant(st.runCtx, st.runID, turn, assistantID, current.request)
	st.usage = addUsage(st.usage, usage)
	return assistant, usage, modelErr, nil
}

// boundary is the safe boundary after a Turn: Steer applies, and FollowUp too
// when the Run is settling. Cancellation is checked first so a queued
// continuation is never committed to a Run that is already ending.
func (st *runState) boundary(turn TurnNumber, settling bool) (bool, *RuntimeError) {
	if err := contextFailure(st.runCtx, "continuation"); err != nil {
		return false, err
	}
	return st.a.consumeControl(st.runCtx, st.runID, turn, settling)
}

// executeRun runs the one canonical Loop. A nil prompt is a Continue: the Run
// appends no user Message and picks up the committed transcript as it stands.
func (a *Session) executeRun(ctx context.Context, prompt *Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	runID, endRun, startFailure := a.beginRun(cancel)
	defer endRun()

	st := &runState{a: a, ctx: ctx, runCtx: runCtx, runID: runID, started: time.Now()}
	a.stage(RunStarted{Header: st.header(0), RequestID: a.host.requestID})
	if startFailure != nil {
		return st.fail(startFailure)
	}
	if err := a.refreshToolSets(runCtx, st.header(0)); err != nil {
		return st.fail(err)
	}
	runStart, failure := a.history.len()
	if failure != nil {
		return st.fail(failure)
	}
	if err := a.history.measure(); err != nil {
		return st.fail(asRuntimeError(err))
	}
	if prompt != nil {
		if err := st.commitPrompt(prompt); err != nil {
			return st.fail(err)
		}
	}
	for turn := TurnNumber(1); ; turn++ {
		if more, result, err := st.runTurn(turn, runStart); !more {
			return result, err
		}
	}
}

// beginRun registers a new Run, whose Abort is cancel, and returns its ID and
// the function that unregisters it when the Run is over. failure is a panic in
// the host's withRunStart function, which fails the Run once RunStarted is
// stored.
func (a *Session) beginRun(cancel context.CancelFunc) (RunID, func(), *RuntimeError) {
	runID := RunID(nextID("run"))
	a.run.mu.Lock()
	a.run.id = runID
	a.run.turn = 0
	a.run.ending = false
	a.run.abortBeforeStart = false
	a.run.keeps = false
	a.run.stop = cancel
	a.run.mu.Unlock()
	a.run.inRun.Store(true)
	var failure *RuntimeError
	if a.host.onRunStart != nil {
		// [LIVENESS] a panic in the host's withRunStart function fails this Run, not the process.
		if err := callHost("WithRunStart", func() error { a.host.onRunStart(runID); return nil }); err != nil {
			failure = runtimeError(CodeExtensionFailure, "WithRunStart", PublicText(err, "withRunStart failed"), err)
		}
	}
	return runID, func() {
		a.run.inRun.Store(false)
		// [LIVENESS] a Run whose run_ended was never written must not leave its span open for the next Run's Events.
		a.run.mu.Lock()
		a.run.open = false
		if a.run.id == runID {
			a.run.id = ""
			a.run.turn = 0
			a.run.stop = nil
		}
		a.run.mu.Unlock()
	}, failure
}

// commitPrompt commits the Prompt's user Message as the Run's first.
func (st *runState) commitPrompt(prompt *Message) *RuntimeError {
	a := st.a
	if prompt.ID == "" {
		prompt.ID = MessageID(nextID("message"))
	}
	prompt.RunID = st.runID
	if err := a.commitMessage(*prompt); err != nil {
		return asRuntimeError(err)
	}
	a.stage(MessageCommitted{Header: st.header(0), Message: prompt.Clone(), Source: MessageSourcePrompt})
	return a.flush(st.runCtx)
}

// failTurn ends the Run as failed by err. It is fail in the form runTurn
// returns: the Run does not go on.
func (st *runState) failTurn(err *RuntimeError) (bool, RunResult, error) {
	result, runErr := st.fail(err)
	return false, result, runErr
}

// settleTurn ends the Run as completed with assistant as its final Message.
func (st *runState) settleTurn(turn TurnNumber, assistant Message, decision StopDecision) (bool, RunResult, error) {
	result, err := st.settle(turn, assistant, decision)
	return false, result, err
}

// runTurn runs one Turn: it builds the request, calls the Model, commits the
// reply and runs the Tool calls it asks for. It reports more when the Run goes
// on to another Turn; otherwise the Run has ended, with result and err.
func (st *runState) runTurn(turn TurnNumber, runStart int) (more bool, result RunResult, err error) {
	a, runCtx := st.a, st.runCtx
	if failure := contextFailure(runCtx, "turn"); failure != nil {
		return st.failTurn(failure)
	}
	st.turns++
	st.setTurn(turn)
	a.stage(TurnStarted{Header: st.header(turn)})
	t := &turnRun{turn: turn, started: time.Now()}

	// The Tool surface is refreshed at the Turn boundary so every Tool the
	// Model is shown in this request resolves for the whole Turn.
	if refreshErr := a.refreshToolsInRun(runCtx); refreshErr != nil {
		return st.failTurn(refreshErr)
	}
	// A notice that waits goes in now, right before this model call: after
	// the prompt, or after the Tool Results of the last Turn.
	if failure := a.commitQueuedNotices(runCtx, st.runID, turn); failure != nil {
		return st.failTurn(failure)
	}

	assistant, usage, modelErr, failure := st.callModel(turn, runStart)
	if failure != nil {
		return st.failTurn(failure)
	}
	t.assistant, t.usage = assistant, usage
	t.text, t.reasoning = replyBytes(assistant)
	st.textBytes += t.text
	st.reasoningBytes += t.reasoning
	if modelErr != nil {
		return t.afterModelError(st, modelErr)
	}

	// A Message with Tool calls stopped for them, unless the output limit
	// cut it off: StopReasonMaxTokens stays, so a TurnStopper sees the cut-off.
	if len(t.assistant.ToolCalls) > 0 && t.assistant.StopReason != StopReasonMaxTokens {
		t.assistant.StopReason = StopReasonToolCalls
	}
	if commitErr := a.commitMessage(t.assistant); commitErr != nil {
		return st.failTurn(asRuntimeError(commitErr))
	}
	if len(t.assistant.ToolCalls) > 0 {
		st.batch = openBatch{turn: turn, calls: t.assistant.ToolCalls}
	}
	a.stage(ReplyEnded{Header: st.header(turn), Message: t.assistant.Clone(), Model: t.assistant.Model, StopReason: t.assistant.StopReason, ToolCalls: len(t.assistant.ToolCalls)})
	replyNumber, flushErr := a.flushNumbered(runCtx, EventKindReplyEnded)
	if flushErr != nil {
		return st.failTurn(flushErr)
	}
	if len(t.assistant.ToolCalls) == 0 {
		return t.endAnswerTurn(st, replyNumber)
	}
	return t.endToolTurn(st)
}

// prepareTurn gives the TurnPreparers their chance before a model call. It
// reports whether one rewrote the Transcript.
func (st *runState) prepareTurn(turn TurnNumber, runStart int, request ModelRequest, refusal error) (bool, *RuntimeError) {
	a := st.a
	if len(a.def.extensions.preparers) == 0 {
		return false, nil
	}
	return a.def.extensions.prepareTurn(st.runCtx, PendingTurn{
		SessionID:  a.def.id,
		RunID:      st.runID,
		Turn:       turn,
		Transcript: a.history.transcript,
		RunStart:   runStart,
		Request:    request,
		Overflow:   refusal != nil,
		Refusal:    refusal,
		Flush:      func(ctx context.Context) error { return asError(a.flush(ctx)) },
	})
}

// consumeControl commits the control Messages that apply at this boundary. It
// runs inside the Session execution unit, so committing them keeps the Session
// goroutine as the only mutation authority for the transcript.
//
// Notices wait in the same queue and go in with them, in the order they
// arrived. A notice never keeps a Run going: at settlement they are committed
// only when a Steer or FollowUp is, and otherwise wait for the next Run.
func (a *Session) consumeControl(ctx context.Context, runID RunID, turn TurnNumber, settling bool) (bool, *RuntimeError) {
	items := a.queue.take(settling)
	// [DATA-LOSS] a message taken and not committed goes back to the queue, whatever stops the commits.
	defer items.putBack()
	injected := false
	for _, item := range items.items {
		if item.source == MessageSourceNotice {
			if err := a.commitNotice(ctx, runID, turn, item, items); err != nil {
				return injected, err
			}
			continue
		}
		if err := a.commitContinuation(ctx, runID, turn, item, items); err != nil {
			return injected, err
		}
		injected = true
	}
	return injected, nil
}

// commitContinuation commits the Message of item, a control Message the Loop
// continues with, and stores its MessageCommitted with the source that
// produced it. taken is the lease the Message was taken under.
func (a *Session) commitContinuation(ctx context.Context, runID RunID, turn TurnNumber, item control, taken *lease) *RuntimeError {
	message, source := item.message, item.source
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message.RunID = runID
	if err := a.commitMessage(message); err != nil {
		a.dropUncommitted(runID, turn, message.ID, source, taken)
		return asRuntimeError(err)
	}
	taken.done()
	a.stage(MessageCommitted{Header: NewHeader(a.def.id, runID, turn), Message: message.Clone(), Source: source})
	return a.flush(ctx)
}

// dropUncommitted stores the MessageDropped of a taken message that cannot be
// committed, and settles it: it would fail again in the next Run, so it does
// not go back to the queue. taken is nil for a message that never waited.
// [FAILURE-CLASS] a message whose commit failed is dropped, not put back, or every Run that takes it fails on it.
func (a *Session) dropUncommitted(runID RunID, turn TurnNumber, id MessageID, source MessageSource, taken *lease) {
	if taken == nil {
		return
	}
	a.stage(MessageDropped{Header: NewHeader(a.def.id, runID, turn), MessageID: id, Source: source, Why: "commit_failed"})
	taken.done()
}

// discardControl drops control Messages left over when a Run reaches its
// terminal Event, and stores a MessageDropped for each. Nothing may continue
// after RunEnded, so a leftover control Message must not silently reappear
// inside the next Run.
func (a *Session) discardControl(runID RunID, turn TurnNumber) {
	for _, queued := range a.queue.drain(false) {
		a.stage(MessageDropped{Header: NewHeader(a.def.id, runID, turn), MessageID: queued.message.ID, Source: queued.source, Why: "run_ended"})
	}
}
