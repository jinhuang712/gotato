// Package ref is the form of a ref: the short text a Segment, a merge's
// digest or Recap and the preview of a spilled Tool Result carry to name one
// original that stays readable in the Record.
//
// A ref is `result:<call_id>` for a Tool Result and `msg:<message_id>` for a
// Message. It is a contract, not a helper: a Segment carries its refs for as
// long as the Record lives, so the form never changes, and a reader of
// refs (the read_result tool, a user's own) and a writer (a Builder, the
// core's spill) agree on it through this package. It imports only the
// standard library.
package ref

import "strings"

// The IDs of the two built-in tools that read originals back. A Segment names
// them in its text, so they cannot be renamed.
const (
	// ReadResult is the ID of the tool that returns the original of one ref,
	// a Tool Result or a Message, page by page.
	ReadResult = "read_result"
	// ReadSegment is the ID of the tool that returns one Segment's Messages
	// word for word, page by page.
	ReadSegment = "read_segment"
)

// The prefixes of the two forms of a ref.
const (
	// ResultPrefix starts the ref of a Tool Result, `result:<call_id>`.
	ResultPrefix = "result:"
	// MessagePrefix starts the ref of a Message, `msg:<message_id>`. It names
	// the Message by its ID, which every Instance and every fork of a Record
	// shares, not by an Entry number, which means something only in the
	// Record that wrote it.
	MessagePrefix = "msg:"
)

// Kind says what a Ref names.
type Kind int

const (
	// KindResult is a Tool Result, named by its call's ID.
	KindResult Kind = iota + 1
	// KindMessage is a user or assistant Message, named by its ID.
	KindMessage
)

// Ref names one original in the Record.
type Ref struct {
	Kind Kind
	// ID is the call ID of a KindResult and the Message ID of a KindMessage.
	ID string
}

// Result returns the ref of the Tool Result of the call with ID call.
func Result(call string) Ref { return Ref{Kind: KindResult, ID: call} }

// Message returns the ref of the Message with ID id.
func Message(id string) Ref { return Ref{Kind: KindMessage, ID: id} }

// String writes the ref in its one form, `result:<id>` or `msg:<id>`. The zero
// Ref is the empty string.
func (r Ref) String() string {
	switch r.Kind {
	case KindResult:
		return ResultPrefix + r.ID
	case KindMessage:
		return MessagePrefix + r.ID
	}
	return ""
}

// Parse reads a ref, ignoring the space around it. It returns false for text
// that is not a ref: no known prefix, or nothing after it.
func Parse(s string) (Ref, bool) {
	s = strings.TrimSpace(s)
	if id, ok := strings.CutPrefix(s, ResultPrefix); ok && id != "" {
		return Result(id), true
	}
	if id, ok := strings.CutPrefix(s, MessagePrefix); ok && id != "" {
		return Message(id), true
	}
	return Ref{}, false
}

// Safe reports whether an ID can stand inside a ref written into a Segment:
// not empty, and with no space, control character, quote, bracket or angle
// bracket, so a ref ends where the line says it does.
func Safe(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("\\\"[]<>", r) {
			return false
		}
	}
	return true
}
