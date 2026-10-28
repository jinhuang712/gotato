package gotato

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// nowHeader returns the Header of an Event that happens now, in the Run in
// flight if there is one. It is safe to call from any goroutine.
func (a *Session) nowHeader() Header {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	return NewHeader(a.id, a.currentRunID, a.currentTurn)
}

// stage queues stored Events of the step in progress. They ride the write of
// the step, in flush, and reach subscribers only after it. Only the Session
// goroutine stages.
func (a *Session) stage(events ...Event) {
	a.staged = append(a.staged, events...)
}

// flush writes the staged Events, and then sends them. A failed write fails
// the Run: nothing that was not stored is sent.
func (a *Session) flush(ctx context.Context) *RuntimeError {
	if len(a.staged) == 0 {
		return nil
	}
	events := a.staged
	a.staged = nil
	return a.write(ctx, events)
}

// flushNumbered is flush that also returns the number of the last staged Event
// of the given kind, 0 when it wrote none.
func (a *Session) flushNumbered(ctx context.Context, kind EventKind) (uint64, *RuntimeError) {
	if len(a.staged) == 0 {
		return 0, nil
	}
	events := a.staged
	a.staged = nil
	written, err := a.writeEvents(ctx, events)
	if err != nil {
		return 0, err
	}
	var number uint64
	for _, event := range written {
		if event.Kind() == kind {
			number = event.Head().Number
		}
	}
	return number, nil
}

// writeNow writes Events of their own, between steps: a queued message, an
// Abort. It is safe to call from any goroutine.
func (a *Session) writeNow(ctx context.Context, events ...Event) *RuntimeError {
	return a.write(ctx, events)
}

// write keeps events in the Journal and sends every Event it wrote, under one
// lock, so that Events are sent in the order of their numbers.
func (a *Session) write(ctx context.Context, events []Event) *RuntimeError {
	_, err := a.writeEvents(ctx, events)
	return err
}

// writeEvents is write that also returns the Events it wrote, numbered. What
// it sends is without the body of a spilled result.
func (a *Session) writeEvents(ctx context.Context, events []Event) ([]Event, *RuntimeError) {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	written, err := a.journal.Write(ctx, events)
	if err != nil {
		return nil, journalError(err)
	}
	for _, event := range written {
		if number := event.Head().Number; number > a.lastNumber.Load() {
			a.lastNumber.Store(number)
		}
		a.feed.Publish(WithoutBody(event))
	}
	return written, nil
}

// send sends a delta at once. It is marked with the number of the stored Event
// it follows, and never stored.
func (a *Session) send(event Event) {
	a.feed.Publish(event)
}

// deltaHeader is the Header of a delta: the Number is that of the last stored
// Event, which the delta follows.
func (a *Session) deltaHeader(runID RunID, turn TurnNumber) Header {
	header := NewHeader(a.id, runID, turn)
	header.Number = a.lastNumber.Load()
	return header
}

// reportExtension stores an ExtensionFailed Event for an advisory extension
// that failed during a Run, on the Session goroutine.
func (a *Session) reportExtension(stage string, failure *RuntimeError) {
	if !a.inRun.Load() {
		return
	}
	a.stage(ExtensionFailed{Header: a.nowHeader(), Stage: stage, Code: failure.Code})
}

