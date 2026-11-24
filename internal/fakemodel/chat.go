package fakemodel

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
)

// OpenAI Chat Completions, from
// https://developers.openai.com/api/reference/resources/chat/subresources/completions
// (the chat.completion.chunk object and stream_options.include_usage). The
// reasoning rule is DeepSeek's, whose API the Chat Completions format carries
// to other providers: a thinking-mode reply's reasoning_content is passed
// back in the tool loop.

type chatWire struct{ openAIWire }

type chatRequest struct {
	Model               string `json:"model"`
	Stream              bool   `json:"stream"`
	MaxTokens           int    `json:"max_tokens"`
	MaxCompletionTokens int    `json:"max_completion_tokens"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ToolCallID       string          `json:"tool_call_id"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []struct {
		ID string `json:"id"`
	} `json:"tool_calls"`
}

func (chatWire) api() string { return APIChat }

func (chatWire) authorize(h http.Header) *apiError { return openAIAuthorize(h) }

func (chatWire) tooManyOutputTokens(p *parsed, limit int) *apiError {
	return openAIErr(http.StatusBadRequest,
		fmt.Sprintf("max_tokens is too large: %d. This model supports at most %d completion tokens, whereas you provided %d.", p.maxOutput, limit, p.maxOutput),
		"max_tokens", "invalid_value")
}

func (chatWire) tooLong(tokens, window int) *apiError {
	return openAIErr(http.StatusBadRequest,
		fmt.Sprintf("This model's maximum context length is %d tokens. However, your messages resulted in %d tokens. Please reduce the length of the messages.", window, tokens),
		"messages", "context_length_exceeded")
}

func chatContentText(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func (c chatWire) parse(_ *Server, body []byte, h http.Header) (*parsed, *apiError) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, c.invalid("We could not parse the JSON body of your request.")
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	if len(req.Messages) == 0 {
		return nil, openAIErr(http.StatusBadRequest, "Invalid 'messages': empty array. Expected an array with minimum length 1, but got an empty array instead.", "messages", "empty_array")
	}
	p := &parsed{model: req.Model, stream: req.Stream, maxOutput: max(req.MaxTokens, req.MaxCompletionTokens), header: h, raw: raw, request: &req, body: body}
	for _, t := range req.Tools {
		if t.Type == "function" {
			p.tools = append(p.tools, toolDef{name: t.Function.Name, schema: t.Function.Parameters})
		}
	}
	rd := &chatMessages{p: p, pending: map[string]bool{}}
	for i, m := range req.Messages {
		if e := rd.message(i, m); e != nil {
			return nil, e
		}
	}
	if len(rd.pending) > 0 {
		return nil, rd.unanswered("messages")
	}
	return p, nil
}

// chatMessages reads the messages of a Chat Completions request into events,
// and pairs each tool call with its tool message.
type chatMessages struct {
	p *parsed
	// pending holds the tool calls not yet answered, by id.
	pending   map[string]bool
	seenOther bool
}

// unanswered refuses the tool calls still pending, naming param.
func (r *chatMessages) unanswered(param string) *apiError {
	return openAIErr(http.StatusBadRequest, fmt.Sprintf("An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. The following tool_call_ids did not have response messages: %s", firstKey(r.pending)), param, "")
}

// message reads message i.
func (r *chatMessages) message(i int, m chatMessage) *apiError {
	if m.Role != "tool" && len(r.pending) > 0 {
		return r.unanswered("messages.[" + fmt.Sprint(i) + "].role")
	}
	switch m.Role {
	case "system", "developer":
		if r.seenOther {
			r.p.midSystem = append(r.p.midSystem, i)
		}
		return nil
	case "user":
		r.p.events = append(r.p.events, event{text: chatContentText(m.Content)})
	case "assistant":
		for _, tc := range m.ToolCalls {
			r.pending[tc.ID] = true
		}
	case "tool":
		if !r.pending[m.ToolCallID] {
			return openAIErr(http.StatusBadRequest, "Invalid parameter: messages with role 'tool' must be a response to a preceding message with 'tool_calls'.", "messages.["+fmt.Sprint(i)+"].role", "") //nolint:misspell // the provider spells it so
		}
		delete(r.pending, m.ToolCallID)
		r.p.events = append(r.p.events, event{toolResult: true, text: chatContentText(m.Content)})
	}
	r.seenOther = true
	return nil
}

