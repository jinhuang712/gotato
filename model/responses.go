package model

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// defaultResponsesCacheKeyField is the Responses API's own field for a prompt
// cache key.
const defaultResponsesCacheKeyField = "prompt_cache_key"

// responsesCacheTTL is the one prompt_cache_options.ttl the Responses API
// takes, from GPT-5.6 on. It is also their default, so asking for it only makes
// the retention explicit.
const responsesCacheTTL = 30 * time.Minute

// Responses is the OpenAI Responses wire API. The zero value is the default.
type Responses struct {
	// CacheKeyField names the top-level body field that carries the request's
	// cache key. Empty is "prompt_cache_key", the API's own; "none" sends no
	// field. It must not name a field the request already sends.
	CacheKeyField string
	// Deferred is how the tools a Session loads one by one from its catalog
	// are shown: DeferredFormNative (the default) or DeferredFormList. Native
	// keeps the tool list of the request the same while tools load, so the
	// prompt cache survives a load.
	Deferred DeferredForm
}

// Validate refuses a CacheKeyField that names a field the request already
// sends, which the cache key would silently replace, and a Deferred form that
// is neither native nor list. New calls it.
func (w Responses) Validate() error {
	if _, err := w.Deferred.native(); err != nil {
		return err
	}
	return checkCacheKeyField(w.cacheKeyField(), reflect.TypeFor[responsesRequest](), responsesReservedFields)
}

// responsesReservedFields are the top-level fields the request carries besides
// the ones its encoder struct declares.
var responsesReservedFields = []string{"max_tool_calls", "metadata", "text", "truncation", "user"}

func (w Responses) cacheKeyField() string {
	if w.CacheKeyField == "" {
		return defaultResponsesCacheKeyField
	}
	return w.CacheKeyField
}

// Encode implements WireAPI. The model's facts steer two things: with
// RoundReasoning the reasoning of an earlier Round is left out of the history,
// and with a CacheRetention of 30 minutes, the one retention this API
// takes, the request asks for it with prompt_cache_options. A retention of
// anything else sends no such field. It sends Sampling's Temperature,
// MaxTokens, ReasoningEffort and ReasoningSummary.
func (w Responses) Encode(call Call) (*Request, error) {
	native, err := w.Deferred.native()
	if err != nil {
		return nil, err
	}
	body, names, err := encodeResponsesRequest(call.Model, call.Request, native)
	if err == nil {
		body, err = withCacheKeyField(body, w.cacheKeyField(), call.CacheKey)
	}
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if call.APIKey != "" {
		header.Set("Authorization", "Bearer "+call.APIKey)
	}
	return &Request{Body: body, Header: header, NewDecoder: func() Decoder { return newResponsesDecoder(names) }}, nil
}

func encodeResponsesRequest(model gotato.ModelConfig, request gotato.ModelRequest, native bool) ([]byte, map[string]string, error) {
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	layout := newDeferredLayout(request, deferredMode(native))
	input, err := responsesInput(model, request.Messages, names, layout)
	if err != nil {
		return nil, nil, err
	}

	// Deferred tools, per https://developers.openai.com/api/docs/guides/tools-tool-search
	// (client-executed tool search): a tool_search entry with execution
	// "client" stands for our search function, the other tools are functions,
	// and the catalog's go in with defer_loading true. The Model's search shows
	// as a tool_search_call item, and our answer as a tool_search_output item
	// that lists the functions it loads; a loaded function is injected at the
	// end of the context, so the tool list stays the same.
	tools, err := responsesTools(orderedTools(layout, request), names, layout)
	if err != nil {
		return nil, nil, err
	}

	payload := responsesRequest{
		Model:             model.Name,
		Store:             false,
		Stream:            true,
		Instructions:      request.SystemInstructions,
		Input:             input,
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Tools:             tools,
	}
	applyResponsesSampling(&payload, model, request.Sampling)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("model: encode Responses request: %w", err)
	}
	return body, names, nil
}

// responsesInput converts the history into the request's input items.
func responsesInput(model gotato.ModelConfig, messages []gotato.Message, names map[string]string, layout *deferredLayout) ([]any, error) {
	input := make([]any, 0, len(messages)*2)
	start := roundStart(messages)
	for i, message := range messages {
		items, err := convertResponsesMessage(message, names, keepsReasoning(model.Facts.RoundReasoning, i, start), layout)
		if err != nil {
			return nil, err
		}
		input = append(input, items...)
	}
	return input, nil
}