// replyBytes counts the text and the reasoning of a reply.
func replyBytes(message Message) (text, reasoning uint64) {
	for _, part := range message.Parts {
		switch part.Kind {
		case ContentKindText:
			text += uint64(len(part.Text))
		case ContentKindReasoning:
			reasoning += uint64(len(part.Text))
		}
	}
	return text, reasoning
}

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
	if !a.keepsQueued(result.Status) {
		a.discardControl(st.runID, turn)
	}
	// No Abort can start a write from here on, and the ones that did
	// have finished: an abort_requested never follows run_ended.
	a.runMu.Lock()
	a.runEnding = true
	a.runMu.Unlock()
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
	return a.flush(st.ctx)
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
			if err := a.syncLoadedTools(); err != nil {
				return Message{}, Usage{}, nil, asRuntimeError(err)
			}
			if err := a.refreshTools(); err != nil {
				return Message{}, Usage{}, nil, asRuntimeError(err)
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
			toolResult, spillErr := a.spill(runCtx, runID, turn, call.ToolID, toolResult)
			if spillErr != nil {
				return spillErr
			}
			content := cloneParts(toolResult.Content)
			resultMessage := Message{ID: MessageID(nextID("message")), RunID: runID, Role: RoleToolResult, Parts: content, ToolResult: cloneToolResultPtr(toolResult)}
			if err := a.commitMessage(resultMessage); err != nil {
				return asRuntimeError(err)
			}
			st.batch.next = committed + 1
			a.stage(ToolResultCommitted{Header: hd(turn), Message: resultMessage.Clone(), CallID: call.ID, Status: toolResult.Status})
			for _, load := range a.registry.announce(toolResult.LoadedTools) {
				a.stage(ToolLoaded{Header: hd(turn), ToolID: load.id, Why: load.why})
			}
			if err := a.flush(runCtx); err != nil {
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

	// A load commits at the batch boundary, so a loaded Tool appears only
	// in the next Model request. registryMu serializes the visibility
	// write with Session.Tools on other goroutines.
	a.registryMu.Lock()
	loads, loadErr := a.registry.commitPending()
	a.registryMu.Unlock()
	if loadErr != nil {
		return nil, nil, asRuntimeError(loadErr)
	}
	for _, load := range loads {
		if load.announced {
			continue
		}
		a.stage(ToolLoaded{Header: hd(turn), ToolID: load.id, Why: load.why})
	}
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
	a.stage(RunStarted{Header: st.header(0)})
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
	a.currentRunStop = cancel
	a.runMu.Unlock()
	a.inRun.Store(true)
	if a.onRunStart != nil {
		a.onRunStart(runID)
	}
	return runID, func() {
		a.inRun.Store(false)
		// A Run that ends before a commit boundary must not leave a staged
		// load behind for the next Run to commit unasked.
		a.registry.abortPending()
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
	if refreshErr := a.refreshTools(); refreshErr != nil {
		return st.failTurn(asRuntimeError(refreshErr))
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

// readAssistant streams one reply from the Model and returns it with its
// Usage. After the stream has started, a failure returns what was received as
// a cut-off reply together with the error.
func (a *Session) readAssistant(ctx context.Context, runID RunID, turn TurnNumber, assistantID MessageID, request ModelRequest) (Message, Usage, *RuntimeError) {
	modelCtx, cancel := boundedContext(ctx, a.limits.ModelCallDeadline)
	defer cancel()
	// An Interrupt cancels this call; one that is waiting already does, now.
	a.intrMu.Lock()
	a.callCancel = cancel
	if a.intrPending > 0 {
		cancel()
	}
	a.intrMu.Unlock()
	defer func() {
		a.intrMu.Lock()
		a.callCancel = nil
		a.intrMu.Unlock()
	}()
	reader := &assistantReader{
		a: a, ctx: ctx, runID: runID, turn: turn,
		assistant:   Message{ID: assistantID, RunID: runID, Role: RoleAssistant},
		partStarted: time.Now(),
	}
	modelCtx = withRetryReporter(modelCtx, reader.retried)
	stream, err := a.model.Stream(modelCtx, request)
	if err != nil {
		return Message{}, Usage{}, classifyModelError(err)
	}
	if stream == nil {
		return Message{}, Usage{}, runtimeError(CodeModelFailure, "Model.Stream", "Model returned a nil stream", nil)
	}
	defer func() { _ = stream.Close() }()
	return reader.read(modelCtx, stream)
}

// assistantReader is the state of one reply while its stream is read: the
// Message built so far, its Usage, and the text not yet stored as a part.
type assistantReader struct {
	a         *Session
	ctx       context.Context // the Run's, for writes; the stream has its own
	runID     RunID
	turn      TurnNumber
	assistant Message
	usage     Usage
	complete  bool
	// A streaming reply is stored in parts, every second or every few KiB, so
	// a crash loses at most the last part of it. Without a store there is
	// nothing to lose.
	partText, partReasoning strings.Builder
	partStarted             time.Time
}

// read receives Model events until the stream completes, and returns the
// reply.
func (r *assistantReader) read(modelCtx context.Context, stream ModelStream) (Message, Usage, *RuntimeError) {
	for !r.complete {
		modelEvent, recvErr := stream.Recv(modelCtx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return r.broken(runtimeError(CodeModelProtocolFailure, "ModelStream", "stream ended without completion", recvErr))
			}
			return r.broken(classifyModelError(recvErr))
		}
		if err := r.apply(modelEvent); err != nil {
			return r.broken(err)
		}
	}
	r.assistant.Parts = coalesceDeltas(r.assistant.Parts)
	r.assistant.Usage = r.usage
	return r.assistant, r.usage, nil
}

// broken ends a reply whose stream has started and failed: what was received
// is kept as a cut-off reply, so what the Model said is not lost.
func (r *assistantReader) broken(err *RuntimeError) (Message, Usage, *RuntimeError) {
	partial := cutOffReply(r.assistant)
	partial.Usage = r.usage
	return partial, r.usage, err
}

// apply adds one Model event to the reply.
func (r *assistantReader) apply(modelEvent ModelEvent) *RuntimeError {
	a := r.a
	if modelEvent.Model != "" {
		r.assistant.Model = modelEvent.Model
	}
	switch modelEvent.Kind {
	case ModelEventKindTextDelta:
		if modelEvent.Text != "" {
			r.assistant.Parts = append(r.assistant.Parts, ContentPart{Kind: ContentKindText, Text: modelEvent.Text})
			a.send(TextDelta{Header: a.deltaHeader(r.runID, r.turn), MessageID: r.assistant.ID, Text: modelEvent.Text})
			r.partText.WriteString(modelEvent.Text)
			return r.writePart()
		}
	case ModelEventKindReasoningDelta:
		if modelEvent.Text != "" {
			r.assistant.Parts = append(r.assistant.Parts, ContentPart{Kind: ContentKindReasoning, Text: modelEvent.Text})
			a.send(ReasoningDelta{Header: a.deltaHeader(r.runID, r.turn), MessageID: r.assistant.ID, Text: modelEvent.Text})
			r.partReasoning.WriteString(modelEvent.Text)
			return r.writePart()
		}
	case ModelEventKindReasoningDone:
		// The Done signs the block its deltas opened (see signReasoning).
		r.assistant.Parts = signReasoning(r.assistant.Parts, modelEvent.ReasoningArtifact)
	case ModelEventKindToolCall:
		calls, callErr := appendToolCall(r.assistant.ToolCalls, modelEvent.ToolCall)
		if callErr != nil {
			return callErr
		}
		r.assistant.ToolCalls = calls
	case ModelEventKindUsage:
		r.usage = modelEvent.Usage
	case ModelEventKindDone:
		r.complete = true
		r.assistant.StopReason = modelEvent.StopReason
		if r.assistant.StopReason == "" {
			r.assistant.StopReason = StopReasonEndTurn
		}
		if modelEvent.Usage.TotalTokens != 0 || modelEvent.Usage.InputTokens != 0 || modelEvent.Usage.OutputTokens != 0 {
			r.usage = modelEvent.Usage
		}
	default:
		return runtimeError(CodeModelProtocolFailure, "ModelStream", "unknown Model event", nil)
	}
	return nil
}

// writePart stores the text received since the last part, once there is
// enough of it or it is old enough.
func (r *assistantReader) writePart() *RuntimeError {
	if !r.a.isDurable() {
		return nil
	}
	size := r.partText.Len() + r.partReasoning.Len()
	if size == 0 || (size < replyPartBytes && time.Since(r.partStarted) < replyPartEvery) {
		return nil
	}
	r.a.stage(ReplyPartKept{Header: NewHeader(r.a.id, r.runID, r.turn), MessageID: r.assistant.ID, Text: r.partText.String(), Reasoning: r.partReasoning.String()})
	r.resetPart()
	return r.a.flush(r.ctx)
}

// resetPart empties the pending part and restarts its clock.
func (r *assistantReader) resetPart() {
	r.partText.Reset()
	r.partReasoning.Reset()
	r.partStarted = time.Now()
}

// retried handles the Model's report that it sends the request again.
func (r *assistantReader) retried(retry ModelRetry) {
	a := r.a
	retried := ModelRetried{Header: NewHeader(a.id, r.runID, r.turn), Attempt: retry.Attempt, WaitMS: millis(retry.Wait), Error: retry.Error}
	if len(r.assistant.Parts) == 0 && len(r.assistant.ToolCalls) == 0 {
		// A retry before any output falls between steps: it gets a write of
		// its own.
		_ = a.writeNow(r.ctx, retried)
		return
	}
	// The stream was cut after some output and the same request is sent
	// again. What was received is void: the text is kept as a log, as a
	// Cut-off reply the model never sees, and the new reply is a reply of its
	// own, so a reader that saw model_retried drops the old one.
	a.stage(retried)
	if partial := cutOffReply(r.assistant); partial.ID != "" && a.commitMessage(partial) == nil {
		a.stage(ReplyCutOff{Header: NewHeader(a.id, r.runID, r.turn), Message: partial.Clone(), Cause: CodeModelFailure})
	}
	r.assistant = Message{ID: MessageID(nextID("message")), RunID: r.runID, Role: RoleAssistant, Model: r.assistant.Model}
	r.usage = Usage{}
	r.resetPart()
	a.stage(ReplyStarted{Header: NewHeader(a.id, r.runID, r.turn), MessageID: r.assistant.ID})
	_ = a.flush(r.ctx)
}

// newTurnEnded builds the TurnEnded of a Turn that ran from started until now.
func newTurnEnded(header Header, stop StopReason, usage Usage, started time.Time, textBytes, reasoningBytes uint64) TurnEnded {
	return TurnEnded{
		Header: header, StopReason: stop, Usage: usage,
		ElapsedMS: millis(time.Since(started)), TextBytes: textBytes, ReasoningBytes: reasoningBytes,
	}
}

// signReasoning attaches a reasoning artifact to the reasoning block the
// Model just finished. A block that streamed no deltas (redacted, or with its
// display omitted) finds the last reasoning Part already signed, and gets a
// Part of its own instead of overwriting that signature.
func signReasoning(parts []ContentPart, artifact []byte) []ContentPart {
	index := -1
	for i, part := range slices.Backward(parts) {
		if part.Kind == ContentKindReasoning {
			index = i
			break
		}
	}
	if index == -1 || len(parts[index].Signature) > 0 {
		parts = append(parts, ContentPart{Kind: ContentKindReasoning})
		index = len(parts) - 1
	}
	parts[index].Signature = slices.Clone(artifact)
	return parts
}

// appendToolCall adds a call the Model made to calls, as a copy. A call
// without an ID or a Tool, or with an ID already used, is a protocol failure.
func appendToolCall(calls []ToolCall, call *ToolCall) ([]ToolCall, *RuntimeError) {
	if call == nil || call.ID == "" || call.ToolID == "" {
		return nil, runtimeError(CodeModelProtocolFailure, "ModelStream", "invalid Tool Call", nil)
	}
	for _, existing := range calls {
		if existing.ID == call.ID {
			return nil, runtimeError(CodeModelProtocolFailure, "ModelStream", "duplicate Tool Call ID", nil)
		}
	}
	added := *call
	added.Arguments = slices.Clone(added.Arguments)
	added.Signature = slices.Clone(added.Signature)
	return append(calls, added), nil
}
