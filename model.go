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
	// LoadedTools are the IDs of the catalog Tools in Tools that the Model
	// loaded, and UnloadedTools are the specs of the catalog Tools it can
	// still load, both in ID order. A Session with a catalog sets them on
	// every request. An adapter that cannot defer tools natively ignores
	// both and sends Tools, which grows when a Tool loads. One that can
	// sends every catalog Tool as deferred in a fixed order, so the tool
	// list stays the same while Tools load, and shows a load as a
	// reference in the Tool Result that loaded it (ToolResult.Loaded).
	LoadedTools   []string     `json:"loaded_tools,omitempty"`
	UnloadedTools []ToolSpec   `json:"unloaded_tools,omitempty"`
	Options            ModelOptions `json:"options,omitempty"`
	// CacheBreakpoints are provider-neutral prompt-cache hints in prefix
	// order. An adapter maps them to its provider's mechanism (for example
	// Anthropic cache_control) or ignores them when the provider caches
	// prefixes automatically. They never change the prompt content.
	CacheBreakpoints []CacheBreakpoint `json:"cache_breakpoints,omitempty"`
	// CacheKey groups the requests that share a cacheable prefix, usually one
	// value per Session, so a provider can serve them from the same prompt
	// cache. An adapter sends it the way its configuration says (a body field
	// such as prompt_cache_key, or a header) or ignores it. Like the
	// breakpoints, it never changes the prompt content or the prefix hash.
	CacheKey string `json:"cache_key,omitempty"`
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

// ModelOptions carries per-request sampling and reasoning hints. A Session
// sends the options from WithModelOptions on every model call, and gateway
// adapters map the fields their provider supports.
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
	// Model is the model the provider reports answering, when it reports
	// one. Behind a gateway that falls back to another model, it can differ
	// from the model that was requested. Any event may carry it; the last
	// one a reply's stream carries wins.
	Model string `json:"model,omitempty"`
}
