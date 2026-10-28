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
	if enforce && limitExceededUint32(a.limits.MaxMessages, uint32(a.transcript.Len()+1)) {
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
	if a.transcript.Len() > 0 {
		candidate++
	}
	if enforce && limitExceededUint64(a.limits.MaxTranscriptBytes, candidate+2) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum transcript bytes exceeded", nil)
	}
	if err := a.transcript.Append(message); err != nil {
		return runtimeError(CodeInternalInvariant, "commitMessage", publicMessage(err, "cannot append to transcript"), err)
	}
	a.transcriptBytes = candidate
	return nil
}

// measureTranscript recomputes the encoded transcript size once per Run so an
// externally mutated Transcript (a compacted or reloaded Session) is bounded
// correctly without re-encoding on every commit.
func (a *Session) measureTranscript() error {
	messages := a.transcript.Messages()
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
