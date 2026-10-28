package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

const (
	defaultResponsesBaseURL = "https://api.openai.com/v1"
	// defaultResponsesCacheKeyField is the Responses API's own field for a
	// prompt cache key.
	defaultResponsesCacheKeyField = "prompt_cache_key"
)

func (c *Client) streamResponses(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	body, names, err := encodeResponsesRequest(c.model, request, c.rules)
	if err == nil {
		body, err = c.withCacheKeyField(body, request.CacheKey)
	}
	if err != nil {
		return nil, err
	}
	open := c.withCacheKeyHeader(c.do, request.CacheKey)
	response, err := open(ctx, body)
	if err != nil {
		return nil, err
	}
	return &responsesStream{
		sseBody:  newSSEBody(response),
		nameMap:  names,
		calls:    make(map[int]*responsesCall),
		texts:    make(map[int]bool),
		restart:  restarter{client: c, body: body, open: open},
		overflow: c.overflow,
	}, nil
}

type responsesRequest struct {
	Model             string              `json:"model"`
	Store             bool                `json:"store"`
	Stream            bool                `json:"stream"`
	Instructions      string              `json:"instructions"`
	Input             []any               `json:"input"`
	Text              responsesText       `json:"text"`
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

type responsesText struct {
	Verbosity string `json:"verbosity"`
}

type responsesReasoning struct {
	Effort  string `json:"effort"`
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

func encodeResponsesRequest(model string, request gotato.ModelRequest, rules providerRules) ([]byte, map[string]string, error) {
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	layout := newDeferredLayout(request, rules)
	input := make([]any, 0, len(request.Messages)*2)
	start := roundStart(request.Messages)
	for i, message := range request.Messages {
		items, err := convertResponsesMessage(message, names, rules.keepsReasoning(i, start), layout)
		if err != nil {
			return nil, nil, err
		}
		input = append(input, items...)
	}

	// Deferred tools, per https://developers.openai.com/api/docs/guides/tools-tool-search
	// (client-executed tool search): a tool_search entry with execution
	// "client" stands for our search function, the other tools are functions,
	// and the catalog's go in with defer_loading true. The Model's search shows
	// as a tool_search_call item, and our answer as a tool_search_output item
	// that lists the functions it loads; a loaded function is injected at the
	// end of the context, so the tool list stays the same.
	ordered := orderedTools(layout, request)
	tools := make([]any, 0, len(ordered))
	for _, entry := range ordered {
		spec := entry.spec
		name, err := assignFunctionName(names, spec.ID)
		if err != nil {
			return nil, nil, err
		}
		parameters, err := toolSchema(spec, `{"type":"object"}`)
		if err != nil {
			return nil, nil, err
		}
		if layout != nil && isSearchToolID(spec.ID) {
			tools = append(tools, responsesToolSearch{Type: "tool_search", Execution: "client", Description: spec.Description, Parameters: parameters})
			continue
		}
		tools = append(tools, responsesTool{Type: "function", Name: name, Description: spec.Description, Parameters: parameters, DeferLoading: entry.deferred})
	}

	instructions := request.SystemInstructions
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	payload := responsesRequest{
		Model:             model,
		Store:             false,
		Stream:            true,
		Instructions:      instructions,
		Input:             input,
		Text:              responsesText{Verbosity: "low"},
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Tools:             tools,
	}
	if request.Config.Temperature != nil {
		payload.Temperature = request.Config.Temperature
	}
	if request.Config.MaxTokens != 0 {
		payload.MaxOutputTokens = request.Config.MaxTokens
	}
	if rules.responsesCacheTTL != "" {
		payload.PromptCacheOptions = &responsesCacheOptions{TTL: rules.responsesCacheTTL}
	}
	if request.Config.ReasoningEffort != "" {
		summary := request.Config.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		payload.Reasoning = &responsesReasoning{Effort: request.Config.ReasoningEffort, Summary: summary}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode Responses request: %w", err)
	}
	return body, names, nil
}

// convertResponsesMessage turns one Message into the Responses input items it
// stands for: none, one or several.
func convertResponsesMessage(message gotato.Message, names map[string]string, keepReasoning bool, native ...*deferredLayout) ([]any, error) {
	var layout *deferredLayout
	if len(native) > 0 {
		layout = native[0]
	}
	switch message.Role {
	case gotato.RoleUser, gotato.RoleNotice:
		// A notice (G-F29) is tagged text in a user item.
		content, err := responsesInputContent(message.Parts)
		if err != nil {
			return nil, err
		}
		if len(content) == 0 {
			return nil, nil
		}
		return []any{responsesMessage{Role: "user", Content: content}}, nil
	case gotato.RoleAssistant:
		return responsesAssistantItems(message, names, keepReasoning, layout)
	case gotato.RoleToolResult:
		return responsesToolResultItems(message, names, layout)
	default:
		return nil, fmt.Errorf("gateway: unsupported Message role %q", message.Role)
	}
}

// responsesAssistantItems is an assistant Message as output messages,
// reasoning items and function calls, in that order.
func responsesAssistantItems(message gotato.Message, names map[string]string, keepReasoning bool, layout *deferredLayout) ([]any, error) {
	items := make([]any, 0, len(message.Parts)+len(message.ToolCalls))
	// Adjacent text Parts are one output message: transcripts saved
	// before Gotato coalesced deltas hold a reply as its fragments.
	var text strings.Builder
	flushText := func() {
		if text.Len() == 0 {
			return
		}
		items = append(items, responsesOutputMessage{
			Type: "message", Role: "assistant", Status: "completed",
			Content: []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
		})
		text.Reset()
	}
	for _, part := range message.Parts {
		switch part.Kind {
		case gotato.ContentKindReasoning:
			if len(part.Signature) == 0 || !keepReasoning {
				continue
			}
			var reasoning json.RawMessage
			if err := json.Unmarshal(part.Signature, &reasoning); err != nil {
				return nil, fmt.Errorf("gateway: assistant reasoning signature is invalid: %w", err)
			}
			flushText()
			items = append(items, reasoning)
		case gotato.ContentKindText:
			text.WriteString(part.Text)
		case gotato.ContentKindImage, gotato.ContentKindJSON:
			if len(part.Data) > 0 || part.Text != "" {
				return nil, fmt.Errorf("gateway: Responses assistant content kind %q is unsupported", part.Kind)
			}
		}
	}
	flushText()
	for _, call := range message.ToolCalls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return nil, err
		}
		arguments := string(replayArguments(call.Arguments))
		callID, itemID := splitResponsesCallID(string(call.ID))
		if layout != nil && layout.searchCalls[string(call.ID)] {
			items = append(items, responsesToolSearchCall{Type: "tool_search_call", Execution: "client", CallID: callID, Status: "completed", Arguments: json.RawMessage(arguments)})
			continue
		}
		// A JSON object is the chat API's extra_content, not an item ID.
		if len(call.Signature) > 0 && !isJSONObject(call.Signature) {
			itemID = string(call.Signature)
		}
		// A call whose reasoning item is left out goes without its item
		// ID too, as a call from another wire API does, so it never
		// names an output its reasoning came with.
		if !keepReasoning {
			itemID = ""
		}
		items = append(items, responsesFunctionCall{Type: "function_call", ID: itemID, CallID: callID, Name: name, Arguments: arguments})
	}
	return items, nil
}

