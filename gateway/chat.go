package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
)

func encodeRequest(model string, request gotato.ModelRequest, rules providerRules) ([]byte, map[string]string, error) {
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]wireMessage, 0, len(request.Messages)+1)
	if request.SystemInstructions != "" {
		messages = append(messages, wireMessage{Role: "system", Content: request.SystemInstructions})
	}
	start := roundStart(request.Messages)
	for i, message := range request.Messages {
		keep := rules.keepsReasoning(i, start)
		converted, err := convertMessage(message, names, keep)
		if err != nil {
			return nil, nil, err
		}
		// A reply that was only reasoning has nothing left to send.
		if !keep && converted.Role == "assistant" && converted.Content == "" && len(converted.ToolCalls) == 0 && gotato.ReasoningOf(message) != "" {
			continue
		}
		messages = append(messages, converted)
	}
	tools := make([]wireTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		name, err := assignFunctionName(names, spec.ID)
		if err != nil {
			return nil, nil, err
		}
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = []byte(`{}`)
		}
		if !json.Valid(parameters) {
			return nil, nil, fmt.Errorf("gateway: Tool %q has invalid InputSchema", spec.ID)
		}
		tools = append(tools, wireTool{Type: "function", Function: wireFunction{Name: name, Description: spec.Description, Parameters: json.RawMessage(parameters)}})
	}
	payload := wireRequest{Model: model, Messages: messages, Tools: tools, Stream: true, StreamOptions: &wireStreamOptions{IncludeUsage: true}}
	if request.Config.Temperature != nil || request.Config.MaxTokens != 0 {
		payload.Temperature = request.Config.Temperature
		if request.Config.MaxTokens != 0 {
			payload.MaxTokens = request.Config.MaxTokens
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode request: %w", err)
	}
	return body, names, nil
}

func convertMessage(message gotato.Message, names map[string]string, keepReasoning bool) (wireMessage, error) {
	role := string(message.Role)
	if role == string(gotato.RoleToolResult) {
		content := toolResultText(message)
		if content == "" && message.ToolResult != nil {
			content = message.ToolResult.SafeError
		}
		if content == "" {
			content = "(no tool output)"
		}
		callID := ""
		if message.ToolResult != nil {
			callID = string(message.ToolResult.CallID)
		}
		return wireMessage{Role: "tool", Content: content, ToolCallID: callID}, nil
	}
	if message.Role == gotato.RoleNotice {
		// Chat Completions has no system message mid-conversation: a notice
		// (G-F29) goes as a user message on the wire, tagged text, and the
		// Record keeps it a notice.
		role = string(gotato.RoleUser)
	}
	if role != string(gotato.RoleUser) && role != string(gotato.RoleAssistant) {
		return wireMessage{}, fmt.Errorf("gateway: unsupported Message role %q", role)
	}
	for _, part := range message.Parts {
		if part.Kind != gotato.ContentKindText && part.Kind != gotato.ContentKindReasoning && (part.Text != "" || len(part.Data) > 0) {
			return wireMessage{}, fmt.Errorf("gateway: unsupported content kind %q", part.Kind)
		}
	}
	out := wireMessage{Role: role, Content: gotato.TextOf(message)}
	if role == string(gotato.RoleAssistant) && keepReasoning {
		out.ReasoningContent = gotato.ReasoningOf(message)
	}
	for _, call := range message.ToolCalls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return wireMessage{}, err
		}
		wire := wireToolCall{ID: string(call.ID), Type: "function", Function: wireFunctionCall{Name: name, Arguments: string(replayArguments(call.Arguments))}}
		// The call's extra_content goes back as it came. Another wire API's
		// artifact, such as a Responses item ID, is not an object.
		if isJSONObject(call.Signature) {
			wire.ExtraContent = json.RawMessage(call.Signature)
		}
		out.ToolCalls = append(out.ToolCalls, wire)
	}
	return out, nil
}

type wireRequest struct {
	Model         string             `json:"model"`
	Messages      []wireMessage      `json:"messages"`
	Tools         []wireTool         `json:"tools,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *wireStreamOptions `json:"stream_options,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	MaxTokens     uint32             `json:"max_tokens,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}
type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// ReasoningContent is the model's thinking (DeepSeek), streamed as
	// delta.reasoning_content; a thinking model requires it back.
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}
type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
	// ExtraContent is provider data on a call, kept as it came: Gemini puts
	// the thought signature it requires back here (google.thought_signature).
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}
type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type stream struct {
	response *http.Response
	reader   *bufio.Reader
	nameMap  map[string]string
	// calls are the Tool calls in the order they first appeared; byIndex
	// is the call each provider index names now, and last the call the
	// latest delta added to.
	calls     []*callBuffer
	byIndex   map[int]*callBuffer
	last      *callBuffer
	queue     []gotato.ModelEvent
	finish    gotato.StopReason
	usage     gotato.Usage
	model     string
	finished  bool
	restart   restarter
	overflow  overflowMatcher
	closeOnce sync.Once
}

type callBuffer struct {
	id, name, arguments string
	// extra is the call's extra_content, from whichever delta carried it.
	extra json.RawMessage
}

