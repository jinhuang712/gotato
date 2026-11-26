package model

import (
	"bytes"
	"encoding/json"
)

type responsesRequest struct {
	Model             string              `json:"model"`
	Store             bool                `json:"store"`
	Stream            bool                `json:"stream"`
	Instructions      string              `json:"instructions,omitempty"`
	Input             []any               `json:"input"`
	Include           []string            `json:"include"`
	ToolChoice        string              `json:"tool_choice"`
	ParallelToolCalls bool                `json:"parallel_tool_calls"`
	Tools             []any               `json:"tools,omitempty"`
	Reasoning         *responsesReasoning `json:"reasoning,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	MaxOutputTokens   uint32              `json:"max_output_tokens,omitempty"`
	// PromptCacheOptions asks GPT-5.6 and later to keep the prompt cache.
	PromptCacheOptions *responsesCacheOptions `json:"prompt_cache_options,omitempty"`
}

type responsesCacheOptions struct {
	TTL string `json:"ttl"`
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
	// DeferLoading keeps the function out of the Model's context until a
	// tool_search_output returns it (the native form of deferred tools).
	DeferLoading bool `json:"defer_loading,omitempty"`
}

// responsesToolSearch is the client-executed tool search the native form
// replaces the search function with.
type responsesToolSearch struct {
	Type        string          `json:"type"`
	Execution   string          `json:"execution"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// responsesToolSearchCall and responsesToolSearchOutput replay a call to the
// search tool and its result in the native form.
type responsesToolSearchCall struct {
	Type      string          `json:"type"`
	Execution string          `json:"execution"`
	CallID    string          `json:"call_id"`
	Status    string          `json:"status"`
	Arguments json.RawMessage `json:"arguments"`
}

type responsesToolSearchOutput struct {
	Type      string          `json:"type"`
	Execution string          `json:"execution"`
	CallID    string          `json:"call_id"`
	Status    string          `json:"status"`
	Tools     []responsesTool `json:"tools"`
}

// responsesArguments is the arguments of an output item: a JSON string for a
// function_call, a JSON object for a tool_search_call. Either way it holds the
// arguments as JSON text.
type responsesArguments string

func (a *responsesArguments) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		*a = responsesArguments(text)
		return nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	*a = responsesArguments(trimmed)
	return nil
}

type responsesMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content,omitempty"`
}

type responsesOutputMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []any  `json:"content"`
	Status  string `json:"status"`
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}
