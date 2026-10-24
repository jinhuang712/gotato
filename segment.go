package gotato

import (
	"slices"
	"time"
)

// Segment is one stretch of a Session's history that a compaction closed (see
// GLOSSARY.md). It is written once, as its own Event (SegmentClosed), and
// never again: the Messages that render it are frozen at that moment, because
// a provider's prompt cache reads the bytes of the prefix. Rendering a
// Segment is the work of the code that closes it; the Record only keeps what
// that code hands it.
type Segment struct {
	// Number names the Segment in its Session. Numbers grow by one with
	// every Segment closed, and a Segment a fork inherits keeps its number.
	Number int `json:"number"`
	// From and To are the times the stretch began and ended.
	From time.Time `json:"from,omitzero"`
	To   time.Time `json:"to,omitzero"`
	// Source says which Entries of which Record the Segment covers, so the
	// original Messages can be read back by number range.
	Source SegmentSource `json:"source"`
	// Messages are the Messages that render the Segment in the Context,
	// word for word what the model is sent. They are never rewritten.
	Messages []Message `json:"messages,omitempty"`
	// Rendered is how many Messages render the Segment: len(Messages).
	Rendered int `json:"rendered"`
	// ToolCalls are the lines that stand for the Tool calls the Segment
	// closed over, in order.
	ToolCalls []ToolCallLine `json:"tool_calls,omitempty"`
	// Recap is the text the Segment ends with, or empty when the Recap
	// failed or none was asked for.
	Recap string `json:"recap,omitempty"`
	// ToolSets are the names of the ToolSets whose Tools the Segment's
	// stretch called.
	ToolSets []string `json:"tool_sets,omitempty"`
}

// SegmentSource says which part of which Record a Segment covers.
type SegmentSource struct {
	// SessionID names the Session whose Record holds the Entries, or is
	// empty when it is the Record the Segment is in. A Segment a fork
	// inherits names its parent here.
	SessionID SessionID `json:"session_id,omitempty"`
	// FromNumber and ToNumber are the numbers of the first and the last
	// Entry of the stretch, both included.
	FromNumber uint64 `json:"from_number,omitempty"`
	ToNumber   uint64 `json:"to_number,omitempty"`
}

// ToolCallLine is one Tool call of a Segment, as the one line that stands for
// it. A call repeated with the same arguments is one line with Count above 1.
type ToolCallLine struct {
	At     time.Time        `json:"at,omitzero"`
	ToolID string           `json:"tool_id"`
	Args   string           `json:"args,omitempty"`
	Status ToolResultStatus `json:"status,omitempty"`
	// Code is the error code of a call that failed.
	Code  string `json:"code,omitempty"`
	Count int    `json:"count,omitempty"`
}

// MessagePosition says where in the Record a Message was committed, and when.
type MessagePosition struct {
	ID     MessageID `json:"id"`
	Number uint64    `json:"number,omitempty"`
	Time   time.Time `json:"time,omitzero"`
}

// Clone returns a copy of the Segment that shares nothing with it.
func (s Segment) Clone() Segment {
	out := s
	out.Messages = make([]Message, len(s.Messages))
	for i, message := range s.Messages {
		out.Messages[i] = message.Clone()
	}
	out.ToolCalls = slices.Clone(s.ToolCalls)
	out.ToolSets = slices.Clone(s.ToolSets)
	return out
}
