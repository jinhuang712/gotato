package gotato

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
)

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
