package gotato

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
)

type AgentID string
type RunID string
type TurnNumber uint32
type MessageID string
type ToolCallID string

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
	// Core carries it across runtime operations but never interprets it.
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
	RunRunning          RunStatus = "running"
	RunCompleted        RunStatus = "completed"
	RunCanceled         RunStatus = "cancelled"
	RunDeadlineExceeded RunStatus = "deadline_exceeded"
	RunFailed           RunStatus = "failed"
)

type RunMetrics struct {
	ElapsedMS      int64  `json:"elapsed_ms"`
	Turns          uint32 `json:"turns"`
	ToolCalls      uint32 `json:"tool_calls"`
	TextBytes      uint64 `json:"text_bytes"`
	ReasoningBytes uint64 `json:"reasoning_bytes"`
}

type RunResult struct {
	RunID        RunID         `json:"run_id"`
	Status       RunStatus     `json:"status"`
	FinalMessage *Message      `json:"final_message,omitempty"`
	Usage        Usage         `json:"usage,omitempty"`
	Metrics      RunMetrics    `json:"metrics"`
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

// nextID returns "<prefix>-<8 hex process nonce><counter hex>". The nonce is
// drawn once per process from crypto/rand, so IDs stay unique across
// restarts and across processes that share one Session store, while the
// counter keeps them ordered and cheap within a process.
func nextID(prefix string) string {
	id := atomic.AddUint64(&globalID, 1)
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

var globalID uint64

var processNonce = func() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("gotato: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}()
