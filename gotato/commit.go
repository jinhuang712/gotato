package gotato

import (
	"encoding/json"
	"errors"
	"fmt"
)

// limitMessageBytes is the Details["limit"] of the error for a Message over
// CoreLimits.MaxMessageBytes.
const limitMessageBytes = "MaxMessageBytes"

// isMessageTooLarge reports whether err is the refusal of a Message over
// CoreLimits.MaxMessageBytes.
func isMessageTooLarge(err error) bool {
	var re *RuntimeError
	return errors.As(err, &re) && re != nil && re.Code == CodeLimitExceeded && re.Details["limit"] == limitMessageBytes
}

// oversizedResult is the failed Result that stands in for a Tool Result too
// large for one Message: the Tool ran (Executed stays true), and the Model
// reads why its output is not there.
func oversizedResult(result ToolResult, size int, limit uint64) ToolResult {
	text := fmt.Sprintf("tool result too large: %d bytes, limit %d bytes; the Tool ran, its output was not kept", size, limit)
	return ToolResult{
		CallID:    result.CallID,
		Status:    ToolResultStatusFailed,
		Content:   []ContentPart{{Kind: ContentKindText, Text: text}},
		SafeError: text,
		Executed:  result.Executed,
	}
}

// commitMessage commits one Message under the Message and transcript limits.
func (a *Session) commitMessage(message Message) error {
	return a.appendMessage(message, true)
}

// appendMessage commits one Message. enforce applies the Message and
// transcript limits; only the closing results of a failed Run skip them (see
// closeOpenCalls).
func (a *Session) appendMessage(message Message, enforce bool) error {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message = message.Clone()
	bytes, err := json.Marshal(message)
	if err != nil {
		return runtimeError(CodeInternalInvariant, "commitMessage", "cannot encode Message", err)
	}
	length, failure := a.transcriptLen()
	if failure != nil {
		return failure
	}
	if enforce && limitExceededUint32(a.limits.MaxMessages, uint32(length+1)) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum Messages exceeded", nil)
	}
	if enforce && limitExceededUint64(a.limits.MaxMessageBytes, uint64(len(bytes))) {
		e := runtimeError(CodeLimitExceeded, "commitMessage", "maximum Message bytes exceeded", nil)
		e.Details = map[string]string{"limit": limitMessageBytes}
		return e
	}
	// The transcript bound is tracked incrementally: one JSON array of n
	// elements costs the elements plus n-1 separators and two brackets.
	candidate := a.transcriptBytes + uint64(len(bytes))
	if length > 0 {
		candidate++
	}
	if enforce && limitExceededUint64(a.limits.MaxTranscriptBytes, candidate+2) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum transcript bytes exceeded", nil)
	}
	if err := a.appendTranscript(message); err != nil {
		return runtimeError(CodeInternalInvariant, "commitMessage", PublicText(err, "cannot append to transcript"), err)
	}
	a.transcriptBytes = candidate
	return nil
}

// measureTranscript recomputes the encoded transcript size once per Run so an
// externally mutated Transcript (a compacted or reloaded Session) is bounded
// correctly without re-encoding on every commit.
func (a *Session) measureTranscript() error {
	messages, failure := a.transcriptMessages()
	if failure != nil {
		return failure
	}
	if len(messages) == 0 {
		a.transcriptBytes = 0
		return nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return runtimeError(CodeInternalInvariant, "Run", "cannot encode transcript", err)
	}
	// Exclude the surrounding brackets; commitMessage adds them back.
	a.transcriptBytes = uint64(len(encoded)) - 2
	return nil
}

// controlSizeError is the refusal of a queued Message (Steer, FollowUp,
// Interrupt, Notice) that the commit at the boundary would refuse for its size:
// it is checked where the Message arrives, so a caller that is told "queued"
// is not later failed at the boundary. It measures the Message as the commit
// writes it, with the Run ID the commit sets. It is nil when the Message fits.
// [CONTRACT] a queued Message that the commit would refuse for its size is refused where it arrives, not after the caller is told "queued".
func (a *Session) controlSizeError(message Message, operation string) error {
	if a.limits.MaxMessageBytes == 0 {
		return nil
	}
	message.RunID = RunID(nextID("run"))
	encoded, err := json.Marshal(message)
	if err != nil {
		return runtimeError(CodeInvalidArgument, operation, "cannot encode "+operation+" message", err)
	}
	if !limitExceededUint64(a.limits.MaxMessageBytes, uint64(len(encoded))) {
		return nil
	}
	e := runtimeError(CodeLimitExceeded, operation, operation+" message is over the maximum Message bytes", nil)
	e.Details = map[string]string{"limit": limitMessageBytes}
	return e
}

// transcriptCall runs one read of the Transcript (or a method it has beside
// Append); a panic in it fails the Run, with a code.
// [LIVENESS] a panic in the host's Transcript would end the Session goroutine and the process instead of failing the Run.
func transcriptCall(call func()) *RuntimeError {
	err := callHost("Transcript", func() error { call(); return nil })
	if err == nil {
		return nil
	}
	return runtimeError(CodeInternalInvariant, "Transcript", PublicText(err, "Transcript failed"), err)
}

// transcriptLen is the Transcript's Len, guarded.
func (a *Session) transcriptLen() (n int, failure *RuntimeError) {
	failure = transcriptCall(func() { n = a.transcript.Len() })
	return n, failure
}

// transcriptMessages is the Transcript's Messages, guarded.
func (a *Session) transcriptMessages() (messages []Message, failure *RuntimeError) {
	failure = transcriptCall(func() { messages = a.transcript.Messages() })
	return messages, failure
}

// appendTranscript appends to the Transcript; a panic in it is an error.
func (a *Session) appendTranscript(message Message) (err error) {
	defer recoverPanic("Transcript", &err)
	return a.transcript.Append(message)
}
