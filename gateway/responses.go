package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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

func convertResponsesMessage(message gotato.Message, names map[string]string, keepReasoning bool, native ...*deferredLayout) ([]any, error) {
	var layout *deferredLayout
	if len(native) > 0 {
		layout = native[0]
	}
	role := string(message.Role)
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
	case gotato.RoleToolResult:
		callID := ""
		if message.ToolResult != nil {
			callID, _ = splitResponsesCallID(string(message.ToolResult.CallID))
		}
		if callID == "" {
			return nil, fmt.Errorf("gateway: Tool result has no Call ID")
		}
		if layout != nil && layout.searchCalls[string(message.ToolResult.CallID)] {
			loaded := []responsesTool{}
			for _, id := range layout.references(message.ToolResult) {
				for _, entry := range layout.tools {
					if entry.spec.ID != id {
						continue
					}
					name, err := assignFunctionName(names, id)
					if err != nil {
						return nil, err
					}
					parameters := entry.spec.InputSchema
					if len(parameters) == 0 || !json.Valid(parameters) {
						parameters = []byte(`{"type":"object"}`)
					}
					loaded = append(loaded, responsesTool{Type: "function", Name: name, Description: entry.spec.Description, Parameters: json.RawMessage(parameters), DeferLoading: true})
				}
			}
			return []any{responsesToolSearchOutput{Type: "tool_search_output", Execution: "client", CallID: callID, Status: "completed", Tools: loaded}}, nil
		}
		output := toolResultOutput(message)
		return []any{responsesFunctionOutput{Type: "function_call_output", CallID: callID, Output: output}}, nil
	default:
		return nil, fmt.Errorf("gateway: unsupported Message role %q", role)
	}
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

type responsesStream struct {
	sseBody
	nameMap  map[string]string
	calls    map[int]*responsesCall
	texts    map[int]bool
	queue    []gotato.ModelEvent
	model    string
	finished bool
	terminal error
	restart  restarter
	overflow overflowMatcher
}

type responsesCall struct {
	id        string
	callID    string
	name      string
	arguments string
	// argumentsDone records that the arguments arrived whole, through
	// response.function_call_arguments.done or response.output_item.done.
	argumentsDone bool
	emitted       bool
}

type responsesOutputItem struct {
	Type             string             `json:"type"`
	ID               string             `json:"id"`
	CallID           string             `json:"call_id"`
	Name             string             `json:"name"`
	Arguments        responsesArguments `json:"arguments"`
	EncryptedContent string             `json:"encrypted_content,omitempty"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

type responsesResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	Model             string `json:"model,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Usage *struct {
		InputTokens        uint64 `json:"input_tokens"`
		OutputTokens       uint64 `json:"output_tokens"`
		TotalTokens        uint64 `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens     uint64 `json:"cached_tokens"`
			CacheWriteTokens uint64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
	Output []responsesOutputItem `json:"output,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type responsesEvent struct {
	Type        string               `json:"type"`
	OutputIndex int                  `json:"output_index"`
	Delta       string               `json:"delta"`
	Arguments   string               `json:"arguments"`
	Item        *responsesOutputItem `json:"item,omitempty"`
	Response    *responsesResponse   `json:"response,omitempty"`
	Code        string               `json:"code,omitempty"`
	Message     string               `json:"message,omitempty"`
}

func (s *responsesStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.finished {
			if s.terminal != nil {
				err := s.terminal
				s.terminal = nil
				return gotato.ModelEvent{}, err
			}
			return gotato.ModelEvent{}, io.EOF
		}
		data, err := readSSEEvent(ctx, s.reader)
		if err != nil {
			response, rerr := s.restart.recover(ctx, err)
			if rerr != nil {
				var gerr *Error
				if errors.Is(err, io.EOF) && errors.As(rerr, &gerr) {
					gerr.Message = "Responses stream ended before completion (" + gerr.Message + ")"
				}
				return gotato.ModelEvent{}, rerr
			}
			s.reopen(response)
			continue
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		var event responsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return gotato.ModelEvent{}, fmt.Errorf("gateway: invalid Responses SSE event: %w", err)
		}
		s.processResponsesEvent(event, []byte(data))
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	s.restart.delivered = true
	return event, nil
}

// reopen continues on a re-issued response, discarding everything decoded
// from the interrupted one (nothing of it reached the caller).
func (s *responsesStream) reopen(response *http.Response) {
	s.replace(response)
	s.calls = make(map[int]*responsesCall)
	s.texts = make(map[int]bool)
	s.queue = nil
}

