package gotato

import (
	"encoding/json"
)

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
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum Message bytes exceeded", nil)
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
		return runtimeError(CodeInternalInvariant, "commitMessage", "cannot append to transcript: "+err.Error(), err)
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
