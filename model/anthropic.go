package model

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

const (
	defaultAnthropicVersion   = "2023-06-01"
	defaultAnthropicMaxTokens = 8192
	// maxAnthropicCacheBreakpoints is the provider's limit on cache_control
	// markers per request.
	maxAnthropicCacheBreakpoints = 4
	// anthropicBindingBeta is the beta header block_binding needs; without
	// it, the provider refuses the field.
	anthropicBindingBeta = "thinking-binding-controls-2026-08-01"
	// anthropicLongTTL is the longest cache_control ttl. A cache write with
	// it costs twice the input price; the default, five minutes, 1.25 times.
	anthropicLongTTL = "1h"
)

// AnthropicMessages is the Anthropic Messages wire API. The zero value is the
// default. It has no body field for the cache key: it caches by cache_control.
type AnthropicMessages struct {
	// Version is the anthropic-version header. Empty is 2023-06-01. A version
	// a Connection's Headers already name is kept when Version is empty.
	Version string
	// Deferred is how the tools a Session loads one by one from its catalog
	// are shown: DeferredFormNative (the default) or DeferredFormList.
	Deferred DeferredForm
}

// Validate refuses a Deferred form that is neither native nor list. New calls
// it.
func (w AnthropicMessages) Validate() error {
	_, err := w.Deferred.native()
	return err
}

// Encode implements WireAPI. The model's facts steer three things: with
// RoundReasoning the reasoning of an earlier Round is left out of the history
// and a cache marker is added where the last Round started; with
// ThinkingBinding the request asks the provider to drop a thinking block whose
// prefix changed (block_binding drop_block, with its beta header) instead of
// refusing the request; and with a CacheRetention of an hour or more every
// cache_control marker asks for the one-hour ttl, which costs twice the input
// price for a cache write, where a shorter or unknown retention keeps the
// provider's default. It sends Sampling's Temperature and MaxTokens (8192 when
// unset) and refuses a request that sets a reasoning effort or summary. The
// CacheKey goes in the Connection's CacheKeyHeader only.
func (w AnthropicMessages) Encode(call Call) (*Request, error) {
	native, err := w.Deferred.native()
	if err != nil {
		return nil, err
	}
	body, names, err := encodeAnthropicRequest(call.Model, call.Request, native)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if version := withDefault(w.Version, call.Header.Get("anthropic-version")); version != "" {
		header.Set("anthropic-version", version)
	} else {
		header.Set("anthropic-version", defaultAnthropicVersion)
	}
	if call.Model.Facts.ThinkingBinding {
		header.Set("anthropic-beta", withAnthropicBeta(call.Header.Get("anthropic-beta"), anthropicBindingBeta))
	}
	if call.APIKey != "" {
		header.Set("x-api-key", call.APIKey)
	}
	return &Request{Body: body, Header: header, NewDecoder: func() Decoder { return newAnthropicDecoder(names) }}, nil
}

