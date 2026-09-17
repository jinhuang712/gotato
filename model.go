package gotato

import (
	"context"
)

type Model interface {
	Stream(context.Context, ModelRequest) (ModelStream, error)
}

type ModelRequest struct {
	SystemInstructions string       `json:"system_instructions,omitempty"`
	Messages           []Message    `json:"messages"`
	Tools              []ToolSpec   `json:"tools,omitempty"`
	Options            ModelOptions `json:"options,omitempty"`
	// CacheBreakpoints are provider-neutral prompt-cache hints in prefix
	// order. An adapter maps them to its provider's mechanism (for example
	// Anthropic cache_control) or ignores them when the provider caches
	// prefixes automatically. They never change the prompt content.
	CacheBreakpoints []CacheBreakpoint `json:"cache_breakpoints,omitempty"`
}

// CacheAnchor names where a CacheBreakpoint sits.
type CacheAnchor string

const (
	// CacheAfterSystem marks the end of the system prompt.
	CacheAfterSystem CacheAnchor = "system"
	// CacheAfterTools marks the end of the tool definitions.
	CacheAfterTools CacheAnchor = "tools"
	// CacheAfterMessage marks the end of Messages[Index].
	CacheAfterMessage CacheAnchor = "message"
)

// CacheBreakpoint is one prompt-cache hint.
type CacheBreakpoint struct {
	After CacheAnchor `json:"after"`
	Index int         `json:"index,omitempty"`
}

type ModelOptions struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        uint32   `json:"max_tokens,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ReasoningSummary string   `json:"reasoning_summary,omitempty"`
}

type ModelStream interface {
	Recv(context.Context) (ModelEvent, error)
	Close() error
}

type ModelEventKind string

const (
	ModelTextDelta      ModelEventKind = "text_delta"
	ModelReasoningDelta ModelEventKind = "reasoning_delta"
	// ModelReasoningDone carries an opaque provider reasoning artifact. Core
	// stores it with the reasoning part but never interprets it.
	ModelReasoningDone ModelEventKind = "reasoning_done"
	ModelToolCall      ModelEventKind = "tool_call"
	ModelUsage         ModelEventKind = "usage"
	ModelDone          ModelEventKind = "done"
)

type ModelEvent struct {
	Kind              ModelEventKind `json:"kind"`
	Text              string         `json:"text,omitempty"`
	ReasoningArtifact []byte         `json:"reasoning_artifact,omitempty"`
	ToolCall          *ToolCall      `json:"tool_call,omitempty"`
	Usage             Usage          `json:"usage,omitempty"`
	StopReason        StopReason     `json:"stop_reason,omitempty"`
}
