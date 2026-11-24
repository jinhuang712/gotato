package fakemodel

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// The shape of an Anthropic Messages request, and the refusals in
// Anthropic's error form.

// betaBinding is the beta header block_binding needs.
const betaBinding = "thinking-binding-controls-2026-08-01"

type anthropicWire struct{}

type anthropicRequest struct {
	Model     string          `json:"model"`
	MaxTokens *int            `json:"max_tokens"`
	System    json.RawMessage `json:"system"`
	Messages  []anthropicMsg  `json:"messages"`
	Tools     []anthropicTool `json:"tools"`
	Stream    bool            `json:"stream"`
	Thinking  *anthropicThink `json:"thinking"`

	blocks [][]map[string]any
}

type anthropicThink struct {
	Type         string `json:"type"`
	BlockBinding *struct {
		PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
	} `json:"block_binding"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name         string         `json:"name"`
	InputSchema  map[string]any `json:"input_schema"`
	DeferLoading bool           `json:"defer_loading"`
	CacheControl map[string]any `json:"cache_control"`
}

func (anthropicWire) api() string { return APIAnthropic }

func anthropicErr(status int, kind, message string) *apiError {
	return &apiError{status: status, body: map[string]any{
		"type":       "error",
		"error":      map[string]any{"type": kind, "message": message},
		"request_id": "req_fake",
	}}
}

func (anthropicWire) invalid(message string) *apiError {
	return anthropicErr(http.StatusBadRequest, "invalid_request_error", message)
}

func (anthropicWire) authorize(h http.Header) *apiError {
	if h.Get("x-api-key") == "" {
		return anthropicErr(http.StatusUnauthorized, "authentication_error", "x-api-key header is required")
	}
	if h.Get("anthropic-version") == "" {
		return anthropicErr(http.StatusBadRequest, "invalid_request_error", "anthropic-version: header is required")
	}
	return nil
}

func (anthropicWire) tooManyOutputTokens(p *parsed, limit int) *apiError {
	return anthropicErr(http.StatusBadRequest, "invalid_request_error",
		fmt.Sprintf("max_tokens: %d > %d, which is the maximum allowed number of output tokens for %s", p.maxOutput, limit, p.model))
}

func (anthropicWire) tooLong(tokens, window int) *apiError {
	return anthropicErr(http.StatusBadRequest, "invalid_request_error",
		fmt.Sprintf("prompt is too long: %d tokens > %d maximum", tokens, window))
}

// contentBlocks turns a message's content, a string or a list of blocks, into
// blocks.
func contentBlocks(raw json.RawMessage) ([]map[string]any, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []map[string]any{{"type": "text", "text": s}}, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("content must be a string or a list of content blocks")
	}
	return blocks, nil
}
