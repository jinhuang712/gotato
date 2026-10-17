package gotato

import (
	"context"
	"errors"
	"time"
)

// Journal is an optional interface of a Transcript that makes a Run's steps
// durable as they happen, so a crash loses at most the step in flight. The
// Session calls Step on its own goroutine, after the step's Messages are
// committed through Append, and before it goes on: when a Step fails, the
// Run fails with the Step's error. session.Writer implements it.
type Journal interface {
	Step(ctx context.Context, step Step) error
}

// StepKind names a step of a Run.
type StepKind string

const (
	// StepPrompt: the prompt is committed.
	StepPrompt StepKind = "prompt"
	// StepModelCall: a model call is about to start. A Journal that cannot
	// write it keeps the model from being called.
	StepModelCall StepKind = "model_call"
	// StepReplyPart: part of a reply has streamed, in Text and Reasoning.
	// It comes every second or every few KiB, whichever is first.
	StepReplyPart StepKind = "reply_part"
	// StepReply: a reply is committed.
	StepReply StepKind = "reply"
	// StepCutOff: a reply cut off mid-stream is committed.
	StepCutOff StepKind = "cut_off"
	// StepToolStart: the Tool calls in Calls are about to run.
	StepToolStart StepKind = "tool_start"
	// StepToolResult: a Tool Result is committed.
	StepToolResult StepKind = "tool_result"
	// StepContinuation: a Steer, FollowUp or continuation Message is
	// committed.
	StepContinuation StepKind = "continuation"
)

// Step is one step of a Run that a Journal makes durable.
type Step struct {
	Kind  StepKind
	RunID RunID
	Turn  TurnNumber
	// MessageID is the Message this step committed, or, for StepReplyPart
	// and StepToolStart, the reply it belongs to.
	MessageID MessageID
	// Calls are the Tool calls a StepToolStart is about to run.
	Calls []ToolCallID
	// Text and Reasoning are what a StepReplyPart streamed since the
	// previous part.
	Text      string
	Reasoning string
}

// replyPartEvery and replyPartBytes bound how much of a streaming reply a
// crash can lose: a StepReplyPart is written every second, or as soon as this
// many bytes have streamed, whichever is first.
var (
	replyPartEvery = time.Second
	replyPartBytes = 4096
)

// journalStep reports a step to the Transcript's Journal, if it has one, and
// turns a failure into the Run's error. A conflict and a store failure keep
// their code; anything else is a store failure.
func (a *Session) journalStep(ctx context.Context, step Step) *RuntimeError {
	if a.journal == nil {
		return nil
	}
	err := a.journal.Step(ctx, step)
	if err == nil {
		return nil
	}
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) && (runtimeErr.Code == ErrConflict || runtimeErr.Code == ErrStoreFailure) {
		return runtimeErr
	}
	return runtimeError(ErrStoreFailure, "Journal", "cannot write the "+string(step.Kind)+" step: "+err.Error(), err)
}
