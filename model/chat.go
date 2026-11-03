package model

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

// ChatCompletions is the OpenAI-compatible Chat Completions wire API, spoken by
// OpenAI and by most compatible servers. The zero value is the default.
type ChatCompletions struct {
	// CacheKeyField names the top-level body field that carries the request's
	// cache key, for a server that routes its prompt cache by one. Empty sends
	// no field: the Chat Completions API names none of its own. "none" is the
	// same. It must not name a field the request already sends.
	CacheKeyField string
}

// Validate refuses a CacheKeyField that names a field the request already
// sends, which the cache key would silently replace. New calls it.
func (w ChatCompletions) Validate() error {
	return checkCacheKeyField(w.CacheKeyField, reflect.TypeFor[chatRequest](), chatReservedFields)
}

// chatReservedFields are the top-level fields the request carries besides the
// ones its encoder struct declares: fields the API takes that a cache key must
// not replace either.
var chatReservedFields = []string{"tool_choice", "parallel_tool_calls", "max_completion_tokens", "response_format", "stop", "n", "user", "metadata", "store"}

// Encode implements WireAPI. The model's facts steer one thing: with
// RoundReasoning the reasoning of an earlier Round is left out of the history.
// The Chat Completions API has no native form of deferred tools, so a Session
// with a catalog gets the tools loaded so far, and the list grows when one
// loads. It sends Sampling.Temperature and MaxTokens, and refuses a request
// that sets a reasoning effort or summary, which it cannot carry.
func (w ChatCompletions) Encode(call Call) (*Request, error) {
	body, names, err := encodeChatRequest(call.Model, call.Request)
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
	return &Request{Body: body, Header: header, NewDecoder: func() Decoder { return newChatDecoder(names) }}, nil
}

func (w ChatCompletions) cacheKeyField() string {
	if w.CacheKeyField == cacheKeyFieldNone {
		return ""
	}
	return w.CacheKeyField
}

func encodeChatRequest(model gotato.ModelConfig, request gotato.ModelRequest) ([]byte, map[string]string, error) {
	if err := refuseReasoningConfig("Chat Completions", request.Sampling); err != nil {
		return nil, nil, err
	}
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]chatMessage, 0, len(request.Messages)+1)
	if request.SystemInstructions != "" {
		messages = append(messages, chatMessage{Role: "system", Content: request.SystemInstructions})
	}
	start := roundStart(request.Messages)
	for i, message := range request.Messages {
		keep := keepsReasoning(model.Facts.RoundReasoning, i, start)
		converted, err := convertChatMessage(message, names, keep)
		if err != nil {
			return nil, nil, err
		}
		// A reply that was only reasoning has nothing left to send.
		if !keep && converted.Role == "assistant" && converted.Content == "" && len(converted.ToolCalls) == 0 && gotato.ReasoningOf(message) != "" {
			continue
		}
		messages = append(messages, converted)
	}
	tools := make([]chatTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		name, err := assignFunctionName(names, spec.ID)
		if err != nil {
			return nil, nil, err
		}
		parameters, err := toolSchema(spec, `{}`)
		if err != nil {
			return nil, nil, err
		}
		tools = append(tools, chatTool{Type: "function", Function: chatFunction{Name: name, Description: spec.Description, Parameters: parameters}})
	}
	payload := chatRequest{Model: model.Name, Messages: messages, Tools: tools, Stream: true, StreamOptions: &chatStreamOptions{IncludeUsage: true}}
	payload.Temperature = request.Sampling.Temperature
	payload.MaxTokens = request.Sampling.MaxTokens
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("model: encode request: %w", err)
	}
	return body, names, nil
}

func convertChatMessage(message gotato.Message, names map[string]string, keepReasoning bool) (chatMessage, error) {
	role := string(message.Role)
	if role == string(gotato.RoleToolResult) {
		content := toolResultOutput(message)
		callID := ""
		if message.ToolResult != nil {
			callID = string(message.ToolResult.CallID)
		}
		return chatMessage{Role: "tool", Content: content, ToolCallID: callID}, nil
	}
	if message.Role == gotato.RoleNotice {
		// Chat Completions has no system message mid-conversation: a notice
		// (F4.4) goes as a user message on the wire, and the Record keeps
		// it a notice. Its tag comes from where the notice is built.
		role = string(gotato.RoleUser)
	}
	if role != string(gotato.RoleUser) && role != string(gotato.RoleAssistant) {
		return chatMessage{}, fmt.Errorf("model: unsupported Message role %q", role)
	}
	for _, part := range message.Parts {
		if part.Kind != gotato.ContentKindText && part.Kind != gotato.ContentKindReasoning && (part.Text != "" || len(part.Data) > 0) {
			return chatMessage{}, fmt.Errorf("model: unsupported content kind %q", part.Kind)
		}
	}
	out := chatMessage{Role: role, Content: gotato.TextOf(message)}
	if role == string(gotato.RoleAssistant) && keepReasoning {
		out.ReasoningContent = gotato.ReasoningOf(message)
	}
	for _, call := range message.ToolCalls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return chatMessage{}, err
		}
		wire := chatToolCall{ID: string(call.ID), Type: "function", Function: chatFunctionCall{Name: name, Arguments: string(replayArguments(call.Arguments))}}
		// The call's extra_content goes back as it came. Another wire API's
		// artifact, such as a Responses item ID, is not an object.
		if isJSONObject(call.Signature) {
			wire.ExtraContent = json.RawMessage(call.Signature)
		}
		out.ToolCalls = append(out.ToolCalls, wire)
	}
	return out, nil
}