func firstKey(m map[string]bool) string {
	best := ""
	for k := range m {
		if best == "" || k < best {
			best = k
		}
	}
	return best
}

// validateReasoning refuses a tool loop whose assistant message calls a tool
// and leaves out the reasoning_content it came with.
func (c chatWire) validateReasoning(p *parsed) *apiError {
	req := p.request.(*chatRequest)
	lastUser := -1
	for i, m := range req.Messages {
		if m.Role == "user" && !isRuntimeText(chatContentText(m.Content)) {
			lastUser = i
		}
	}
	for i := lastUser + 1; i < len(req.Messages); i++ {
		m := req.Messages[i]
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ReasoningContent == "" {
			return openAIErr(http.StatusBadRequest, "The `reasoning_content` in the thinking mode must be passed back to the API.", "", "invalid_request_error")
		}
	}
	return nil
}

func (chatWire) stream(s *sseWriter, p *parsed, pl *plan, _ *quirks) {
	req := p.request.(*chatRequest)
	c := &chatStream{s: s, p: p, pl: pl}
	c.chunk(map[string]any{"role": "assistant", "content": ""}, nil)
	for _, r := range chunks(pl.reasoning, reasoningChunkBytes) {
		c.chunk(map[string]any{"reasoning_content": r}, nil)
	}
	finish := "stop"
	if pl.call != nil {
		finish = "tool_calls"
	}
	if !c.content() {
		return
	}
	c.chunk(map[string]any{}, finish)
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		c.usage()
	}
	s.event("", "[DONE]")
}

// chatStream writes one reply as Chat Completions chunks.
type chatStream struct {
	s  *sseWriter
	p  *parsed
	pl *plan
}

// chunk writes one chunk with the delta and the finish reason.
func (c *chatStream) chunk(delta map[string]any, finish any) {
	c.s.event("", map[string]any{"id": "chatcmpl-" + c.pl.id, "object": "chat.completion.chunk", "created": 1, "model": c.p.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
}

// content writes the text or the tool call, and reports false when a
// mid-stream overflow ended the stream.
func (c *chatStream) content() bool {
	pl := c.pl
	if pl.call == nil {
		return pl.play(c.s, textChunks, pl.text, func(t string) {
			c.chunk(map[string]any{"content": t}, nil)
		}, c.overflow)
	}
	call := func(fn map[string]any, extra map[string]any) {
		tc := map[string]any{"index": 0, "function": fn}
		maps.Copy(tc, extra)
		c.chunk(map[string]any{"tool_calls": []any{tc}}, nil)
	}
	call(map[string]any{"name": pl.call.name, "arguments": ""}, map[string]any{"id": "call_" + pl.id, "type": "function"})
	return pl.play(c.s, argsChunks, pl.call.args, func(a string) {
		call(map[string]any{"arguments": a}, nil)
	}, c.overflow)
}

// overflow ends the stream with the error chunk Chat Completions sends when
// the context window runs out.
func (c *chatStream) overflow(string) {
	c.s.event("", map[string]any{"error": map[string]any{
		"message": "This model's maximum context length is exceeded. Please reduce the length of the messages.",
		"type":    "invalid_request_error", "param": "messages", "code": "context_length_exceeded"}})
}

// usage writes the usage chunk.
func (c *chatStream) usage() {
	pl := c.pl
	c.s.event("", map[string]any{"id": "chatcmpl-" + pl.id, "object": "chat.completion.chunk", "created": 1, "model": c.p.model,
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens": pl.input, "completion_tokens": pl.output, "total_tokens": pl.input + pl.output,
			"prompt_tokens_details":     map[string]any{"cached_tokens": pl.cached, "audio_tokens": 0},
			"completion_tokens_details": map[string]any{"reasoning_tokens": len(pl.reasoning) / bytesPerToken},
		}})
}
