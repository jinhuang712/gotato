package gotato

import (
	"context"
	"errors"
	"io"
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

// writeNow writes Events of their own, between steps: a queued message, an
// Abort. It is safe to call from any goroutine.
func (a *Session) writeNow(ctx context.Context, events ...Event) *RuntimeError {
	return a.write(ctx, events)
}

// write keeps events in the Journal and sends every Event it wrote, under one
// lock, so that Events are sent in the order of their numbers.
func (a *Session) write(ctx context.Context, events []Event) *RuntimeError {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	written, err := a.journal.Write(ctx, events)
	if err != nil {
		return journalError(err)
	}
	for _, event := range written {
		if number := event.Head().Number; number > a.lastNumber.Load() {
			a.lastNumber.Store(number)
		}
		a.feed.Publish(event)
	}
	return nil
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
		case ContentText:
			text += uint64(len(part.Text))
		case ContentReasoning:
			reasoning += uint64(len(part.Text))
		}
	}
	return text, reasoning
}

// executeRun runs the one canonical Loop. A nil prompt is a Continue: the Run
// appends no user Message and picks up the committed transcript as it stands.
func (a *Session) executeRun(ctx context.Context, prompt *Message) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if a.limits.RunDeadline > 0 {
		var deadlineCancel context.CancelFunc
		runCtx, deadlineCancel = context.WithTimeout(runCtx, a.limits.RunDeadline)
		defer deadlineCancel()
	}

	runID := RunID(nextID("run"))
	a.runMu.Lock()
	a.currentRunID = runID
	a.currentTurn = 0
	a.currentRunStop = cancel
	a.runMu.Unlock()
	defer func() {
		a.runMu.Lock()
		if a.currentRunID == runID {
			a.currentRunID = ""
			a.currentTurn = 0
			a.currentRunStop = nil
		}
		a.runMu.Unlock()
	}()
	// A Run that ends before a commit boundary must not leave a staged ToolSet
	// behind for the next Run to activate unasked.
	defer a.registry.abortPending()
	a.inRun.Store(true)
	defer a.inRun.Store(false)
	if a.onRunStart != nil {
		a.onRunStart(runID)
	}

	var totalUsage Usage
	var final *Message
	var toolCalls uint32
	var turns uint32
	var truncatedContinuations uint32
	var textBytes, reasoningBytes uint64
	runStarted := time.Now()
	runMetrics := func() RunMetrics {
		return RunMetrics{
			ElapsedMS:      time.Since(runStarted).Milliseconds(),
			Turns:          turns,
			ToolCalls:      toolCalls,
			TextBytes:      textBytes,
			ReasoningBytes: reasoningBytes,
		}
	}
	setTurn := func(turn TurnNumber) {
		a.runMu.Lock()
		a.currentTurn = turn
		a.runMu.Unlock()
	}
	hd := func(turn TurnNumber) Header { return NewHeader(a.id, runID, turn) }
	// batch is the Tool batch of the last committed assistant Message, while
	// some of its calls still have no committed result.
	var batch openBatch

	// end stores the Run's last Events, RunEnded among them. A Run may not
	// end without exactly one RunEnded, so it is staged even when the Run
	// failed on a write: a Journal that has stopped refuses it too, and then
	// nothing was stored, and nothing is sent.
	end := func(turn TurnNumber, result RunResult, extra func(*RunEnded)) {
		a.discardControl(runID, turn)
		a.waitForAborts()
		metrics := runMetrics()
		ended := RunEnded{
			Header:         hd(turn),
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
		_ = a.flush(ctx)
	}
	currentTurn := func() TurnNumber {
		a.runMu.Lock()
		defer a.runMu.Unlock()
		return a.currentTurn
	}
	fail := func(err *RuntimeError) (RunResult, error) {
		if err == nil {
			err = runtimeError(ErrInternalInvariant, "Run", "nil terminal error", nil)
		}
		result := RunResult{RunID: runID, Status: RunFailed, Usage: totalUsage, Metrics: runMetrics(), Error: err}
		if errors.Is(err.Cause, context.Canceled) || err.Code == ErrCancelled {
			result.Status = RunCanceled
		}
		if err.Code == ErrDeadlineExceeded {
			result.Status = RunDeadlineExceeded
		}
		turn := currentTurn()
		a.closeOpenCalls(runID, &batch, err, result.Status)
		end(turn, result, nil)
		return result, err
	}

	a.stage(RunStarted{Header: hd(0)})
	if err := a.extensions.prepareRun(runCtx, a.transcript); err != nil {
		return fail(err)
	}
	if err := a.measureTranscript(); err != nil {
		return fail(asRuntimeError(err))
	}

	if prompt != nil {
		prompt.ID = MessageID(nextID("message"))
		if err := a.commitMessage(*prompt); err != nil {
			return fail(asRuntimeError(err))
		}
		a.stage(MessageCommitted{Header: hd(0), Message: prompt.Clone(), Source: SourcePrompt})
		if err := a.flush(runCtx); err != nil {
			return fail(err)
		}
	}

	for turn := TurnNumber(1); ; turn++ {
		if limitExceededUint32(a.limitsSet, a.limits.MaxTurns, uint32(turn)) {
			return fail(runtimeError(ErrLimitExceeded, "Run", "maximum Turns exceeded", nil))
		}
		if err := contextFailure(runCtx, "turn"); err != nil {
			return fail(err)
		}
		turns++
		setTurn(turn)
		a.stage(TurnStarted{Header: hd(turn)})
		turnStarted := time.Now()

		// The Tool surface is refreshed at the Turn boundary so every Tool the
		// Model is shown in this request resolves for the whole Turn.
		if err := a.refreshTools(); err != nil {
			return fail(asRuntimeError(err))
		}

		snapshot := ContextSnapshot{
			SessionID:          a.id,
			RunID:              runID,
			Turn:               turn,
			SystemInstructions: a.instruction,
			Messages:           cloneMessages(a.transcript.Messages()),
		}
		built, request, prefix, assembleErr := a.assembleTurn(runCtx, snapshot)
		if assembleErr != nil {
			return fail(assembleErr)
		}
		contextBuilt := ContextBuilt{
			Header:           hd(turn),
			Strategy:         built.Metadata["strategy"],
			Messages:         len(request.Messages),
			SourceMessages:   len(snapshot.Messages),
			SelectedMessages: len(built.Messages),
			PrefixHash:       prefix,
			PrefixMessages:   max(len(request.Messages)-1, 0),
			SystemBytes:      len(request.SystemInstructions),
			Tools:            len(request.Tools),
		}
		// The builder's Metadata keeps its own key, so it can never replace
		// a count above with its string form.
		if len(built.Metadata) > 0 {
			contextBuilt.Metadata = make(map[string]string, len(built.Metadata))
			for key, value := range built.Metadata {
				contextBuilt.Metadata[key] = value
			}
		}
		// The model call is written before the Model is called, so a writer
		// that can no longer write stops here, and the store's time stays out
		// of the model's.
		assistantID := MessageID(nextID("message"))
		a.stage(contextBuilt, ReplyStarted{Header: hd(turn), MessageID: assistantID})
		if err := a.flush(runCtx); err != nil {
			return fail(err)
		}
		assistant, usage, modelErr := a.readAssistant(runCtx, runID, turn, assistantID, request)
		totalUsage = addUsage(totalUsage, usage)
		turnText, turnReasoning := replyBytes(assistant)
		textBytes += turnText
		reasoningBytes += turnReasoning
		if modelErr != nil {
			// The text the Model sent before its stream broke is committed,
			// so the Transcript keeps it; the Run still fails as it did.
			if assistant.StopReason == StopCutOff && a.commitMessage(assistant) == nil {
				// The Run fails with the model's error either way; a cut-off
				// reply that cannot be written fails the Run's last write.
				a.stage(ReplyCutOff{Header: hd(turn), Message: assistant.Clone(), Cause: modelErr.Code})
				_ = a.flush(runCtx)
			}
			return fail(modelErr)
		}
		// A Message with Tool calls stopped for them, unless the output limit
		// cut it off: StopMaxTokens stays, so a TurnStopper sees the cut-off.
		if len(assistant.ToolCalls) > 0 && assistant.StopReason != StopMaxTokens {
			assistant.StopReason = StopToolCalls
		}
		if err := a.commitMessage(assistant); err != nil {
			return fail(asRuntimeError(err))
		}
		if len(assistant.ToolCalls) > 0 {
			batch = openBatch{turn: turn, calls: assistant.ToolCalls}
		}
		a.stage(ReplyEnded{Header: hd(turn), Message: assistant.Clone(), Model: assistant.Model, StopReason: assistant.StopReason, ToolCalls: len(assistant.ToolCalls)})
		if err := a.flush(runCtx); err != nil {
			return fail(err)
		}

		settle := func(stopped bool, reason string) (RunResult, error) {
			finalCopy := assistant.Clone()
			final = &finalCopy
			result := RunResult{RunID: runID, Status: RunCompleted, FinalMessage: final, Usage: totalUsage, Metrics: runMetrics()}
			end(turn, result, func(e *RunEnded) {
				if stopped {
					e.StoppedByExtension = true
					e.StopReason = reason
				}
			})
			return result, nil
		}

		if len(assistant.ToolCalls) == 0 {
			a.stage(TurnEnded{
				Header: hd(turn), StopReason: assistant.StopReason, Usage: usage,
				ElapsedMS: time.Since(turnStarted).Milliseconds(), TextBytes: turnText, ReasoningBytes: turnReasoning,
			})
			// A TurnStopper runs after TurnEnded and before continuation
			// selection. A stop preserves the Turn and settles the Run.
			decision, stopErr := a.extensions.stopTurn(runCtx, TurnSnapshot{
				SessionID: a.id, RunID: runID, Turn: turn, Assistant: assistant.Clone(), StopReason: assistant.StopReason,
			})
			if stopErr != nil {
				return fail(stopErr)
			}
			if decision.Stop {
				return settle(true, decision.Reason)
			}
			// Safe boundary at settlement: Steer and FollowUp both apply here.
			// Cancellation is checked first so a queued continuation is never
			// committed to a Run that is already ending.
			if err := contextFailure(runCtx, "continuation"); err != nil {
				return fail(err)
			}
			injected, controlErr := a.consumeControl(runCtx, runID, turn, true)
			if controlErr != nil {
				return fail(controlErr)
			}
			if injected {
				continue
			}
			// A Turn cut off at the output limit before any Tool Call is not
			// an answer: the Model ran out of room, often while reasoning.
			// Within the configured bound the Run continues instead of
			// settling on it.
			if assistant.StopReason == StopMaxTokens && truncatedContinuations < a.limits.MaxTruncatedContinuations {
				truncatedContinuations++
				if err := a.commitContinuation(runCtx, runID, turn, UserMessage(TruncatedContinuationText), SourceContinuation); err != nil {
					return fail(err)
				}
				continue
			}
			return settle(false, "")
		}

		// Preflight is source ordered: resolve, validate, and run the Pre
		// chain before any executor starts.
		plans, planErr := a.preflightTools(runCtx, runID, turn, assistant.ToolCalls, &toolCalls)
		if planErr != nil {
			return fail(planErr)
		}
		// A blocked Tool is already settled at preflight: it ends without
		// having started.
		outcomes := make(map[int]ToolResult, len(plans))
		batch.outcomes = outcomes
		for i := range plans {
			if plans[i].blocked {
				outcomes[i] = plans[i].result
				a.stage(ToolEnded{Header: hd(turn), CallID: plans[i].call.ID, ToolID: plans[i].call.ToolID, Status: plans[i].result.Status, Executed: plans[i].result.Executed})
			}
		}

		// Results commit in source order, as soon as every call before them
		// has settled: after each group runs, and for blocked calls, which
		// settled at preflight. So a crash loses at most the group in flight.
		toolOutcomes := make([]ToolOutcome, 0, len(plans))
		settledResults := make([]ToolResult, 0, len(plans))
		committed := 0
		commitUpTo := func(end int) *RuntimeError {
			for ; committed < end; committed++ {
				call := plans[committed].call
				toolResult := outcomes[committed]
				// Post-Tool-Use sees executed, blocked, failed, and cancelled
				// outcomes alike, in reverse installation order.
				finalized, postErr := a.extensions.afterTool(runCtx, toolResult)
				if postErr != nil {
					return postErr
				}
				toolResult = finalized
				if err := validateToolResultSize(a.limits, a.limitsSet, toolResult); err != nil {
					return err
				}
				content := cloneContent(toolResult.Content)
				resultMessage := Message{ID: MessageID(nextID("message")), Role: RoleToolResult, Parts: content, ToolResult: ptrToolResult(toolResult)}
				if err := a.commitMessage(resultMessage); err != nil {
					return asRuntimeError(err)
				}
				batch.next = committed + 1
				a.stage(ToolResultCommitted{Header: hd(turn), Message: resultMessage.Clone(), CallID: call.ID, Status: toolResult.Status})
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
				return fail(err)
			}
			completed := a.executeToolGroup(runCtx, runID, turn, plans, group)
			// A cancelled group still reports what it knows, so the closing
			// results of the failed Run keep Executed true for started Tools.
			for index, result := range completed {
				outcomes[index] = result
			}
			if err := commitUpTo(group[len(group)-1] + 1); err != nil {
				return fail(err)
			}
		}
		if err := commitUpTo(len(plans)); err != nil {
			return fail(err)
		}
		batch = openBatch{}

		// ToolSet activation commits at the batch boundary, so newly active
		// Tools appear only in the next Model request. registryMu serializes
		// the visibility write with ToolInspector.Tools on other goroutines.
		a.registryMu.Lock()
		activated, activationErr := a.registry.commitPending()
		a.registryMu.Unlock()
		if activationErr != nil {
			return fail(asRuntimeError(activationErr))
		}
		for _, name := range activated {
			a.stage(ToolSetActivated{Header: hd(turn), ToolSet: name})
		}

		a.stage(TurnEnded{
			Header: hd(turn), StopReason: StopToolCalls, Usage: usage,
			ElapsedMS: time.Since(turnStarted).Milliseconds(), TextBytes: turnText, ReasoningBytes: turnReasoning,
			ToolCalls: len(assistant.ToolCalls), Tools: toolOutcomes,
		})
		decision, stopErr := a.extensions.stopTurn(runCtx, TurnSnapshot{
			SessionID: a.id, RunID: runID, Turn: turn, Assistant: assistant.Clone(), ToolResults: settledResults, StopReason: StopToolCalls,
		})
		if stopErr != nil {
			return fail(stopErr)
		}
		if decision.Stop {
			return settle(true, decision.Reason)
		}
		// Safe boundary between Turns: Steer applies, FollowUp waits for
		// settlement.
		if err := contextFailure(runCtx, "continuation"); err != nil {
			return fail(err)
		}
		if _, controlErr := a.consumeControl(runCtx, runID, turn, false); controlErr != nil {
			return fail(controlErr)
		}
	}
}