// responsesTools encodes the tools in the order they are sent, the search
// tool as a client-executed tool_search when the native form is on.
func responsesTools(ordered []layoutTool, names map[string]string, layout *deferredLayout) ([]any, error) {
	tools := make([]any, 0, len(ordered))
	for _, entry := range ordered {
		tool, err := responsesFunctionTool(names, entry)
		if err != nil {
			return nil, err
		}
		if layout != nil && isSearchToolID(entry.spec.ID) {
			tools = append(tools, responsesToolSearch{Type: "tool_search", Execution: "client", Description: tool.Description, Parameters: tool.Parameters})
			continue
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// responsesFunctionTool encodes one tool as a function, deferred when the
// layout defers it.
func responsesFunctionTool(names map[string]string, entry layoutTool) (responsesTool, error) {
	name, err := assignFunctionName(names, entry.spec.ID)
	if err != nil {
		return responsesTool{}, err
	}
	parameters, err := toolSchema(entry.spec, `{"type":"object"}`)
	if err != nil {
		return responsesTool{}, err
	}
	return responsesTool{Type: "function", Name: name, Description: entry.spec.Description, Parameters: parameters, DeferLoading: entry.deferred}, nil
}

// applyResponsesSampling sets the request's sampling fields, and asks for the
// cache retention the model states when the API takes it.
func applyResponsesSampling(payload *responsesRequest, model gotato.ModelConfig, sampling gotato.Sampling) {
	if sampling.Temperature != nil {
		payload.Temperature = sampling.Temperature
	}
	if sampling.MaxTokens != 0 {
		payload.MaxOutputTokens = sampling.MaxTokens
	}
	if model.Facts.CacheRetention == responsesCacheTTL {
		payload.PromptCacheOptions = &responsesCacheOptions{TTL: "30m"}
	}
	if sampling.ReasoningEffort != "" || sampling.ReasoningSummary != "" {
		summary := sampling.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		payload.Reasoning = &responsesReasoning{Effort: sampling.ReasoningEffort, Summary: summary}
	}
}

// convertResponsesMessage turns one Message into the Responses input items it
// stands for: none, one or several.
func convertResponsesMessage(message gotato.Message, names map[string]string, replay reasoningReplay, native ...*deferredLayout) ([]any, error) {
	var layout *deferredLayout
	if len(native) > 0 {
		layout = native[0]
	}
	switch message.Role {
	case gotato.RoleUser, gotato.RoleNotice:
		// A notice (F4.4) is tagged text in a user item.
		content, err := responsesInputContent(message.Parts)
		if err != nil {
			return nil, err
		}
		if len(content) == 0 {
			return nil, nil
		}
		return []any{responsesMessage{Role: "user", Content: content}}, nil
	case gotato.RoleAssistant:
		return responsesAssistantItems(message, names, replay, layout)
	case gotato.RoleToolResult:
		return responsesToolResultItems(message, names, layout)
	default:
		return nil, fmt.Errorf("model: unsupported Message role %q", message.Role)
	}
}

// responsesAssistantItems is an assistant Message as output messages,
// reasoning items and function calls, in that order.
func responsesAssistantItems(message gotato.Message, names map[string]string, replay reasoningReplay, layout *deferredLayout) ([]any, error) {
	items := make([]any, 0, len(message.Parts)+len(message.ToolCalls))
	items, err := appendResponsesAssistantParts(items, message.Parts, replay)
	if err != nil {
		return nil, err
	}
	return appendResponsesCalls(items, message.ToolCalls, names, replay, layout)
}

// appendResponsesAssistantParts adds the Parts of an assistant Message as
// output messages and reasoning items.
func appendResponsesAssistantParts(items []any, parts []gotato.ContentPart, replay reasoningReplay) ([]any, error) {
	// Adjacent text Parts are one output message: transcripts saved
	// before Gotato coalesced deltas hold a reply as its fragments.
	var text strings.Builder
	for _, part := range parts {
		if part.Kind == gotato.ContentKindReasoning && replay == reasoningDropped {
			continue
		}
		var err error
		if items, err = appendResponsesAssistantPart(items, &text, part); err != nil {
			return nil, err
		}
	}
	return flushResponsesText(items, &text), nil
}

// appendResponsesCalls adds the Tool Calls of an assistant Message as
// function calls, and calls to the search tool as tool_search_call items.
func appendResponsesCalls(items []any, calls []gotato.ToolCall, names map[string]string, replay reasoningReplay, layout *deferredLayout) ([]any, error) {
	for _, call := range calls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return nil, err
		}
		if layout != nil && layout.searchCalls[string(call.ID)] {
			items = append(items, responsesSearchCallItem(call))
			continue
		}
		item := responsesFunctionCallItem(call, name)
		// A call whose reasoning item is left out goes without its item
		// ID too, as a call from another wire API does, so it never
		// names an output its reasoning came with.
		if replay == reasoningDropped {
			item.ID = ""
		}
		items = append(items, item)
	}
	return items, nil
}

// appendResponsesAssistantPart adds one assistant part: text to the pending
// output message, a signed reasoning item after the text before it.
func appendResponsesAssistantPart(items []any, text *strings.Builder, part gotato.ContentPart) ([]any, error) {
	switch part.Kind {
	case gotato.ContentKindReasoning:
		if len(part.Signature) == 0 {
			return items, nil
		}
		var reasoning json.RawMessage
		if err := json.Unmarshal(part.Signature, &reasoning); err != nil {
			return nil, fmt.Errorf("model: assistant reasoning signature is invalid: %w", err)
		}
		items = flushResponsesText(items, text)
		return append(items, reasoning), nil
	case gotato.ContentKindText:
		text.WriteString(part.Text)
	case gotato.ContentKindImage, gotato.ContentKindJSON:
		if len(part.Data) > 0 || part.Text != "" {
			return nil, fmt.Errorf("model: Responses assistant content kind %q is unsupported", part.Kind)
		}
	}
	return items, nil
}

// flushResponsesText adds the pending text as one output message and empties it.
func flushResponsesText(items []any, text *strings.Builder) []any {
	if text.Len() == 0 {
		return items
	}
	items = append(items, responsesOutputMessage{
		Type: "message", Role: "assistant", Status: "completed",
		Content: []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}},
	})
	text.Reset()
	return items
}

