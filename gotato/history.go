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

// sessionHistory is the Session's committed history: its Transcript, such as a
// record.Record, and the encoded size of what it holds. The Session goroutine
// is the Transcript's only writer during a Run, so the part has no lock. The
// size is tracked incrementally so a commit never re-encodes the history.
type sessionHistory struct {
	transcript Transcript
	// size is the encoded size of the Messages in the JSON array a Transcript
	// is bounded by, without its brackets.
	size uint64
}

// commit commits one Message under the Message and transcript limits.
func (h *sessionHistory) commit(limits CoreLimits, message Message) error {
	encoded, err := h.encode(message)
	if err != nil {
		return err
	}
	if err := encoded.withinLimits(limits); err != nil {
		return err
	}
	return h.appendEncoded(encoded)
}

// commitUnbounded commits one Message past the Message and transcript limits:
// only the closing results of a failed Run skip them (see closeOpenCalls).
func (h *sessionHistory) commitUnbounded(message Message) error {
	encoded, err := h.encode(message)
	if err != nil {
		return err
	}
	return h.appendEncoded(encoded)
}

// encodedMessage is a Message about to be committed, with its encoding and
// what the transcript would hold with it.
type encodedMessage struct {
	message Message
	bytes   []byte
	// length is the number of Messages before this one.
	length int
	// candidate is the encoded size of the transcript with this Message,
	// without its brackets.
	candidate uint64
}

// encode gives message an ID if it has none, and encodes a copy of it.
func (h *sessionHistory) encode(message Message) (encodedMessage, error) {
	if message.ID == "" {
		message.ID = MessageID(nextID("message"))
	}
	message = message.Clone()
	bytes, err := json.Marshal(message)
	if err != nil {
		return encodedMessage{}, runtimeError(CodeInternalInvariant, "commitMessage", "cannot encode Message", err)
	}
	length, failure := h.len()
	if failure != nil {
		return encodedMessage{}, failure
	}
	// The transcript bound is tracked incrementally: one JSON array of n
	// elements costs the elements plus n-1 separators and two brackets.
	candidate := h.size + uint64(len(bytes))
	if length > 0 {
		candidate++
	}
	return encodedMessage{message: message, bytes: bytes, length: length, candidate: candidate}, nil
}

// withinLimits fails when the Message, or the transcript with it, would pass
// limits.
func (e encodedMessage) withinLimits(limits CoreLimits) error {
	if limitExceededUint32(limits.MaxMessages, uint32(e.length+1)) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum Messages exceeded", nil)
	}
	if limitExceededUint64(limits.MaxMessageBytes, uint64(len(e.bytes))) {
		failure := runtimeError(CodeLimitExceeded, "commitMessage", "maximum Message bytes exceeded", nil)
		failure.Details = map[string]string{"limit": limitMessageBytes}
		return failure
	}
	if limitExceededUint64(limits.MaxTranscriptBytes, e.candidate+jsonBracketsBytes) {
		return runtimeError(CodeLimitExceeded, "commitMessage", "maximum transcript bytes exceeded", nil)
	}
	return nil
}

// appendEncoded appends the encoded Message to the transcript.
func (h *sessionHistory) appendEncoded(encoded encodedMessage) error {
	if err := h.appendGuarded(encoded.message); err != nil {
		return runtimeError(CodeInternalInvariant, "commitMessage", PublicText(err, "cannot append to transcript"), err)
	}
	h.size = encoded.candidate
	return nil
}

// commitToolResult commits the Message of a Tool Result. The Tool already ran,
// so a Result too large for one Message is not a failure of the Run: it is
// replaced by a failed Result that tells the Model why its output is missing,
// and that is committed in its place. It returns the Message and the Result as
// committed.
func (h *sessionHistory) commitToolResult(limits CoreLimits, message Message, result ToolResult) (Message, ToolResult, error) {
	err := h.commit(limits, message)
	if isMessageTooLarge(err) {
		size, _ := json.Marshal(message)
		result = oversizedResult(result, len(size), limits.MaxMessageBytes)
		message.Parts = cloneParts(result.Content)
		message.ToolResult = cloneToolResultPtr(result)
		err = h.commit(limits, message)
	}
	return message, result, err
}

// commitMessage commits one Message under the Message and transcript limits.
func (a *Session) commitMessage(message Message) error {
	return a.history.commit(a.def.limits, message)
}

// measure recomputes the encoded transcript size once per Run so an externally
// mutated Transcript (a compacted or reloaded Session) is bounded correctly
// without re-encoding on every commit.
func (h *sessionHistory) measure() error {
	messages, failure := h.messages()
	if failure != nil {
		return failure
	}
	if len(messages) == 0 {
		h.size = 0
		return nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return runtimeError(CodeInternalInvariant, "Run", "cannot encode transcript", err)
	}
	// Exclude the surrounding brackets; commit adds them back.
	h.size = uint64(len(encoded)) - jsonBracketsBytes
	return nil
}

// controlSizeError is the refusal of a queued Message (Steer, FollowUp,
// Interrupt, Notice) that the commit at the boundary would refuse for its size:
// it is checked where the Message arrives, so a caller that is told "queued"
// is not later failed at the boundary. It measures the Message as the commit
// writes it, with the Run ID the commit sets. It is nil when the Message fits.
// [CONTRACT] a queued Message that the commit would refuse for its size is refused where it arrives, not after the caller is told "queued".
func controlSizeError(limits CoreLimits, message Message, operation string) error {
	if limits.MaxMessageBytes == 0 {
		return nil
	}
	message.RunID = RunID(nextID("run"))
	encoded, err := json.Marshal(message)
	if err != nil {
		return runtimeError(CodeInvalidArgument, operation, "cannot encode "+operation+" message", err)
	}
	if !limitExceededUint64(limits.MaxMessageBytes, uint64(len(encoded))) {
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

// len is the Transcript's Len, guarded.
func (h *sessionHistory) len() (n int, failure *RuntimeError) {
	failure = transcriptCall(func() { n = h.transcript.Len() })
	return n, failure
}

// messages is the Transcript's Messages, guarded.
func (h *sessionHistory) messages() (messages []Message, failure *RuntimeError) {
	failure = transcriptCall(func() { messages = h.transcript.Messages() })
	return messages, failure
}

// capabilities is the Transcript's declaration now, guarded. It is asked again
// where it can change, as Durable does for a Record that gets stored.
func (h *sessionHistory) capabilities() (caps Capabilities, failure *RuntimeError) {
	failure = transcriptCall(func() { caps = h.transcript.Capabilities() })
	return caps, failure
}

// appendGuarded appends to the Transcript; a panic in it is an error.
func (h *sessionHistory) appendGuarded(message Message) (err error) {
	defer recoverPanic("Transcript", &err)
	return h.transcript.Append(message)
}