// consumeControl commits the control Messages that apply at this boundary. It
// runs inside the Session execution unit, so committing them keeps the Session
// goroutine as the only mutation authority for the transcript.
func (a *Session) consumeControl(ctx context.Context, runID RunID, turn TurnNumber, settling bool) (bool, *RuntimeError) {
	injected := false
	commit := func(message Message, source MessageSource) *RuntimeError {
		if err := a.commitContinuation(ctx, runID, turn, message, source); err != nil {
			return err
		}
		injected = true
		return nil
	}
	for {
		select {
		case message := <-a.steer:
			if err := commit(message, SourceSteer); err != nil {
				return injected, err
			}
			continue
		default:
		}
		break
	}
	if !settling {
		return injected, nil
	}
	for {
		select {
		case message := <-a.followUp:
			if err := commit(message, SourceFollowUp); err != nil {
				return injected, err
			}
			continue
		default:
		}
		break
	}
	return injected, nil
}

// commitContinuation commits a Message the Loop continues with (a control
// Message, or the truncation continuation) and stores its MessageCommitted
// with the source that produced it.
func (a *Session) commitContinuation(ctx context.Context, runID RunID, turn TurnNumber, message Message, source MessageSource) *RuntimeError {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
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
		case message := <-a.steer:
			drop(message, SourceSteer)
		case message := <-a.followUp:
			drop(message, SourceFollowUp)
		default:
			return
		}
	}
}