// responsesSearchCallItem replays a call to the search tool as a tool_search_call.
func responsesSearchCallItem(call gotato.ToolCall) responsesToolSearchCall {
	arguments := string(replayArguments(call.Arguments))
	callID, _ := splitResponsesCallID(string(call.ID))
	return responsesToolSearchCall{Type: "tool_search_call", Execution: "client", CallID: callID, Status: "completed", Arguments: json.RawMessage(arguments)}
}

// responsesFunctionCallItem replays a call as a function_call with its item ID.
func responsesFunctionCallItem(call gotato.ToolCall, name string) responsesFunctionCall {
	arguments := string(replayArguments(call.Arguments))
	callID, itemID := splitResponsesCallID(string(call.ID))
	// A JSON object is the chat API's extra_content, not an item ID.
	if len(call.Signature) > 0 && !isJSONObject(call.Signature) {
		itemID = string(call.Signature)
	}
	return responsesFunctionCall{Type: "function_call", ID: itemID, CallID: callID, Name: name, Arguments: arguments}
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
		return nil, fmt.Errorf("model: Tool result has no Call ID")
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
			tool, err := responsesFunctionTool(names, layoutTool{spec: entry.spec, deferred: true})
			if err != nil {
				return nil, err
			}
			loaded = append(loaded, tool)
		}
	}
	return loaded, nil
}

func responsesInputContent(parts []gotato.ContentPart) ([]any, error) {
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		item, err := responsesInputItem(part)
		if err != nil {
			return nil, err
		}
		if item != nil {
			content = append(content, item)
		}
	}
	return content, nil
}

// responsesInputItem is one user Part as an input item, or nil for a Part
// that sends nothing.
func responsesInputItem(part gotato.ContentPart) (map[string]any, error) {
	switch part.Kind {
	case gotato.ContentKindText:
		if part.Text != "" {
			return map[string]any{"type": "input_text", "text": part.Text}, nil
		}
	case gotato.ContentKindImage:
		if len(part.Data) > 0 {
			return responsesInputImage(part), nil
		}
	case gotato.ContentKindReasoning, gotato.ContentKindJSON:
		if part.Text != "" || len(part.Data) > 0 {
			return nil, fmt.Errorf("model: Responses user content kind %q is unsupported", part.Kind)
		}
	}
	return nil, nil
}

// responsesInputImage is an image Part as an input_image with a data URL.
func responsesInputImage(part gotato.ContentPart) map[string]any {
	mimeType := part.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return map[string]any{
		"type":      "input_image",
		"detail":    "auto",
		"image_url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data),
	}
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
