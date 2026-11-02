package gotato

import (
	"context"
	"time"
)

// Model is the interface Gotato calls for a reply, as a stream of events.
// Stream starts a call and returns its ModelStream, or an error when the call
// could not start. The ctx ends the call. Config says what the model is: its
// name, the facts the runtime reads (the context window, the prompt cache
// retention) and its default sampling. A wrapper that embeds a Model
// forwards Config with it, so a fact is never hidden by a wrapper that forgot
// to pass it on.
//
// A Model written by hand, a fake in a test for instance, returns the zero
// ModelConfig when it has nothing to state. That is a valid answer: no
// window, which reads as unknown, so compaction has no ceiling trigger and
// runs only when the provider refuses a request as too long; no retention;
// no default sampling.
type Model interface {
	Stream(context.Context, ModelRequest) (ModelStream, error)
	Config() ModelConfig
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
	LoadedTools   []string   `json:"loaded_tools,omitempty"`
	UnloadedTools []ToolSpec `json:"unloaded_tools,omitempty"`
	// Sampling is the sampling and reasoning hints of this call: the Model's
	// own defaults (ModelConfig.Sampling) overridden field by field by the
	// Session's WithSampling. A wire API maps the fields its provider takes.
	Sampling Sampling `json:"sampling,omitzero"`
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

// ModelConfig is one model, described once: what it is called, what it is,
// and how it is sampled by default.
type ModelConfig struct {
	// Name is the model's id as its provider knows it.
	Name string `json:"name,omitempty"`
	// Facts is what the model is. Nothing overrides it, and Gotato guesses
	// none of it from a name.
	Facts ModelFacts `json:"facts,omitzero"`
	// Sampling holds the defaults of every call. The Session's WithSampling
	// overrides them field by field into ModelRequest.Sampling.
	Sampling Sampling `json:"sampling,omitzero"`
}

// ModelFacts is what a model is, as the team that runs it states it. The zero
// value of every field is the cautious answer: unknown, or off.
type ModelFacts struct {
	// Window is the context window in tokens, or "unknown", written out. The
	// runtime reads it to size the ceiling that triggers compaction; an
	// unknown or unstated window leaves no ceiling.
	Window Window `json:"window,omitzero"`
	// CacheRetention is how long the provider keeps the prompt cache after a
	// request. Zero means unknown, never "no cache". The Runner reads it to
	// time a warm Session's idle compaction, and a wire API may ask the
	// provider for that retention.
	CacheRetention time.Duration `json:"cache_retention,omitempty"`
	// RoundReasoning is set when the provider takes back only the reasoning
	// of the current Round (the replies after the last user Message). Gotato
	// then sends no earlier reasoning, and a compaction leaves it out of the
	// Messages it keeps. Left unset, every reasoning artifact goes back,
	// which a provider accepts.
	RoundReasoning bool `json:"round_reasoning,omitempty"`
	// ThinkingBinding is set when the provider checks the prefix a thinking
	// block was written under, so a wire API that can may ask it to drop a
	// block whose prefix changed instead of refusing the request.
	ThinkingBinding bool `json:"thinking_binding,omitempty"`
}

// Sampling carries sampling and reasoning hints. A model's defaults are in
// ModelConfig.Sampling, a Session overrides them with WithSampling, and the
// result is ModelRequest.Sampling. A wire API maps the fields its provider
// supports.
type Sampling struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxTokens        uint32   `json:"max_tokens,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ReasoningSummary string   `json:"reasoning_summary,omitempty"`
}

// over returns s with every field it leaves unset taken from defaults: the
// Sampling a Session sends is its own WithSampling over the Model's.
func (s Sampling) over(defaults Sampling) Sampling {
	if s.Temperature == nil {
		s.Temperature = defaults.Temperature
	}
	if s.MaxTokens == 0 {
		s.MaxTokens = defaults.MaxTokens
	}
	if s.ReasoningEffort == "" {
		s.ReasoningEffort = defaults.ReasoningEffort
	}
	if s.ReasoningSummary == "" {
		s.ReasoningSummary = defaults.ReasoningSummary
	}
	return s
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