func (a *Session) readAssistant(ctx context.Context, runID RunID, turn TurnNumber, assistantID MessageID, request ModelRequest) (Message, Usage, *RuntimeError) {
	modelCtx, cancel := boundedContext(ctx, a.limits.ModelCallDeadline)
	defer cancel()
	modelCtx = withRetryReporter(modelCtx, func(retry ModelRetry) {
		// A retry falls between steps: it gets a write of its own.
		_ = a.writeNow(ctx, ModelRetried{Header: NewHeader(a.id, runID, turn), Attempt: retry.Attempt, WaitMS: retry.Wait.Milliseconds(), Error: retry.Error})
	})
	stream, err := a.model.Stream(modelCtx, request)
	if err != nil {
		return Message{}, Usage{}, classifyModelError(err)
	}
	if stream == nil {
		return Message{}, Usage{}, runtimeError(ErrModelFailure, "Model.Stream", "Model returned a nil stream", nil)
	}
	defer func() { _ = stream.Close() }()

	assistant := Message{ID: assistantID, Role: RoleAssistant}
	var usage Usage
	var complete bool
	// Once the stream has started, a failure keeps the text received so far
	// as a cut-off reply, so what the Model said is not lost.
	broken := func(err *RuntimeError) (Message, Usage, *RuntimeError) {
		partial := cutOffReply(assistant)
		partial.Usage = usage
		return partial, usage, err
	}
	// A streaming reply is stored in parts, every second or every few KiB, so
	// a crash loses at most the last part of it. Without a store there is
	// nothing to lose.
	var partText, partReasoning strings.Builder
	partStarted := time.Now()
	writePart := func() *RuntimeError {
		if !a.durable {
			return nil
		}
		size := partText.Len() + partReasoning.Len()
		if size == 0 || (size < replyPartBytes && time.Since(partStarted) < replyPartEvery) {
			return nil
		}
		a.stage(ReplyPartKept{Header: NewHeader(a.id, runID, turn), MessageID: assistant.ID, Text: partText.String(), Reasoning: partReasoning.String()})
		partText.Reset()
		partReasoning.Reset()
		partStarted = time.Now()
		return a.flush(ctx)
	}
	for {
		modelEvent, recvErr := stream.Recv(modelCtx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) && complete {
				break
			}
			if errors.Is(recvErr, io.EOF) {
				return broken(runtimeError(ErrModelProtocolFailure, "ModelStream", "stream ended without completion", recvErr))
			}
			return broken(classifyModelError(recvErr))
		}
		if complete {
			return broken(runtimeError(ErrModelProtocolFailure, "ModelStream", "event arrived after completion", nil))
		}
		if modelEvent.Model != "" {
			assistant.Model = modelEvent.Model
		}
		switch modelEvent.Kind {
		case ModelTextDelta:
			if modelEvent.Text != "" {
				assistant.Parts = append(assistant.Parts, ContentPart{Kind: ContentText, Text: modelEvent.Text})
				a.send(TextDelta{Header: a.deltaHeader(runID, turn), MessageID: assistant.ID, Text: modelEvent.Text})
				partText.WriteString(modelEvent.Text)
				if err := writePart(); err != nil {
					return broken(err)
				}
			}
		case ModelReasoningDelta:
			if modelEvent.Text != "" {
				assistant.Parts = append(assistant.Parts, ContentPart{Kind: ContentReasoning, Text: modelEvent.Text})
				a.send(ReasoningDelta{Header: a.deltaHeader(runID, turn), MessageID: assistant.ID, Text: modelEvent.Text})
				partReasoning.WriteString(modelEvent.Text)
				if err := writePart(); err != nil {
					return broken(err)
				}
			}
		case ModelReasoningDone:
			// The Done signs the block its deltas opened. A block that
			// streamed no deltas (redacted, or with its display omitted)
			// finds the last reasoning Part already signed, and gets a Part
			// of its own instead of overwriting that signature.
			partIndex := -1
			for i := len(assistant.Parts) - 1; i >= 0; i-- {
				if assistant.Parts[i].Kind == ContentReasoning {
					partIndex = i
					break
				}
			}
			if partIndex == -1 || len(assistant.Parts[partIndex].Signature) > 0 {
				assistant.Parts = append(assistant.Parts, ContentPart{Kind: ContentReasoning})
				partIndex = len(assistant.Parts) - 1
			}
			assistant.Parts[partIndex].Signature = slices.Clone(modelEvent.ReasoningArtifact)
		case ModelToolCall:
			if modelEvent.ToolCall == nil || modelEvent.ToolCall.ID == "" || modelEvent.ToolCall.ToolID == "" {
				return broken(runtimeError(ErrModelProtocolFailure, "ModelStream", "invalid Tool Call", nil))
			}
			for _, existing := range assistant.ToolCalls {
				if existing.ID == modelEvent.ToolCall.ID {
					return broken(runtimeError(ErrModelProtocolFailure, "ModelStream", "duplicate Tool Call ID", nil))
				}
			}
			call := *modelEvent.ToolCall
			call.Arguments = slices.Clone(call.Arguments)
			call.Signature = slices.Clone(call.Signature)
			assistant.ToolCalls = append(assistant.ToolCalls, call)
		case ModelUsage:
			usage = modelEvent.Usage
		case ModelDone:
			complete = true
			assistant.StopReason = modelEvent.StopReason
			if assistant.StopReason == "" {
				assistant.StopReason = StopEndTurn
			}
			if modelEvent.Usage.TotalTokens != 0 || modelEvent.Usage.InputTokens != 0 || modelEvent.Usage.OutputTokens != 0 {
				usage = modelEvent.Usage
			}
		default:
			return broken(runtimeError(ErrModelProtocolFailure, "ModelStream", "unknown Model event", nil))
		}
		if complete {
			break
		}
	}
	if !complete {
		return broken(runtimeError(ErrModelProtocolFailure, "ModelStream", "missing completion", nil))
	}
	assistant.Parts = coalesceDeltas(assistant.Parts)
	assistant.Usage = usage
	return assistant, usage, nil
}
