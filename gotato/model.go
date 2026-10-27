package gotato

import (
	"context"
)

// Model is the interface Gotato calls for a reply, as a stream of events.
// Stream starts a call and returns its ModelStream, or an error when the call
// could not start. The ctx ends the call.
type Model interface {
	Stream(context.Context, ModelRequest) (ModelStream, error)
}

// ModelRequest is what the Model receives for one call: the Context a
// ContextBuilder built, with the Tools to offer.
type ModelRequest struct {
	SystemInstructions string     `json:"system_instructions,omitempty"`
	Messages           []Message  `json:"messages"`
	Tools              []ToolSpec `json:"tools,omitempty"`
	// LoadedTools are the IDs of the catalog Tools in Tools that the Model
	// loaded, and UnloadedTools are the specs of the catalog Tools it can
	// still load, both in ID order. A Session with a catalog sets them on
	// every request. An adapter that cannot defer tools natively ignores
	// both and sends Tools, which grows when a Tool loads. One that can
	// sends every catalog Tool as deferred in a fixed order, so the tool
	// list stays the same while Tools load, and shows a load as a
	// reference in the Tool Result that loaded it (ToolResult.LoadedTools).
	LoadedTools   []string    `json:"loaded_tools,omitempty"`
	UnloadedTools []ToolSpec  `json:"unloaded_tools,omitempty"`
	Config        ModelConfig `json:"config,omitzero"`
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
	// CacheAnchorAfterSystem marks the end of the system prompt.
	CacheAnchorAfterSystem CacheAnchor = "system"
	// CacheAnchorAfterTools marks the end of the tool definitions.
	CacheAnchorAfterTools CacheAnchor = "tools"
	// CacheAnchorAfterMessage marks the end of Messages[Index].
	CacheAnchorAfterMessage CacheAnchor = "message"
)

// CacheBreakpoint is one prompt-cache hint.
type CacheBreakpoint struct {
	After CacheAnchor `json:"after"`
	Index int         `json:"index,omitempty"`
}

// ModelConfig carries per-request sampling and reasoning hints. A Session
// sends the options from WithModelConfig on every model call, and gateway
// adapters map the fields their provider supports.
type ModelConfig struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        uint32   `json:"max_tokens,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ReasoningSummary string   `json:"reasoning_summary,omitempty"`
}

// ModelStream is the events of one model call. One goroutine reads it: Recv
// returns the next event, and io.EOF after the last. The caller always calls
// Close, which releases the call.
type ModelStream interface {
	Recv(context.Context) (ModelEvent, error)
	Close() error
}

// ModelEventKind says what a ModelEvent carries.
type ModelEventKind string

// The kinds of ModelEvent. A reply is a stream of deltas and Tool calls, then
// Usage, then ModelEventKindDone.
const (
	ModelEventKindTextDelta      ModelEventKind = "text_delta"
	ModelEventKindReasoningDelta ModelEventKind = "reasoning_delta"
	// ModelEventKindReasoningDone carries an opaque provider reasoning artifact.
	// Core stores it with the reasoning part but never interprets it.
	ModelEventKindReasoningDone ModelEventKind = "reasoning_done"
	ModelEventKindToolCall      ModelEventKind = "tool_call"
	ModelEventKindUsage         ModelEventKind = "usage"
	ModelEventKindDone          ModelEventKind = "done"
)

// ModelEvent is one piece of a reply: a delta of text or reasoning, a Tool
// call, the Usage, or the end of the reply (ModelEventKindDone, with the
// StopReason). Only the fields of its Kind are set.
type ModelEvent struct {
	Kind              ModelEventKind `json:"kind"`
	Text              string         `json:"text,omitempty"`
	ReasoningArtifact []byte         `json:"reasoning_artifact,omitempty"`
	ToolCall          *ToolCall      `json:"tool_call,omitempty"`
	Usage             Usage          `json:"usage,omitzero"`
	StopReason        StopReason     `json:"stop_reason,omitempty"`
	// Model is the model the provider reports answering, when it reports
	// one. Behind a gateway that falls back to another model, it can differ
	// from the model that was requested. Any event may carry it; the last
	// one a reply's stream carries wins.
	Model string `json:"model,omitempty"`
}
