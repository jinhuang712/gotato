package gotato

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

type AgentID string
type AgentName string
type ConversationID string
type ConversationKey string
type RunID string
type TurnNumber uint32
type MessageID string
type ToolCallID string
type SpawnID string
type AgentGeneration uint64

type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

type ContentKind string

const (
	ContentText      ContentKind = "text"
	ContentReasoning ContentKind = "reasoning"
	ContentImage     ContentKind = "image"
	ContentJSON      ContentKind = "json"
)

type ContentPart struct {
	Kind     ContentKind `json:"kind"`
	Text     string      `json:"text,omitempty"`
	Data     []byte      `json:"data,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
	// Signature is an opaque provider artifact, for example encrypted
	// reasoning content required when replaying a stateless response API.
	// Core carries it across snapshots but never interprets it.
	Signature []byte            `json:"signature,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Message struct {
	ID         MessageID     `json:"id,omitempty"`
	Role       Role          `json:"role"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolResult *ToolResult   `json:"tool_result,omitempty"`
	Usage      Usage         `json:"usage,omitempty"`
	StopReason StopReason    `json:"stop_reason,omitempty"`
}

func UserMessage(text string) Message {
	return Message{Role: RoleUser, Parts: []ContentPart{{Kind: ContentText, Text: text}}}
}

func AssistantMessage(text string) Message {
	return Message{Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentText, Text: text}}}
}

func TextOf(m Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Kind == ContentText || p.Kind == ContentReasoning {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func (m Message) Clone() Message {
	out := m
	out.Parts = make([]ContentPart, len(m.Parts))
	for i, p := range m.Parts {
		out.Parts[i] = p
		out.Parts[i].Data = slices.Clone(p.Data)
		out.Parts[i].Signature = slices.Clone(p.Signature)
		out.Parts[i].Metadata = maps.Clone(p.Metadata)
	}
	out.ToolCalls = slices.Clone(m.ToolCalls)
	if m.ToolResult != nil {
		tr := m.ToolResult.Clone()
		out.ToolResult = &tr
	}
	return out
}

type ToolCall struct {
	ID        ToolCallID `json:"id"`
	ToolID    string     `json:"tool_id"`
	Arguments []byte     `json:"arguments"`
}

type ToolResultStatus string

const (
	ToolResultOK       ToolResultStatus = "ok"
	ToolResultBlocked  ToolResultStatus = "blocked"
	ToolResultFailed   ToolResultStatus = "failed"
	ToolResultCanceled ToolResultStatus = "cancelled"
)

type ToolResult struct {
	CallID    ToolCallID        `json:"call_id"`
	Status    ToolResultStatus  `json:"status"`
	Content   []ContentPart     `json:"content,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	SafeError string            `json:"safe_error,omitempty"`
	Executed  bool              `json:"executed"`
}

func (r ToolResult) Clone() ToolResult {
	out := r
	out.Content = make([]ContentPart, len(r.Content))
	for i, p := range r.Content {
		out.Content[i] = p
		out.Content[i].Data = slices.Clone(p.Data)
		out.Content[i].Metadata = maps.Clone(p.Metadata)
	}
	out.Metadata = maps.Clone(r.Metadata)
	return out
}

type Usage struct {
	InputTokens  uint64 `json:"input_tokens,omitempty"`
	OutputTokens uint64 `json:"output_tokens,omitempty"`
	TotalTokens  uint64 `json:"total_tokens,omitempty"`
}

type StopReason string

const (
	StopNone      StopReason = "none"
	StopEndTurn   StopReason = "end_turn"
	StopToolCalls StopReason = "tool_calls"
	StopMaxTokens StopReason = "max_tokens"
	StopCanceled  StopReason = "cancelled"
	StopError     StopReason = "error"
)

type RunStatus string

const (
	RunCompleted        RunStatus = "completed"
	RunCanceled         RunStatus = "cancelled"
	RunDeadlineExceeded RunStatus = "deadline_exceeded"
	RunFailed           RunStatus = "failed"
)

type RunResult struct {
	RunID        RunID         `json:"run_id"`
	Status       RunStatus     `json:"status"`
	FinalMessage *Message      `json:"final_message,omitempty"`
	Usage        Usage         `json:"usage,omitempty"`
	Error        *RuntimeError `json:"error,omitempty"`
}

func (r RunResult) Clone() RunResult {
	out := r
	if r.FinalMessage != nil {
		m := r.FinalMessage.Clone()
		out.FinalMessage = &m
	}
	if r.Error != nil {
		e := *r.Error
		out.Error = &e
	}
	return out
}

type CoreSnapshot struct {
	Version            uint32    `json:"version"`
	SystemInstructions string    `json:"system_instructions,omitempty"`
	Messages           []Message `json:"messages"`
	StateVersion       uint64    `json:"state_version"`
	CapturedAt         time.Time `json:"captured_at"`
}

func (s CoreSnapshot) Clone() CoreSnapshot {
	out := s
	out.Messages = make([]Message, len(s.Messages))
	for i, m := range s.Messages {
		out.Messages[i] = m.Clone()
	}
	return out
}

func (s CoreSnapshot) MarshalJSON() ([]byte, error) {
	type snapshot CoreSnapshot
	return json.Marshal(snapshot(s))
}

func nextID(prefix string) string {
	id := atomic.AddUint64(&globalID, 1)
	return fmt.Sprintf("%s-%d", prefix, id)
}

var globalID uint64