// responsesToolResultItems is a tool result as a function_call_output, or, for
// the result of a client-side tool search, as a tool_search_output that lists
// the Tools it loaded.
func responsesToolResultItems(message gotato.Message, names map[string]string, layout *deferredLayout) ([]any, error) {
	callID := ""
	if message.ToolResult != nil {
		callID, _ = splitResponsesCallID(string(message.ToolResult.CallID))
	}
	if callID == "" {
		return nil, fmt.Errorf("gateway: Tool result has no Call ID")
	}
	if layout != nil && layout.searchCalls[string(message.ToolResult.CallID)] {
		loaded, err := responsesLoadedTools(message.ToolResult, names, layout)
		if err != nil {
			return nil, err
		}
		return []any{responsesToolSearchOutput{Type: "tool_search_output", Execution: "client", CallID: callID, Status: "completed", Tools: loaded}}, nil
	}
	return []any{responsesFunctionOutput{Type: "function_call_output", CallID: callID, Output: toolResultOutput(message)}}, nil
}

// responsesLoadedTools lists, as deferred function Tools, the Tools a tool
// search result references.
func responsesLoadedTools(result *gotato.ToolResult, names map[string]string, layout *deferredLayout) ([]responsesTool, error) {
	loaded := []responsesTool{}
	for _, id := range layout.references(result) {
		for _, entry := range layout.tools {
			if entry.spec.ID != id {
				continue
			}
			name, err := assignFunctionName(names, id)
			if err != nil {
				return nil, err
			}
			parameters, err := toolSchema(entry.spec, `{"type":"object"}`)
			if err != nil {
				return nil, err
			}
			loaded = append(loaded, responsesTool{Type: "function", Name: name, Description: entry.spec.Description, Parameters: parameters, DeferLoading: true})
		}
	}
	return loaded, nil
}

func responsesInputContent(parts []gotato.ContentPart) ([]any, error) {
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case gotato.ContentKindText:
			if part.Text != "" {
				content = append(content, map[string]any{"type": "input_text", "text": part.Text})
			}
		case gotato.ContentKindImage:
			if len(part.Data) == 0 {
				continue
			}
			mimeType := part.MIMEType
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			content = append(content, map[string]any{
				"type":      "input_image",
				"detail":    "auto",
				"image_url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data),
			})
		case gotato.ContentKindReasoning, gotato.ContentKindJSON:
			if part.Text != "" || len(part.Data) > 0 {
				return nil, fmt.Errorf("gateway: Responses user content kind %q is unsupported", part.Kind)
			}
		}
	}
	return content, nil
}

// splitResponsesCallID reads a call ID as Sessions saved before
// ToolCall.Signature stored it, with the provider's item ID packed after a
// "|". A call ID that has no "|" is returned as it is.
func splitResponsesCallID(id string) (callID, itemID string) {
	if before, after, ok := strings.Cut(id, "|"); ok {
		return before, after
	}
	return id, ""
}