func (s *responsesStream) processResponsesEvent(event responsesEvent, raw []byte) {
	switch event.Type {
	case "response.created", "response.in_progress":
		if event.Response != nil && event.Response.Model != "" {
			s.model = event.Response.Model
		}
	case "response.output_item.added":
		if event.Item == nil {
			return
		}
		s.ensureResponsesCall(event.OutputIndex, event.Item)
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta != "" {
			s.texts[event.OutputIndex] = true
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: event.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDelta, Text: event.Delta})
		}
	case "response.function_call_arguments.delta":
		call := s.calls[event.OutputIndex]
		if call == nil {
			call = &responsesCall{}
			s.calls[event.OutputIndex] = call
		}
		call.arguments += event.Delta
	case "response.function_call_arguments.done":
		call := s.calls[event.OutputIndex]
		if call == nil {
			call = &responsesCall{}
			s.calls[event.OutputIndex] = call
		}
		call.arguments = event.Arguments
		call.argumentsDone = true
	case "response.output_item.done":
		if event.Item == nil {
			return
		}
		itemRaw := responsesItemRaw(raw)
		s.finishResponsesItem(event.OutputIndex, *event.Item, itemRaw)
	case "response.completed", "response.incomplete":
		s.finishResponsesResponse(event.Response)
	case "response.failed":
		message := event.Message
		if event.Response != nil && event.Response.Error != nil {
			message = event.Response.Error.Message
			if message == "" {
				message = event.Response.Error.Code
			}
		}
		if message == "" {
			message = "Responses response failed"
		}
		code := ""
		if event.Response != nil && event.Response.Error != nil {
			code = event.Response.Error.Code
		}
		s.finishWithError(s.overflow.streamError("Responses response failed", message, code))
	case "error":
		message := event.Message
		if message == "" {
			message = event.Code
		}
		if message == "" {
			message = "Responses stream error"
		}
		s.finishWithError(s.overflow.streamError("Responses stream error", message, event.Code))
	}
}

func responsesItemRaw(event []byte) []byte {
	var envelope struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil || len(envelope.Item) == 0 {
		return nil
	}
	return append([]byte(nil), envelope.Item...)
}

func (s *responsesStream) ensureResponsesCall(index int, item *responsesOutputItem) {
	if item.Type != "function_call" && item.Type != "tool_search_call" {
		return
	}
	call := s.calls[index]
	if call == nil {
		call = &responsesCall{}
		s.calls[index] = call
	}
	if item.ID != "" {
		call.id = item.ID
	}
	if item.CallID != "" {
		call.callID = item.CallID
	}
	if item.Name != "" {
		call.name = item.Name
	}
	if item.Type == "tool_search_call" {
		// The Model's search is a call to our search tool.
		call.name = s.searchToolName()
	}
	if item.Arguments != "" {
		call.arguments = string(item.Arguments)
	}
}

func (s *responsesStream) finishResponsesItem(index int, item responsesOutputItem, raw []byte) {
	if item.Type == "reasoning" {
		artifact, err := json.Marshal(item)
		if err == nil && len(raw) > 0 {
			artifact = append([]byte(nil), raw...)
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: artifact})
		return
	}
	if item.Type == "message" && !s.texts[index] {
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: content.Text})
			}
		}
		return
	}
	if item.Type != "function_call" && item.Type != "tool_search_call" {
		return
	}
	s.ensureResponsesCall(index, &item)
	if call := s.calls[index]; call != nil {
		call.argumentsDone = true
	}
	s.emitResponsesCall(index)
}

// searchToolName is the provider name the search tool was sent under.
func (s *responsesStream) searchToolName() string {
	for name, id := range s.nameMap {
		if isSearchToolID(id) {
			return name
		}
	}
	return gotato.SearchToolName
}

func (s *responsesStream) emitResponsesCall(index int) {
	call := s.calls[index]
	if call == nil || call.emitted {
		return
	}
	name := call.name
	if original, ok := s.nameMap[name]; ok {
		name = original
	}
	// Validate before marking emitted: a call that cannot be delivered must
	// not make finishResponsesResponse report StopReasonToolCalls.
	if name == "" || call.callID == "" {
		return
	}
	call.emitted = true
	arguments := call.arguments
	if arguments == "" {
		arguments = "{}"
	}
	// The call ID is the one the Session and its Tool Result use. The
	// provider's item ID is adapter-owned, so it travels as the Signature.
	toolCall := &gotato.ToolCall{ID: gotato.ToolCallID(call.callID), ToolID: name, Arguments: []byte(arguments)}
	if call.id != "" {
		toolCall.Signature = []byte(call.id)
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindToolCall, ToolCall: toolCall})
}

func (s *responsesStream) finishResponsesResponse(response *responsesResponse) {
	if response == nil {
		s.finishWithError(fmt.Errorf("gateway: Responses completion has no response"))
		return
	}
	stop := responsesStopReason(response.Status, response.IncompleteDetails)
	if response.Model != "" {
		s.model = response.Model
	}
	indices := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		// A call that the output limit cut off has incomplete arguments, so
		// it is not delivered.
		if stop == gotato.StopReasonMaxTokens && !s.calls[index].argumentsDone {
			continue
		}
		s.emitResponsesCall(index)
	}
	if response.Usage != nil {
		usage := gotato.Usage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}
		if details := response.Usage.InputTokensDetails; details != nil {
			usage.CacheReadTokens = details.CachedTokens
			usage.CacheWriteTokens = details.CacheWriteTokens
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindUsage, Usage: usage})
	}
	// Delivered calls make the stop StopReasonToolCalls, unless the output limit
	// cut the response off: StopReasonMaxTokens stays, so the agent learns it.
	for _, call := range s.calls {
		if call.emitted && stop != gotato.StopReasonMaxTokens {
			stop = gotato.StopReasonToolCalls
			break
		}
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: stop, Model: s.model})
	s.finished = true
}

func responsesStopReason(status string, details *struct {
	Reason string `json:"reason"`
}) gotato.StopReason {
	if status == "incomplete" && details != nil && details.Reason == "max_output_tokens" {
		return gotato.StopReasonMaxTokens
	}
	if status == "failed" || status == "canceled" || (status == "incomplete" && (details == nil || details.Reason != "max_output_tokens")) {
		return gotato.StopReasonError
	}
	return gotato.StopReasonEndTurn
}

func (s *responsesStream) finishWithError(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.terminal = err
}