type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Tools         []chatTool         `json:"tools,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	MaxTokens     uint32             `json:"max_tokens,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}
type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// ReasoningContent is the model's thinking (DeepSeek), streamed as
	// delta.reasoning_content; a thinking model requires it back.
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}
type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
	// ExtraContent is provider data on a call, kept as it came: Gemini puts
	// the thought signature it requires back here (google.thought_signature).
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}
type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatDecoder reads the reply of one Chat Completions attempt.
type chatDecoder struct {
	nameMap map[string]string
	// calls are the Tool calls in the order they first appeared; byIndex
	// is the call each provider index names now, and last the call the
	// latest delta added to.
	calls    []*chatCall
	byIndex  map[int]*chatCall
	last     *chatCall
	queue    []gotato.ModelEvent
	finish   gotato.StopReason
	usage    gotato.Usage
	model    string
	finished bool
}

func newChatDecoder(names map[string]string) *chatDecoder {
	return &chatDecoder{nameMap: names, byIndex: make(map[int]*chatCall)}
}

type chatCall struct {
	id, name, arguments string
	// extra is the call's extra_content, from whichever delta carried it.
	extra json.RawMessage
}

type chatChunk struct {
	// Model is the model that answered, which a proxy with fallbacks may
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
		// CacheReadInputTokens and CacheCreationInputTokens are Anthropic's
		// names, passed through by a proxy in front of an Anthropic model.
		CacheReadInputTokens     uint64 `json:"cache_read_input_tokens,omitempty"`
		CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens,omitempty"`
	} `json:"usage"`
}

// Decode reads one chunk. A stream that ends with no finish_reason is handed
// back to the stream loop as an ordinary interruption, so its error has no
// "ended before completion" text: the end of a chat reply is not one event (a
// provider may omit [DONE] after a finish_reason), so there is no single
// missing event to name, as the other two APIs name theirs.
func (s *chatDecoder) Decode(event SSEEvent) ([]gotato.ModelEvent, bool, error) {
	err := s.decode(event)
	out := s.queue
	s.queue = nil
	return out, s.finished, err
}

func (s *chatDecoder) decode(event SSEEvent) error {
	if event.End {
		// A provider that omits [DONE] after a finish_reason still completed
		// the response.
		if s.finish != "" {
			s.finishStream()
		}
		return nil
	}
	data := event.Data
	if data == "[DONE]" {
		s.finishStream()
		return nil
	}
	if data == "" {
		return nil
	}
	var chunk chatChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return fmt.Errorf("model: invalid SSE chunk: %w", err)
	}
	if chunk.Error != nil {
		// An error is not an interruption: it is not sent again as a cut
		// stream is, and an overflow is reported as one.
		s.finished = true
		message := chunk.Error.Message
		if message == "" {
			message = chunk.Error.Type
		}
		return Failure{Message: "Chat Completions stream error: " + message, Code: errorCode(chunk.Error.Code), Type: chunk.Error.Type}
	}
	if err := s.processChunk(chunk); err != nil {
		s.finished = true
		return err
	}
	return nil
}

func (s *chatDecoder) processChunk(chunk chatChunk) error {
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		s.usage = gotato.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		cached := max(chunk.Usage.PromptCacheHitTokens, chunk.Usage.CacheReadInputTokens)
		if d := chunk.Usage.PromptTokensDetails; d != nil && d.CachedTokens > cached {
			cached = d.CachedTokens
		}
		s.usage.CacheReadTokens = min(cached, s.usage.InputTokens)
		s.usage.CacheWriteTokens = min(chunk.Usage.CacheCreationInputTokens, s.usage.InputTokens-s.usage.CacheReadTokens)
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
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			stop, ok := chatStopReason(*choice.FinishReason)
			if !ok {
				return protocolFailure("Chat Completions stream ended with the unknown finish_reason %q", *choice.FinishReason)
			}
			s.finish = stop
		}
	}
	return nil
}

// callFor returns the Tool call a delta adds to. The chat API names each
// call by its index, and sends its ID once, in its first delta. Gemini's chat
// API sends parallel calls whole, each with its own ID, but without an
// index, with a null one, or all at index 0. So a delta that names an ID
// other than the one its index holds starts a new call, and a delta without
// an index adds to the last call.
func (s *chatDecoder) callFor(index *int, id string) *chatCall {
	call := s.last
	if index != nil {
		call = s.byIndex[*index]
	}
	if call == nil || (id != "" && call.id != "" && id != call.id) {
		call = &chatCall{}
		s.calls = append(s.calls, call)
		if index != nil {
			s.byIndex[*index] = call
		}
	}
	s.last = call
	return call
}

// [LIMITATION] a tool call's arguments are emitted whole once complete, never as partial JSON, because the Session runs a call only on complete arguments.
func (s *chatDecoder) finishStream() {
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

// chatStopReason maps a finish_reason; ok is false for one it does not know
// (DeepSeek's insufficient_system_resource, say), which fails the stream
// rather than ending the Turn cleanly with a cut-off answer.
func chatStopReason(reason string) (stop gotato.StopReason, ok bool) {
	switch reason {
	case "tool_calls", "function_call":
		return gotato.StopReasonToolCalls, true
	case "length":
		return gotato.StopReasonMaxTokens, true
	case "content_filter":
		return gotato.StopReasonError, true
	case "stop", "":
		return gotato.StopReasonEndTurn, true
	}
	return "", false
}
