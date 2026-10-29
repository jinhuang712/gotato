package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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
	st.a.runMu.Lock()
	st.a.currentTurn = turn
	st.a.runMu.Unlock()
}

func (st *runState) currentTurn() TurnNumber {
	st.a.runMu.Lock()
	defer st.a.runMu.Unlock()
	return st.a.currentTurn
}

func (st *runState) header(turn TurnNumber) Header { return NewHeader(st.a.id, st.runID, turn) }

// end stores the Run's last Events, RunEnded among them. A Run may not
// end without exactly one RunEnded, so it is staged even when the Run
// failed on a write: a Journal that has stopped refuses it too, and then
// nothing was stored, and nothing is sent.
func (st *runState) end(turn TurnNumber, result RunResult, extra func(*RunEnded)) *RuntimeError {
	a := st.a
	// From here on no Abort can start a write, and no Steer, FollowUp or
	// Interrupt is taken into a queue (see sendControl): what is queued now is
	// dropped below, and nothing slips in behind that.
	a.runMu.Lock()
	a.runEnding = true
	a.runKeeps = a.keepsQueued(result.Status)
	a.runMu.Unlock()
	if !a.runKeeps {
		a.discardControl(st.runID, turn)
	}
	// The Aborts that did start a write have finished: an abort_requested
	// never follows run_ended.
	a.waitForAborts()
	if a.endingHook != nil {
		a.endingHook()
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

// settle ends the Run as completed with assistant as its final Message. When
// the write that ends it fails, the Run failed instead.
func (st *runState) settle(turn TurnNumber, assistant Message, stopped bool, reason string) (RunResult, error) {
	final := assistant.Clone()
	result := RunResult{RunID: st.runID, Status: RunStatusCompleted, FinalMessage: &final, Usage: st.usage, Metrics: st.metrics()}
	writeErr := st.end(turn, result, func(e *RunEnded) {
		if stopped {
			e.StoppedByExtension = true
			e.StopReason = reason
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
	a, runCtx, runID, hd := st.a, st.runCtx, st.runID, st.header
	// build makes the request of this Turn from the Transcript as it
	// stands. A TurnPreparer that rewrote the Transcript has it built again.
	build := func() (turnBuild, *RuntimeError) {
		snapshot := ContextSnapshot{
			SessionID:          a.id,
			RunID:              runID,
			Turn:               turn,
			SystemInstructions: a.instruction,
			Messages:           cloneMessages(a.transcript.Messages()),
		}
		built, request, prefix, err := a.assembleTurn(runCtx, snapshot)
		return turnBuild{snapshot: snapshot, built: built, request: request, prefix: prefix}, err
	}
	current, assembleErr := build()
	if assembleErr != nil {
		return Message{}, Usage{}, nil, assembleErr
	}

	// The Model is called here, once, and once more when it refuses the
	// request as too long before any output and a TurnPreparer shortened it.
	var (
		assistantID MessageID
		refused     *RuntimeError
	)
	for {
		changed, prepareErr := a.prepareTurn(runCtx, runID, turn, runStart, current.request, asError(refused))
		if prepareErr != nil {
			return Message{}, Usage{}, nil, prepareErr
		}
		if refused != nil && !changed {
			// Nothing shortened the request: the refusal stands.
			assistant, usage, modelErr = Message{}, Usage{}, refused
			break
		}
		if changed {
			if err := a.measureTranscript(); err != nil {
				return Message{}, Usage{}, nil, asRuntimeError(err)
			}
			a.syncLoadedTools()
			if err := a.refreshToolsInRun(runCtx); err != nil {
				return Message{}, Usage{}, nil, err
			}
			if current, assembleErr = build(); assembleErr != nil {
				return Message{}, Usage{}, nil, assembleErr
			}
		}
		contextBuilt := ContextBuilt{
			Header:           hd(turn),
			Strategy:         current.built.Metadata["strategy"],
			Messages:         len(current.request.Messages),
			SourceMessages:   len(current.snapshot.Messages),
			SelectedMessages: len(current.built.Messages),
			PrefixHash:       current.prefix,
			PrefixMessages:   max(len(current.request.Messages)-1, 0),
			SystemBytes:      len(current.request.SystemInstructions),
			Tools:            len(current.request.Tools),
		}
		// The builder's Metadata keeps its own key, so it can never replace
		// a count above with its string form.
		if len(current.built.Metadata) > 0 {
			contextBuilt.Metadata = maps.Clone(current.built.Metadata)
		}
		// The model call is written before the Model is called, so a writer
		// that can no longer write stops here, and the store's time stays out
		// of the model's.
		assistantID = MessageID(nextID("message"))
		a.stage(contextBuilt, ReplyStarted{Header: hd(turn), MessageID: assistantID})
		if err := a.flush(runCtx); err != nil {
			return Message{}, Usage{}, nil, err
		}
		var callUsage Usage
		assistant, callUsage, modelErr = a.readAssistant(runCtx, runID, turn, assistantID, current.request)
		usage = callUsage
		st.usage = addUsage(st.usage, callUsage)
		// A refusal before any output is the one failure a shorter request
		// can mend, once per Turn.
		if modelErr != nil && modelErr.Code == CodeContextOverflow && refused == nil &&
			len(assistant.Parts) == 0 && len(assistant.ToolCalls) == 0 {
			refused = modelErr
			continue
		}
		break
	}
	return assistant, usage, modelErr, nil
}

// runToolBatch runs the Tool calls of assistant, the reply of this Turn: it
// checks them in source order, runs them in groups, commits their results in
// source order and commits the Tools they loaded. It returns what the calls
// came to. A non-nil failure is an error of the Run.
func (st *runState) runToolBatch(turn TurnNumber, assistant Message) (toolOutcomes []ToolOutcome, settledResults []ToolResult, failure *RuntimeError) {
	a, runCtx, runID, hd := st.a, st.runCtx, st.runID, st.header
	// Preflight is source ordered: resolve, validate, and run the Pre
	// chain before any executor starts.
	plans, planErr := a.preflightTools(runCtx, runID, turn, assistant.ToolCalls, &st.toolCalls)
	if planErr != nil {
		return nil, nil, planErr
	}
	// A blocked Tool is already settled at preflight: it ends without
	// having started.
	outcomes := make(map[int]ToolResult, len(plans))
	st.batch.outcomes = outcomes
	for i := range plans {
		if plans[i].blocked {
			outcomes[i] = plans[i].result
			a.stage(ToolEnded{Header: hd(turn), CallID: plans[i].call.ID, ToolID: plans[i].call.ToolID, Status: plans[i].result.Status, Executed: plans[i].result.Executed})
		}
	}

	// Results commit in source order, as soon as every call before them
	// has settled: after each group runs, and for blocked calls, which
	// settled at preflight. So a crash loses at most the group in flight.
	toolOutcomes = make([]ToolOutcome, 0, len(plans))
	settledResults = make([]ToolResult, 0, len(plans))
	committed := 0
	commitUpTo := func(end int) *RuntimeError {
		for ; committed < end; committed++ {
			call := plans[committed].call
			toolResult := outcomes[committed]
			// Post-Tool-Use sees executed, blocked, failed, and canceled
			// outcomes alike, in reverse installation order.
			finalized, postErr := a.extensions.afterTool(runCtx, toolResult)
			if postErr != nil {
				return postErr
			}
			toolResult = finalized
			// The Tool already ran: a canceled Run still stores its body.
			spillCtx, cancelSpill := finalContext(runCtx)
			toolResult, spillErr := a.spill(spillCtx, runID, turn, call.ToolID, toolResult)
			cancelSpill()
			if spillErr != nil {
				return spillErr
			}
			content := cloneParts(toolResult.Content)
			resultMessage := Message{ID: MessageID(nextID("message")), RunID: runID, Role: RoleToolResult, Parts: content, ToolResult: cloneToolResultPtr(toolResult)}
			commitErr := a.commitMessage(resultMessage)
			if isMessageTooLarge(commitErr) {
				// The Tool already ran: the Model is told why its output is
				// missing, and the Run goes on.
				size, _ := json.Marshal(resultMessage)
				toolResult = oversizedResult(toolResult, len(size), a.limits.MaxMessageBytes)
				resultMessage.Parts = cloneParts(toolResult.Content)
				resultMessage.ToolResult = cloneToolResultPtr(toolResult)
				commitErr = a.commitMessage(resultMessage)
			}
			if commitErr != nil {
				return asRuntimeError(commitErr)
			}
			st.batch.next = committed + 1
			a.stage(ToolResultCommitted{Header: hd(turn), Message: resultMessage.Clone(), CallID: call.ID, Status: toolResult.Status})
			// A Tool loads as the Tool Result that lists it commits, and its
			// ToolLoaded goes in the same write, so the Record never says
			// "Loaded" for a Tool it does not hold as loaded. registryMu
			// serializes the visibility write with Session.Tools.
			for _, id := range toolResult.LoadedTools {
				a.registryMu.Lock()
				loadedNow := a.registry.load(id)
				a.registryMu.Unlock()
				if loadedNow {
					a.stage(ToolLoaded{Header: hd(turn), ToolID: id, Why: loadWhy(call, id)})
				}
			}
			// The result is committed in memory already: a canceled
			// Run still stores it.
			flushCtx, cancelFlush := finalContext(runCtx)
			err := a.flush(flushCtx)
			cancelFlush()
			if err != nil {
				return err
			}
			settledResults = append(settledResults, toolResult.Clone())
			toolOutcomes = append(toolOutcomes, ToolOutcome{ToolID: call.ToolID, CallID: call.ID, Status: toolResult.Status, Executed: toolResult.Executed})
		}
		return nil
	}

	// Execution may be sequential or bounded parallel. Completion Events
	// follow actual completion order; commitment stays source ordered.
	for _, group := range groupPlans(plans, a.parallelWorkers()) {
		// The calls are written before they run, so a crash during them
		// leaves calls that are known to have started.
		for _, index := range group {
			a.stage(ToolStarted{Header: hd(turn), MessageID: assistant.ID, CallID: plans[index].call.ID, ToolID: plans[index].call.ToolID})
		}
		if err := a.flush(runCtx); err != nil {
			return nil, nil, err
		}
		completed := a.executeToolGroup(runCtx, runID, turn, plans, group)
		// A canceled group still reports what it knows, so the closing
		// results of the failed Run keep Executed true for started Tools.
		maps.Copy(outcomes, completed)
		if err := commitUpTo(group[len(group)-1] + 1); err != nil {
			return nil, nil, err
		}
	}
	if err := commitUpTo(len(plans)); err != nil {
		return nil, nil, err
	}
	st.batch = openBatch{}
	return toolOutcomes, settledResults, nil
}

// stopTurn asks the TurnStoppers whether the Run stops after this Turn.
func (st *runState) stopTurn(turn TurnNumber, assistant Message, results []ToolResult, reason StopReason) (StopDecision, *RuntimeError) {
	return st.a.extensions.stopTurn(st.runCtx, TurnSnapshot{
		SessionID: st.a.id, RunID: st.runID, Turn: turn, Assistant: assistant.Clone(), ToolResults: results, StopReason: reason,
	})
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

	runID, endRun := a.beginRun(cancel)
	defer endRun()

	st := &runState{a: a, ctx: ctx, runCtx: runCtx, runID: runID, started: time.Now()}
	a.stage(RunStarted{Header: st.header(0), RequestID: a.requestID})
	if err := a.refreshToolSets(runCtx, st.header(0)); err != nil {
		return st.fail(err)
	}
	runStart := a.transcript.Len()
	if err := a.measureTranscript(); err != nil {
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
// the function that unregisters it when the Run is over.
func (a *Session) beginRun(cancel context.CancelFunc) (RunID, func()) {
	runID := RunID(nextID("run"))
	a.runMu.Lock()
	a.currentRunID = runID
	a.currentTurn = 0
	a.runEnding = false
	a.runKeeps = false
	a.currentRunStop = cancel
	a.runMu.Unlock()
	a.inRun.Store(true)
	if a.onRunStart != nil {
		a.onRunStart(runID)
	}
	return runID, func() {
		a.inRun.Store(false)
		a.runMu.Lock()
		if a.currentRunID == runID {
			a.currentRunID = ""
			a.currentTurn = 0
			a.currentRunStop = nil
		}
		a.runMu.Unlock()
	}
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
func (st *runState) turnEnded(t *turnRun, stop StopReason) TurnEnded {
	return newTurnEnded(st.header(t.turn), stop, t.usage, t.started, t.text, t.reasoning)
}

// failTurn ends the Run as failed by err. It is fail in the form runTurn
// returns: the Run does not go on.
func (st *runState) failTurn(err *RuntimeError) (bool, RunResult, error) {
	result, runErr := st.fail(err)
	return false, result, runErr
}

// settleTurn ends the Run as completed with assistant as its final Message.
func (st *runState) settleTurn(turn TurnNumber, assistant Message, stopped bool, reason string) (bool, RunResult, error) {
	result, err := st.settle(turn, assistant, stopped, reason)
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
		return st.afterModelError(t, modelErr)
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
		return st.endAnswerTurn(t, replyNumber)
	}
	return st.endToolTurn(t)
}

// afterModelError settles a Turn whose Model call failed: an Interrupt cut it
// short and the Run goes on, or the Run fails with the Model's error.
func (st *runState) afterModelError(t *turnRun, modelErr *RuntimeError) (bool, RunResult, error) {
	a, hd := st.a, st.header
	if modelErr.Code == CodeCanceled && st.runCtx.Err() == nil && a.interrupting() {
		// An Interrupt cut the call short: the text it had sent is kept as
		// a Cut-off reply, and the Interrupt starts the next Turn.
		if t.assistant.StopReason == StopReasonCutOff && a.commitMessage(t.assistant) == nil {
			a.stage(ReplyCutOff{Header: hd(t.turn), Message: t.assistant.Clone(), Cause: modelErr.Code})
		}
		a.stage(st.turnEnded(t, StopReasonCutOff))
		if _, controlErr := a.consumeControl(st.runCtx, st.runID, t.turn, false); controlErr != nil {
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
func (st *runState) endAnswerTurn(t *turnRun, replyNumber uint64) (bool, RunResult, error) {
	a, turn, assistant := st.a, t.turn, t.assistant
	a.stage(st.turnEnded(t, assistant.StopReason))
	// A TurnStopper runs after TurnEnded and before continuation
	// selection. A stop preserves the Turn and settles the Run.
	decision, stopErr := st.stopTurn(turn, assistant, nil, assistant.StopReason)
	if stopErr != nil {
		return st.failTurn(stopErr)
	}
	if decision.Stop {
		return st.settleTurn(turn, assistant, true, decision.Reason)
	}
	// Safe boundary at settlement: Steer and FollowUp both apply here.
	// Cancellation is checked first so a queued continuation is never
	// committed to a Run that is already ending.
	injected, controlErr := st.boundary(turn, true)
	if controlErr != nil {
		return st.failTurn(controlErr)
	}
	if injected {
		return true, RunResult{}, nil
	}
	// A Turn cut off at the output limit before any Tool Call is not an
	// answer: the Model ran out of room, often while reasoning. Within the
	// configured bound the Run continues instead of settling on it.
	if assistant.StopReason == StopReasonMaxTokens && st.truncatedContinuations < a.limits.MaxTruncatedContinuations {
		st.truncatedContinuations++
		// A notice, not a user Message: it starts no Round, so the
		// reasoning of the cut reply stays in the request.
		if err := a.commitNotice(st.runCtx, st.runID, turn, NewNotice(NoticeSourceGotato, NoticeContinue, TruncatedContinuationText), 0, replyNumber); err != nil {
			return st.failTurn(err)
		}
		return true, RunResult{}, nil
	}
	return st.settleTurn(turn, assistant, false, "")
}

// endToolTurn runs the Tool calls of a Turn's reply and ends the Turn: the Run
// goes on to the next Turn unless a TurnStopper stops it.
func (st *runState) endToolTurn(t *turnRun) (bool, RunResult, error) {
	a, turn, assistant := st.a, t.turn, t.assistant
	toolOutcomes, settledResults, failure := st.runToolBatch(turn, assistant)
	if failure != nil {
		return st.failTurn(failure)
	}
	turnEnded := st.turnEnded(t, StopReasonToolCalls)
	turnEnded.ToolCalls, turnEnded.Tools = len(assistant.ToolCalls), toolOutcomes
	a.stage(turnEnded)
	decision, stopErr := st.stopTurn(turn, assistant, settledResults, StopReasonToolCalls)
	if stopErr != nil {
		return st.failTurn(stopErr)
	}
	if decision.Stop {
		return st.settleTurn(turn, assistant, true, decision.Reason)
	}
	// Safe boundary between Turns: Steer applies, FollowUp waits for
	// settlement.
	if _, controlErr := st.boundary(turn, false); controlErr != nil {
		return st.failTurn(controlErr)
	}
	return true, RunResult{}, nil
}

// turnBuild is the request of a Turn with what it was built from.
type turnBuild struct {
	snapshot ContextSnapshot
	built    ModelContext
	request  ModelRequest
	prefix   string
}

// prepareTurn gives the TurnPreparers their chance before a model call. It
// reports whether one rewrote the Transcript.
func (a *Session) prepareTurn(ctx context.Context, runID RunID, turn TurnNumber, runStart int, request ModelRequest, refusal error) (bool, *RuntimeError) {
	if len(a.extensions.preparers) == 0 {
		return false, nil
	}
	return a.extensions.prepareTurn(ctx, PendingTurn{
		SessionID:  a.id,
		RunID:      runID,
		Turn:       turn,
		Transcript: a.transcript,
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
	var items []control
	for {
		select {
		case queued := <-a.steer:
			if queued.source == MessageSourceInterrupt {
				a.interruptTaken()
			}
			items = append(items, queued)
			continue
		default:
		}
		break
	}
	if settling {
		for {
			select {
			case queued := <-a.followUp:
				items = append(items, queued)
				continue
			default:
			}
			break
		}
	}
	if len(items) > 0 || !settling {
		items = mergeNotices(items, a.takeNotices())
	}
	injected := false
	for _, item := range items {
		if item.source == MessageSourceNotice {
			if err := a.commitNotice(ctx, runID, turn, item.message, item.number, 0); err != nil {
				return injected, err
			}
			continue
		}
		if err := a.commitContinuation(ctx, runID, turn, item.message, item.source); err != nil {
			return injected, err
		}
		injected = true
	}
	return injected, nil
}

// commitContinuation commits a Message the Loop continues with (a control
// Message) and stores its MessageCommitted
// with the source that produced it.
func (a *Session) commitContinuation(ctx context.Context, runID RunID, turn TurnNumber, message Message, source MessageSource) *RuntimeError {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message.RunID = runID
	if err := a.commitMessage(message); err != nil {
		return asRuntimeError(err)
	}
	a.stage(MessageCommitted{Header: NewHeader(a.id, runID, turn), Message: message.Clone(), Source: source})
	return a.flush(ctx)
}

// discardControl drops control Messages left over when a Run reaches its
// terminal Event, and stores a MessageDropped for each. Nothing may continue
// after RunEnded, so a leftover control Message must not silently reappear
// inside the next Run.
func (a *Session) discardControl(runID RunID, turn TurnNumber) {
	drop := func(message Message, source MessageSource) {
		a.stage(MessageDropped{Header: NewHeader(a.id, runID, turn), MessageID: message.ID, Source: source, Why: "run_ended"})
	}
	for {
		select {
		case queued := <-a.steer:
			if queued.source == MessageSourceInterrupt {
				a.interruptTaken()
			}
			drop(queued.message, queued.source)
		case queued := <-a.followUp:
			drop(queued.message, queued.source)
		default:
			return
		}
	}
}

// newTurnEnded builds the TurnEnded of a Turn that ran from started until now.
func newTurnEnded(header Header, stop StopReason, usage Usage, started time.Time, textBytes, reasoningBytes uint64) TurnEnded {
	return TurnEnded{
		Header: header, StopReason: stop, Usage: usage,
		ElapsedMS: millis(time.Since(started)), TextBytes: textBytes, ReasoningBytes: reasoningBytes,
	}
}
