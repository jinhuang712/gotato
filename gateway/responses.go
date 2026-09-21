package gateway

import (
	"bufio"
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
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

const defaultResponsesBaseURL = "https://api.openai.com/v1"

func (c *Client) streamResponses(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	body, names, err := encodeResponsesRequest(c.model, request)
	if err != nil {
		return nil, err
	}
	response, err := c.doResponses(ctx, body)
	if err != nil {
		return nil, err
	}
	return &responsesStream{
		response: response,
		reader:   bufio.NewReader(response.Body),
		nameMap:  names,
		calls:    make(map[int]*responsesCall),
		texts:    make(map[int]bool),
	}, nil
}

func (c *Client) doResponses(ctx context.Context, body []byte) (*http.Response, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, &Error{Message: "gateway: api_key is required for the Responses API"}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for key, value := range c.headers {
			req.Header.Set(key, value)
		}
		// Protocol headers are set after the configurable ones so a provider
		// header override cannot break authentication or streaming.
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("User-Agent", "gotato/0.1")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		response, requestErr := c.httpClient.Do(req)
		if requestErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < c.maxRetries {
				if err := wait(ctx, withJitter(c.retryBackoff*time.Duration(attempt+1))); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &Error{Retryable: true, Message: requestErr.Error()}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		message := readErrorBody(response.Body)
		_ = response.Body.Close()
		retryable := retryableStatus(response.StatusCode, message)
		if retryable && attempt < c.maxRetries {
			if err := wait(ctx, c.retryDelay(response.Header, attempt)); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &Error{StatusCode: response.StatusCode, Retryable: retryable, Message: message}
	}
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
	Tools             []responsesTool     `json:"tools,omitempty"`
	Reasoning         *responsesReasoning `json:"reasoning,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	MaxOutputTokens   uint32              `json:"max_output_tokens,omitempty"`
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

func encodeResponsesRequest(model string, request gotato.ModelRequest) ([]byte, map[string]string, error) {
	names := make(map[string]string, len(request.Tools))
	input := make([]any, 0, len(request.Messages)*2)
	for _, message := range request.Messages {
		items, err := convertResponsesMessage(message, names)
		if err != nil {
			return nil, nil, err
		}
		input = append(input, items...)
	}

	tools := make([]responsesTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		name := gatewayFunctionName(spec.ID)
		if previous, exists := names[name]; exists && previous != spec.ID {
			return nil, nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, spec.ID)
		}
		names[name] = spec.ID
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = []byte(`{"type":"object"}`)
		}
		if !json.Valid(parameters) {
			return nil, nil, fmt.Errorf("gateway: Tool %q has invalid InputSchema", spec.ID)
		}
		tools = append(tools, responsesTool{Type: "function", Name: name, Description: spec.Description, Parameters: json.RawMessage(parameters)})
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
	if request.Options.Temperature != nil {
		payload.Temperature = request.Options.Temperature
	}
	if request.Options.MaxTokens != 0 {
		payload.MaxOutputTokens = request.Options.MaxTokens
	}
	if request.Options.ReasoningEffort != "" {
		summary := request.Options.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		payload.Reasoning = &responsesReasoning{Effort: request.Options.ReasoningEffort, Summary: summary}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode Responses request: %w", err)
	}
	return body, names, nil
}

func convertResponsesMessage(message gotato.Message, names map[string]string) ([]any, error) {
	role := string(message.Role)
	switch message.Role {
	case gotato.RoleUser:
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
		for _, part := range message.Parts {
			switch part.Kind {
			case gotato.ContentReasoning:
				if len(part.Signature) == 0 {
					continue
				}
				var reasoning json.RawMessage
				if err := json.Unmarshal(part.Signature, &reasoning); err != nil {
					return nil, fmt.Errorf("gateway: assistant reasoning signature is invalid: %w", err)
				}
				items = append(items, reasoning)
			case gotato.ContentText:
				if part.Text == "" {
					continue
				}
				items = append(items, responsesOutputMessage{
					Type: "message", Role: "assistant", Status: "completed",
					Content: []any{map[string]any{"type": "output_text", "text": part.Text, "annotations": []any{}}},
				})
			case gotato.ContentImage, gotato.ContentJSON:
				if len(part.Data) > 0 || part.Text != "" {
					return nil, fmt.Errorf("gateway: Responses assistant content kind %q is unsupported", part.Kind)
				}
			}
		}
		for _, call := range message.ToolCalls {
			name := gatewayFunctionName(call.ToolID)
			if previous, exists := names[name]; exists && previous != call.ToolID {
				return nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, call.ToolID)
			}
			names[name] = call.ToolID
			arguments := string(call.Arguments)
			if arguments == "" {
				arguments = "{}"
			}
			if !json.Valid([]byte(arguments)) {
				return nil, fmt.Errorf("gateway: Tool Call %q has invalid arguments", call.ID)
			}
			callID, itemID := splitResponsesCallID(string(call.ID))
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
		output := toolResultText(message)
		if output == "" && message.ToolResult != nil {
			output = message.ToolResult.SafeError
		}
		if output == "" {
			output = "(no tool output)"
		}
		return []any{responsesFunctionOutput{Type: "function_call_output", CallID: callID, Output: output}}, nil
	default:
		return nil, fmt.Errorf("gateway: unsupported Message role %q", role)
	}
}

func responsesInputContent(parts []gotato.ContentPart) ([]any, error) {
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case gotato.ContentText:
			if part.Text != "" {
				content = append(content, map[string]any{"type": "input_text", "text": part.Text})
			}
		case gotato.ContentImage:
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
		case gotato.ContentReasoning, gotato.ContentJSON:
			if part.Text != "" || len(part.Data) > 0 {
				return nil, fmt.Errorf("gateway: Responses user content kind %q is unsupported", part.Kind)
			}
		}
	}
	return content, nil
}

func splitResponsesCallID(id string) (callID, itemID string) {
	if index := strings.IndexByte(id, '|'); index >= 0 {
		return id[:index], id[index+1:]
	}
	return id, ""
}

type responsesStream struct {
	response  *http.Response
	reader    *bufio.Reader
	nameMap   map[string]string
	calls     map[int]*responsesCall
	texts     map[int]bool
	queue     []gotato.ModelEvent
	finished  bool
	terminal  error
	closeOnce sync.Once
}

type responsesCall struct {
	id        string
	callID    string
	name      string
	arguments string
	emitted   bool
}

type responsesOutputItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
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
		data, err := nextSSEData(ctx, s.reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return gotato.ModelEvent{}, fmt.Errorf("gateway: Responses stream ended before completion")
			}
			return gotato.ModelEvent{}, err
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
	return event, nil
}

func nextSSEData(ctx context.Context, reader *bufio.Reader) (string, error) {
	return readSSEEvent(ctx, reader)
}

func (s *responsesStream) processResponsesEvent(event responsesEvent, raw []byte) {
	switch event.Type {
	case "response.output_item.added":
		if event.Item == nil {
			return
		}
		s.ensureResponsesCall(event.OutputIndex, event.Item)
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta != "" {
			s.texts[event.OutputIndex] = true
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: event.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDelta, Text: event.Delta})
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
		s.finishWithError(fmt.Errorf("gateway: Responses response failed: %s", message))
	case "error":
		message := event.Message
		if message == "" {
			message = event.Code
		}
		if message == "" {
			message = "Responses stream error"
		}
		s.finishWithError(fmt.Errorf("gateway: Responses stream error: %s", message))
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
	if item.Type != "function_call" {
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
	if item.Arguments != "" {
		call.arguments = item.Arguments
	}
}

func (s *responsesStream) finishResponsesItem(index int, item responsesOutputItem, raw []byte) {
	if item.Type == "reasoning" {
		artifact, err := json.Marshal(item)
		if err == nil && len(raw) > 0 {
			artifact = append([]byte(nil), raw...)
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDone, ReasoningArtifact: artifact})
		return
	}
	if item.Type == "message" && !s.texts[index] {
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: content.Text})
			}
		}
		return
	}
	if item.Type != "function_call" {
		return
	}
	s.ensureResponsesCall(index, &item)
	s.emitResponsesCall(index)
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
	// not make finishResponsesResponse report StopToolCalls.
	if name == "" || call.callID == "" {
		return
	}
	call.emitted = true
	arguments := call.arguments
	if arguments == "" {
		arguments = "{}"
	}
	id := call.callID
	if call.id != "" {
		id += "|" + call.id
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelToolCall, ToolCall: &gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: name, Arguments: []byte(arguments)}})
}

func (s *responsesStream) finishResponsesResponse(response *responsesResponse) {
	if response == nil {
		s.finishWithError(fmt.Errorf("gateway: Responses completion has no response"))
		return
	}
	indices := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		s.emitResponsesCall(index)
	}
	if response.Usage != nil {
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelUsage, Usage: gotato.Usage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}})
	}
	stop := responsesStopReason(response.Status, response.IncompleteDetails)
	for _, call := range s.calls {
		if call.emitted {
			stop = gotato.StopToolCalls
			break
		}
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: stop})
	s.finished = true
}

func responsesStopReason(status string, details *struct {
	Reason string `json:"reason"`
}) gotato.StopReason {
	if status == "incomplete" && details != nil && details.Reason == "max_output_tokens" {
		return gotato.StopMaxTokens
	}
	if status == "failed" || status == "cancelled" || (status == "incomplete" && (details == nil || details.Reason != "max_output_tokens")) {
		return gotato.StopError
	}
	return gotato.StopEndTurn
}

func (s *responsesStream) finishWithError(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.terminal = err
}

func (s *responsesStream) Close() error {
	s.closeOnce.Do(func() {
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	return nil
}