// anthropicBlock is one content block. A map keeps provider blocks, such as a
// replayed thinking block, intact and lets a cache_control marker attach to
// any block kind.
type anthropicBlock = map[string]any

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   uint32             `json:"max_tokens"`
	System      []anthropicBlock   `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	Stream      bool               `json:"stream"`
}

// anthropicThinking is sent only to carry block_binding, on the models whose
// default is "adaptive" (thinkingPrefixModels), so it changes nothing else.
type anthropicThinking struct {
	Type         string                `json:"type"`
	BlockBinding anthropicBlockBinding `json:"block_binding"`
}

type anthropicBlockBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl anthropicBlock  `json:"cache_control,omitempty"`
	// DeferLoading keeps the tool out of the Model's context until a
	// tool_reference block names it (the native form of deferred tools).
	DeferLoading bool `json:"defer_loading,omitempty"`
}

// blockRef locates the last content block a gotato Message produced.
type blockRef struct{ message, block int }

func encodeAnthropicRequest(model gotato.ModelConfig, request gotato.ModelRequest, native bool) ([]byte, map[string]string, error) {
	if err := refuseReasoningConfig("Anthropic Messages", request.Sampling); err != nil {
		return nil, nil, err
	}
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	payload := newAnthropicPayload(model, request)
	// Deferred tools, per
	// https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool :
	// every tool goes in the tools array on every request, and the ones with
	// defer_loading true reach the Model's context only when a tool_reference
	// block names them. Search is our own client-side tool, which answers with
	// tool_reference blocks ("Custom tool search implementation"). The deferred
	// tools come last, so the cache_control marker sits on a tool that is not
	// deferred, as the API requires.
	layout := newDeferredLayout(request, deferredMode(native))
	if payload.Tools, err = anthropicTools(orderedTools(layout, request), names); err != nil {
		return nil, nil, err
	}
	ends := make([]blockRef, len(request.Messages))
	if err := payload.encodeMessages(ends, request.Messages, names, layout, model.Facts.RoundReasoning); err != nil {
		return nil, nil, err
	}
	breakpoints := request.CacheBreakpoints
	if model.Facts.RoundReasoning {
		breakpoints = withRoundBreakpoint(breakpoints, request.Messages)
	}
	ttl := ""
	if model.Facts.CacheRetention >= time.Hour {
		ttl = anthropicLongTTL
	}
	applyAnthropicCacheBreakpoints(&payload, breakpoints, ends, ttl)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("model: encode request: %w", err)
	}
	return body, names, nil
}

// newAnthropicPayload returns a request with its settings and system
// instructions, before its tools and messages.
func newAnthropicPayload(model gotato.ModelConfig, request gotato.ModelRequest) anthropicRequest {
	payload := anthropicRequest{
		Model:       model.Name,
		MaxTokens:   defaultAnthropicMaxTokens,
		Temperature: request.Sampling.Temperature,
		Stream:      true,
		Messages:    make([]anthropicMessage, 0, len(request.Messages)),
	}
	if request.Sampling.MaxTokens != 0 {
		payload.MaxTokens = request.Sampling.MaxTokens
	}
	if model.Facts.ThinkingBinding {
		payload.Thinking = &anthropicThinking{Type: "adaptive", BlockBinding: anthropicBlockBinding{PrefixMismatchBehavior: "drop_block"}}
	}
	if request.SystemInstructions != "" {
		payload.System = []anthropicBlock{{"type": "text", "text": request.SystemInstructions}}
	}
	return payload
}

// anthropicTools encodes the tools in the order they are sent.
func anthropicTools(ordered []layoutTool, names map[string]string) ([]anthropicTool, error) {
	var tools []anthropicTool
	for _, entry := range ordered {
		spec := entry.spec
		name, err := assignFunctionName(names, spec.ID)
		if err != nil {
			return nil, err
		}
		schema, err := toolSchema(spec, `{"type":"object"}`)
		if err != nil {
			return nil, err
		}
		tools = append(tools, anthropicTool{Name: name, Description: spec.Description, InputSchema: schema, DeferLoading: entry.deferred})
	}
	return tools, nil
}

func anthropicBlocks(message gotato.Message, names map[string]string, replay reasoningReplay) (string, []anthropicBlock, error) {
	switch message.Role {
	case gotato.RoleUser, gotato.RoleNotice:
		// A notice (F4.4) is tagged text in the adjacent user turn.
		return "user", anthropicUserBlocks(message), nil
	case gotato.RoleToolResult:
		return "user", []anthropicBlock{anthropicToolResultBlock(message)}, nil
	case gotato.RoleAssistant:
		blocks, err := anthropicAssistantBlocks(message, names, replay)
		if err != nil {
			return "", nil, err
		}
		return "assistant", blocks, nil
	default:
		return "", nil, fmt.Errorf("model: unsupported Message role %q", message.Role)
	}
}

// anthropicAssistantBlocks encodes the parts and Tool Calls of an assistant
// Message, leaving its reasoning out when replay drops it.
func anthropicAssistantBlocks(message gotato.Message, names map[string]string, replay reasoningReplay) ([]anthropicBlock, error) {
	var blocks []anthropicBlock
	for _, part := range message.Parts {
		if part.Kind == gotato.ContentKindReasoning && replay == reasoningDropped {
			continue
		}
		var err error
		if blocks, err = appendAnthropicAssistantPart(blocks, part); err != nil {
			return nil, err
		}
	}
	return appendAnthropicToolUses(blocks, message.ToolCalls, names)
}

// anthropicReasoningBlock decodes a signed thinking or redacted_thinking block for replay.
func anthropicReasoningBlock(signature []byte) (anthropicBlock, error) {
	var block anthropicBlock
	if err := json.Unmarshal(signature, &block); err != nil {
		return nil, fmt.Errorf("model: assistant reasoning artifact is invalid: %w", err)
	}
	if kind, _ := block["type"].(string); kind != "thinking" && kind != "redacted_thinking" {
		return nil, fmt.Errorf("model: assistant reasoning artifact has type %q, want thinking or redacted_thinking", kind)
	}
	return block, nil
}

// withRoundBreakpoint adds a cache marker on the last user Message before the
// final Message, in prefix order. When a new Round leaves the last Round's
// reasoning out, the request differs from the last one at its first thinking
// block, so the entry cached at the end of the last request no longer
// matches, and the provider looks back only about 20 blocks from a marker for
// an earlier one. This marker sits where the last Round started, whose prefix
// every request since has shared: the first request of a Round reads the
// cache up to the Round before it, and a request in a tool loop up to its own
// Round's start.
func withRoundBreakpoint(breakpoints []gotato.CacheBreakpoint, messages []gotato.Message) []gotato.CacheBreakpoint {
	if len(messages) < 2 {
		return breakpoints
	}
	start := roundStart(messages[:len(messages)-1])
	if start < 0 {
		return breakpoints
	}
	marker := gotato.CacheBreakpoint{After: gotato.CacheAnchorAfterMessage, Index: start}
	out := make([]gotato.CacheBreakpoint, 0, len(breakpoints)+1)
	placed := false
	for _, point := range breakpoints {
		if !placed && point.After == gotato.CacheAnchorAfterMessage && point.Index >= start {
			if point.Index > start {
				out = append(out, marker)
			}
			placed = true
		}
		out = append(out, point)
	}
	if !placed {
		out = append(out, marker)
	}
	return out
}

// applyAnthropicCacheBreakpoints places cache_control markers, each with ttl
// when it is set. When a request carries more breakpoints than the provider
// accepts, the last ones win: they cover the longest prefixes.
func applyAnthropicCacheBreakpoints(payload *anthropicRequest, breakpoints []gotato.CacheBreakpoint, ends []blockRef, ttl string) {
	var targets []anthropicCacheTarget
	seen := make(map[anthropicCacheTarget]bool)
	for _, point := range breakpoints {
		t, ok := anthropicCacheTargetOf(payload, point, ends)
		if !ok {
			continue
		}
		if !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	if len(targets) > maxAnthropicCacheBreakpoints {
		targets = targets[len(targets)-maxAnthropicCacheBreakpoints:]
	}
	for _, t := range targets {
		placeAnthropicCacheMarker(payload, t, ttl)
	}
}

// anthropicCacheTarget is where one cache_control marker goes.
type anthropicCacheTarget struct {
	anchor gotato.CacheAnchor
	ref    blockRef
}

// anthropicCacheTargetOf returns the target of one breakpoint; ok is false
// when it falls on nothing the request sends.
func anthropicCacheTargetOf(payload *anthropicRequest, point gotato.CacheBreakpoint, ends []blockRef) (t anthropicCacheTarget, ok bool) {
	t = anthropicCacheTarget{anchor: point.After, ref: blockRef{message: -1}}
	switch point.After {
	case gotato.CacheAnchorAfterSystem:
		return t, len(payload.System) > 0
	case gotato.CacheAnchorAfterTools:
		return t, len(payload.Tools) > 0
	case gotato.CacheAnchorAfterMessage:
		if point.Index < 0 || point.Index >= len(ends) || ends[point.Index].message < 0 {
			return t, false
		}
		t.ref = ends[point.Index]
		return t, true
	default:
		return t, false
	}
}

// placeAnthropicCacheMarker puts a cache_control marker, with ttl when it is
// set, on the block a target names.
func placeAnthropicCacheMarker(payload *anthropicRequest, t anthropicCacheTarget, ttl string) {
	marker := anthropicCacheMarker(ttl)
	switch t.anchor {
	case gotato.CacheAnchorAfterSystem:
		payload.System[len(payload.System)-1]["cache_control"] = marker
	case gotato.CacheAnchorAfterTools:
		end := len(payload.Tools) - 1
		for end > 0 && payload.Tools[end].DeferLoading {
			end--
		}
		payload.Tools[end].CacheControl = marker
	case gotato.CacheAnchorAfterMessage:
		payload.Messages[t.ref.message].Content[t.ref.block]["cache_control"] = marker
	}
}

// anthropicCacheMarker returns a new cache_control marker, with ttl when it is set.
func anthropicCacheMarker(ttl string) anthropicBlock {
	if ttl != "" {
		return anthropicBlock{"type": "ephemeral", "ttl": ttl}
	}
	return anthropicBlock{"type": "ephemeral"}
}
