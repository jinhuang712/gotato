package gotato

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
)

// SessionID names a Session. It stays the same wherever the Session is loaded.
type SessionID string

// RunID names one Run of a Session.
type RunID string

// TurnNumber counts the Turns of a Run, from 1.
type TurnNumber uint32

// MessageID names a Message in the Transcript.
type MessageID string

// ToolCallID names one Tool call, and the ToolResult that answers it.
type ToolCallID string

// Role says who a Message is from.
type Role string

// The roles of a Message. RoleUser is the user's, RoleAssistant the Model's,
// and RoleToolResult carries the ToolResult of one Tool call.
const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
	// RoleNotice is a Message that tells the model a runtime fact the user did
	// not say (see NoticeInfo). It starts no Round and is not the user's.
	RoleNotice Role = "notice"
)

// ContentKind says what a ContentPart holds.
type ContentKind string

// The kinds of ContentPart. Text and reasoning use Text; an image uses Data and
// MIMEType; JSON uses Text as the JSON document.
const (
	ContentKindText      ContentKind = "text"
	ContentKindReasoning ContentKind = "reasoning"
	ContentKindImage     ContentKind = "image"
	ContentKindJSON      ContentKind = "json"
)

// ContentPart is one piece of a Message or a ToolResult: text, reasoning, an
// image or JSON. Text carries the text kinds; Data and MIMEType carry an image.
type ContentPart struct {
	Kind     ContentKind `json:"kind"`
	Text     string      `json:"text,omitempty"`
	Data     []byte      `json:"data,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
	// Signature is an opaque provider artifact, for example encrypted
	// reasoning content required when replaying a stateless response API.
	// Core carries it across runtime operations but never interprets it.
	Signature []byte            `json:"signature,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Message is one entry in the Transcript: user input, an assistant reply, a
// tool result or a notice. Role says which, and the fields that do not fit a
// Role stay empty.
type Message struct {
	ID MessageID `json:"id,omitempty"`
	// RunID is the Run that committed the Message: its prompt, its replies,
	// its tool results and the messages it took in. It is empty for a Message
	// that did not come from a Run, such as history a Record starts with. It is a
	// record, not part of the request: ForModel drops it, so it never reaches a
	// provider.
	RunID      RunID         `json:"run_id,omitempty"`
	Role       Role          `json:"role"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolResult *ToolResult   `json:"tool_result,omitempty"`
	Usage      Usage         `json:"usage,omitzero"`
	StopReason StopReason    `json:"stop_reason,omitempty"`
	// Model is the model that wrote an assistant reply, as its provider
	// reported it, and is empty when the provider named none. It is a
	// record, not part of the request: ForModel drops it.
	Model string `json:"model,omitempty"`
	// Notice says what a notice is; it is set exactly on a Message with the
	// role RoleNotice.
	Notice *NoticeInfo `json:"notice,omitempty"`
}

// UserMessage returns a user Message of one text Part.
func UserMessage(text string) Message {
	return Message{Role: RoleUser, Parts: []ContentPart{{Kind: ContentKindText, Text: text}}}
}

// AssistantMessage returns an assistant Message of one text Part.
func AssistantMessage(text string) Message {
	return Message{Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentKindText, Text: text}}}
}

// TextOf returns what a message says: its text Parts, in order. Reasoning is
// not part of it; ReasoningOf returns that.
func TextOf(m Message) string { return partsText(m, ContentKindText) }

// ReasoningOf returns a message's reasoning Parts' text, in order: the
// model's thinking, which a provider may require back (DeepSeek's
// reasoning_content) but which is never the answer.
func ReasoningOf(m Message) string { return partsText(m, ContentKindReasoning) }

func partsText(m Message, kind ContentKind) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Kind == kind {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// Clone returns a deep copy of m: changing the copy's Parts, ToolCalls,
// ToolResult or Notice, or their byte slices and maps, never changes m.
func (m Message) Clone() Message {
	out := m
	out.Parts = cloneParts(m.Parts)
	out.ToolCalls = cloneToolCalls(m.ToolCalls)
	if m.ToolResult != nil {
		tr := m.ToolResult.Clone()
		out.ToolResult = &tr
	}
	if m.Notice != nil {
		notice := *m.Notice
		out.Notice = &notice
	}
	return out
}

// ToolCall is a call the Model made to a Tool: the Tool's ID and the JSON
// Arguments, which the Session validates against the Tool's input schema.
type ToolCall struct {
	ID        ToolCallID `json:"id"`
	ToolID    string     `json:"tool_id"`
	Arguments []byte     `json:"arguments"`
	// Signature is an opaque provider artifact, for example the provider's
	// own ID for the item that carried the call. The adapter that streamed
	// the call sets it and reads it back on replay. Core carries it across
	// runtime operations but never interprets it, and ID stays the call ID.
	Signature []byte `json:"signature,omitempty"`
}

// ToolResultStatus says how a Tool call ended.
type ToolResultStatus string

// The statuses of a ToolResult. ToolResultStatusBlocked means an Extension
// refused the call, so the Tool did not run (Executed is false).
const (
	ToolResultStatusOK       ToolResultStatus = "ok"
	ToolResultStatusBlocked  ToolResultStatus = "blocked"
	ToolResultStatusFailed   ToolResultStatus = "failed"
	ToolResultStatusCanceled ToolResultStatus = "canceled"
)

// ToolResult is what a Tool call returned, committed as a Message of the role
// RoleToolResult. A Tool sets Status, Content and Metadata; the Session sets
// CallID and Executed.
type ToolResult struct {
	CallID   ToolCallID        `json:"call_id"`
	Status   ToolResultStatus  `json:"status"`
	Content  []ContentPart     `json:"content,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// SafeError is the failure text the Model and subscribers read. The Session
	// fills it from an error a Tool returns only with what the error makes
	// public (see PublicError); a Tool may also set it itself.
	SafeError string `json:"safe_error,omitempty"`
	// Executed reports whether the Tool's executor was started. Core owns it:
	// a call that was blocked, rejected, or never started because the Run
	// ended has Executed false. A Tool canceled while it ran keeps it true,
	// because it may have done its work.
	Executed bool `json:"executed"`
	// Spilled is set when the text of the result was longer than one page: the
	// Content then holds a preview and a ref, and the full text is the Entry
	// at Spilled.Seq of the Record (see SpilledResult).
	Spilled *SpilledResult `json:"spilled,omitempty"`
	// Loaded lists the qualified IDs of the catalog Tools this result loaded
	// (search_tools, or a call to a Tool that was not loaded). A gateway that
	// defers tools natively shows each to the Model as a reference at this
	// place of the history, and a gateway that does not ignores the field.
	LoadedTools []string `json:"loaded_tools,omitempty"`
}

// Clone returns a deep copy of r: its Content, Metadata, LoadedTools and
// Spilled are not shared with r.
func (r ToolResult) Clone() ToolResult {
	out := r
	out.Content = cloneParts(r.Content)
	out.Metadata = maps.Clone(r.Metadata)
	out.LoadedTools = slices.Clone(r.LoadedTools)
	if r.Spilled != nil {
		spilled := *r.Spilled
		out.Spilled = &spilled
	}
	return out
}

// Usage counts tokens for one model call or an aggregate of calls.
// InputTokens covers every prompt token the provider processed, cached or
// not; CacheReadTokens and CacheWriteTokens are the parts of InputTokens served
// from and written to the provider's prompt cache.
type Usage struct {
	InputTokens      uint64 `json:"input_tokens,omitempty"`
	OutputTokens     uint64 `json:"output_tokens,omitempty"`
	TotalTokens      uint64 `json:"total_tokens,omitempty"`
	CacheReadTokens  uint64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens uint64 `json:"cache_write_tokens,omitempty"`
}

// StopReason is why a model call or a Turn stopped. A Message's StopReason is
// the Model's: StopReasonToolCalls when it called Tools, and
// StopReasonMaxTokens when the output limit cut it off, even next to Tool
// calls.
type StopReason string

// The stop reasons. The empty StopReason means no model call ended the Message.
const (
	StopReasonEndTurn   StopReason = "end_turn"
	StopReasonToolCalls StopReason = "tool_calls"
	StopReasonMaxTokens StopReason = "max_tokens"
	StopReasonCanceled  StopReason = "canceled"
	StopReasonError     StopReason = "error"
	// StopReasonCutOff marks a reply whose stream broke before the Model finished:
	// the Message keeps the text received so far. It stays in the Transcript
	// but is never sent to a Model again (ForModel leaves it out).
	StopReasonCutOff StopReason = "cut_off"
)

// RunStatus is how a Run stands: running, or how it ended.
type RunStatus string

// The statuses of a Run. Every status but RunStatusRunning is final.
const (
	RunStatusRunning          RunStatus = "running"
	RunStatusCompleted        RunStatus = "completed"
	RunStatusCanceled         RunStatus = "canceled"
	RunStatusDeadlineExceeded RunStatus = "deadline_exceeded"
	RunStatusFailed           RunStatus = "failed"
)

// RunMetrics counts what a Run did.
type RunMetrics struct {
	ElapsedMS      float64 `json:"elapsed_ms"`
	Turns          uint32  `json:"turns"`
	ToolCalls      uint32  `json:"tool_calls"`
	TextBytes      uint64  `json:"text_bytes"`
	ReasoningBytes uint64  `json:"reasoning_bytes"`
}

// RunResult is the settled result of a Run: its status, its final Message and
// its total Usage. Error is set when the Run did not complete.
type RunResult struct {
	RunID        RunID         `json:"run_id"`
	Status       RunStatus     `json:"status"`
	FinalMessage *Message      `json:"final_message,omitempty"`
	Usage        Usage         `json:"usage,omitzero"`
	Metrics      RunMetrics    `json:"metrics"`
	Error        *RuntimeError `json:"error,omitempty"`
}

// Clone returns a deep copy of r: its FinalMessage and Error are not shared
// with r.
func (r RunResult) Clone() RunResult {
	out := r
	if r.FinalMessage != nil {
		m := r.FinalMessage.Clone()
		out.FinalMessage = &m
	}
	if r.Error != nil {
		e := *r.Error
		e.Details = maps.Clone(r.Error.Details)
		out.Error = &e
	}
	return out
}

// nextID returns "<prefix>-<8 hex process nonce><counter hex>". The nonce is
// drawn once per process from crypto/rand, so IDs stay unique across
// restarts and across processes that share one Session store, while the
// counter keeps them ordered and cheap within a process.
func nextID(prefix string) string {
	id := globalID.Add(1)
	var buf [8]byte
	buf[0] = byte(id >> 56)
	buf[1] = byte(id >> 48)
	buf[2] = byte(id >> 40)
	buf[3] = byte(id >> 32)
	buf[4] = byte(id >> 24)
	buf[5] = byte(id >> 16)
	buf[6] = byte(id >> 8)
	buf[7] = byte(id)
	return prefix + "-" + processNonce + strings.TrimLeft(hex.EncodeToString(buf[:]), "0")
}

var globalID atomic.Uint64

var processNonce = func() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("gotato: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}()

func addUsage(total, current Usage) Usage {
	total.InputTokens += current.InputTokens
	total.OutputTokens += current.OutputTokens
	total.TotalTokens += current.TotalTokens
	total.CacheReadTokens += current.CacheReadTokens
	total.CacheWriteTokens += current.CacheWriteTokens
	return total
}