type wireChunk struct {
	// Model is the model that answered, which a gateway with fallbacks may
	// have chosen over the requested one.
	Model string `json:"model"`
	// Error is an error the provider sent in the middle of the stream, as
	// {"error": {...}} in place of a chunk.
	Error *struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
	} `json:"error"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
				ExtraContent json.RawMessage `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        uint64 `json:"prompt_tokens"`
		CompletionTokens    uint64 `json:"completion_tokens"`
		TotalTokens         uint64 `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens uint64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details,omitempty"`
		// PromptCacheHitTokens is DeepSeek's name for cached prompt tokens.
		PromptCacheHitTokens uint64 `json:"prompt_cache_hit_tokens,omitempty"`
	} `json:"usage"`
}

func (s *stream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.finished {
			return gotato.ModelEvent{}, io.EOF
		}
		data, err := s.nextData(ctx)
		if errors.Is(err, io.EOF) && s.finish != "" {
			// A provider that omits [DONE] after a finish_reason still
			// completed the response.
			s.finishStream()
			continue
		}
		if err != nil {
			response, rerr := s.restart.recover(ctx, err)
			if rerr != nil {
				return gotato.ModelEvent{}, rerr
			}
			s.reopen(response)
			continue
		}
		if data == "[DONE]" {
			s.finishStream()
			continue
		}
		if data == "" {
			continue
		}
		var chunk wireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return gotato.ModelEvent{}, fmt.Errorf("gateway: invalid SSE chunk: %w", err)
		}
		if chunk.Error != nil {
			// An error is not an interruption: it is not sent again as a
			// cut stream is, and an overflow is reported as one.
			s.finished = true
			message := chunk.Error.Message
			if message == "" {
				message = chunk.Error.Type
			}
			return gotato.ModelEvent{}, s.overflow.streamError("Chat Completions stream error", message, errorCode(chunk.Error.Code), chunk.Error.Type)
		}
		s.processChunk(chunk)
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	s.restart.delivered = true
	return event, nil
}

// reopen continues on a re-issued response, discarding everything decoded
// from the interrupted one (nothing of it reached the caller).
func (s *stream) reopen(response *http.Response) {
	if s.response != nil && s.response.Body != nil {
		_ = s.response.Body.Close()
	}
	s.response = response
	s.reader = bufio.NewReader(response.Body)
	s.calls = nil
	s.byIndex = make(map[int]*callBuffer)
	s.last = nil
	s.queue = nil
	s.finish = ""
	s.usage = gotato.Usage{}
}

func (s *stream) nextData(ctx context.Context) (string, error) {
	return readSSEEvent(ctx, s.reader)
}

func (s *stream) processChunk(chunk wireChunk) {
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		s.usage = gotato.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		cached := chunk.Usage.PromptCacheHitTokens
		if d := chunk.Usage.PromptTokensDetails; d != nil && d.CachedTokens > cached {
			cached = d.CachedTokens
		}
		s.usage.CacheReadTokens = min(cached, s.usage.InputTokens)
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindUsage, Usage: s.usage})
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.ReasoningContent != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDelta, Text: choice.Delta.ReasoningContent})
		}
		if choice.Delta.Content != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: choice.Delta.Content})
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := s.callFor(delta.Index, delta.ID)
			if delta.ID != "" {
				call.id = delta.ID
			}
			if delta.Function.Name != "" {
				call.name += delta.Function.Name
			}
			call.arguments += delta.Function.Arguments
			if isJSONObject(delta.ExtraContent) {
				call.extra = delta.ExtraContent
			}
		}
		if choice.FinishReason != nil {
			s.finish = mapStopReason(*choice.FinishReason)
		}
	}
}

// callFor returns the Tool call a delta adds to. The chat API names each
// call by its index, and sends its ID once, in its first delta. Gemini's chat
// API sends parallel calls whole, each with its own ID, but without an
// index, with a null one, or all at index 0. So a delta that names an ID
// other than the one its index holds starts a new call, and a delta without
// an index adds to the last call.
func (s *stream) callFor(index *int, id string) *callBuffer {
	call := s.last
	if index != nil {
		call = s.byIndex[*index]
	}
	if call == nil || (id != "" && call.id != "" && id != call.id) {
		call = &callBuffer{}
		s.calls = append(s.calls, call)
		if index != nil {
			s.byIndex[*index] = call
		}
	}
	s.last = call
	return call
}

func (s *stream) finishStream() {
	for _, call := range s.calls {
		name := call.name
		if original, ok := s.nameMap[name]; ok {
			name = original
		}
		// A provider may stream a call that takes no arguments with empty
		// arguments. Core accepts only JSON, so the call would fail as invalid
		// arguments; the Responses and Anthropic adapters send {} too.
		arguments := call.arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindToolCall, ToolCall: &gotato.ToolCall{ID: gotato.ToolCallID(call.id), ToolID: name, Arguments: []byte(arguments), Signature: call.extra}})
	}
	if s.finish == "" {
		s.finish = gotato.StopReasonEndTurn
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: s.finish, Usage: s.usage, Model: s.model})
	s.finished = true
}

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	return nil
}

func mapStopReason(reason string) gotato.StopReason {
	switch reason {
	case "tool_calls", "function_call":
		return gotato.StopReasonToolCalls
	case "length":
		return gotato.StopReasonMaxTokens
	case "content_filter":
		return gotato.StopReasonError
	case "stop", "":
		return gotato.StopReasonEndTurn
	default:
		return gotato.StopReasonEndTurn
	}
}
