package fakegateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Anthropic Messages, from https://platform.claude.com/docs/en/api/messages,
// .../build-with-claude/streaming, .../api/errors,
// .../build-with-claude/mid-conversation-system-messages and
// .../build-with-claude/thinking.

// betaBinding is the beta header block_binding needs.
const betaBinding = "thinking-binding-controls-2026-08-01"

// midSystemModels are the models that take a system message in the middle of
// the conversation (the mid-conversation system messages page). Sonnet 5 is
// not one.
var midSystemModels = []string{"claude-fable-5-1", "claude-mythos-5-1", "claude-fable-5", "claude-mythos-5",
	"claude-opus-5-5", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5-5", "claude-haiku-5-5"}

func takesMidSystem(model string) bool {
	for _, m := range midSystemModels {
		if model == m {
			return true
		}
		// A dated snapshot: the name and eight digits.
		if rest, ok := strings.CutPrefix(model, m+"-"); ok && len(rest) == 8 && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

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
	Name        string         `json:"name"`
	InputSchema map[string]any `json:"input_schema"`
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

func (a anthropicWire) parse(_ *Gateway, body []byte, h http.Header) (*parsed, *apiError) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, a.invalid("Request body is not valid JSON: " + err.Error())
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	if req.MaxTokens == nil {
		return nil, a.invalid("max_tokens: Field required")
	}
	if len(req.Messages) == 0 {
		return nil, a.invalid("messages: at least one message is required")
	}
	if req.Messages[0].Role != "user" {
		return nil, a.invalid(`messages: first message must use the "user" role`)
	}
	if req.Thinking != nil && req.Thinking.BlockBinding != nil && !strings.Contains(h.Get("anthropic-beta"), betaBinding) {
		return nil, a.invalid("thinking.block_binding: Extra inputs are not permitted")
	}
	p := &parsed{model: req.Model, stream: req.Stream, maxOutput: *req.MaxTokens, header: h, raw: raw, request: &req, body: body}
	for _, t := range req.Tools {
		p.tools = append(p.tools, toolDef{name: t.Name, schema: t.InputSchema})
	}
	req.blocks = make([][]map[string]any, len(req.Messages))
	for i, m := range req.Messages {
		switch m.Role {
		case "user", "assistant", "system":
		default:
			return nil, a.invalid(fmt.Sprintf("messages.%d.role: Input should be 'user' or 'assistant'", i))
		}
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			return nil, a.invalid(fmt.Sprintf("messages.%d.content: %s", i, err))
		}
		req.blocks[i] = blocks
	}
	// Tool loop: each tool_use is answered by a tool_result in the next
	// message, and each tool_result answers a tool_use of the previous one.
	for i, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		var uses, results []string
		for _, b := range req.blocks[i] {
			switch b["type"] {
			case "tool_use":
				id, _ := b["id"].(string)
				uses = append(uses, id)
			case "tool_result":
				id, _ := b["tool_use_id"].(string)
				results = append(results, id)
			}
		}
		if m.Role == "assistant" && len(uses) > 0 {
			answered := map[string]bool{}
			if i+1 < len(req.Messages) {
				next := i + 1
				for next < len(req.Messages) && req.Messages[next].Role == "system" {
					next++
				}
				if next < len(req.Messages) {
					for _, b := range req.blocks[next] {
						if b["type"] == "tool_result" {
							id, _ := b["tool_use_id"].(string)
							answered[id] = true
						}
					}
				}
			}
			var missing []string
			for _, id := range uses {
				if !answered[id] {
					missing = append(missing, id)
				}
			}
			if len(missing) > 0 {
				return nil, a.invalid(fmt.Sprintf("messages.%d: `tool_use` ids were found without `tool_result` blocks immediately after: %s. Each `tool_use` block must have a corresponding `tool_result` block in the next message.", i, strings.Join(missing, ", ")))
			}
		}
		if m.Role == "user" && len(results) > 0 {
			prev := map[string]bool{}
			for j := i - 1; j >= 0; j-- {
				if req.Messages[j].Role == "system" {
					continue
				}
				if req.Messages[j].Role == "assistant" {
					for _, b := range req.blocks[j] {
						if b["type"] == "tool_use" {
							id, _ := b["id"].(string)
							prev[id] = true
						}
					}
				}
				break
			}
			for _, id := range results {
				if !prev[id] {
					return nil, a.invalid(fmt.Sprintf("messages.%d.content.0: unexpected `tool_use_id` found in `tool_result` blocks: %s. Each `tool_result` block must have a corresponding `tool_use` block in the previous message.", i, id))
				}
			}
		}
	}
	// System messages in the middle: placement and model.
	for i, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		if !takesMidSystem(req.Model) {
			return nil, a.invalid(fmt.Sprintf("messages.%d.role: Input should be 'user' or 'assistant'", i))
		}
		for _, b := range req.blocks[i] {
			if b["type"] != "text" {
				return nil, a.invalid(fmt.Sprintf("messages.%d.content: a system message holds text blocks only", i))
			}
		}
		afterUser := i > 0 && (req.Messages[i-1].Role == "user" || req.Messages[i-1].Role == "system")
		beforeAssistant := i == len(req.Messages)-1 || req.Messages[i+1].Role == "assistant" || req.Messages[i+1].Role == "system"
		if !afterUser || !beforeAssistant {
			return nil, a.invalid(fmt.Sprintf("messages.%d: a system message must immediately follow a user turn, and must be the last message or be followed by an assistant turn", i))
		}
		p.midSystem = append(p.midSystem, i)
	}
	for i, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		for _, b := range req.blocks[i] {
			switch b["type"] {
			case "text":
				if m.Role == "user" {
					text, _ := b["text"].(string)
					p.events = append(p.events, event{text: text})
				}
			case "tool_result":
				p.events = append(p.events, event{toolResult: true, text: toolResultText(b["content"])})
			}
		}
	}
	return p, nil
}

func toolResultText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if m, ok := b.(map[string]any); ok && m["type"] == "text" {
				s, _ := m["text"].(string)
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// stripCache removes cache_control, which marks a prefix and is not part of
// it.
func stripCache(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			if k != "cache_control" {
				out[k] = stripCache(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = stripCache(x)
		}
		return out
	}
	return v
}

// prefixHash identifies what precedes block j of message i: the system
// prompt, the tools, the messages before i, and the blocks before j.
func prefixHash(raw map[string]any, i, j int) string {
	messages, _ := raw["messages"].([]any)
	prefix := []any{stripCache(raw["system"]), stripCache(raw["tools"])}
	if i <= len(messages) {
		prefix = append(prefix, stripCache(messages[:i]))
	}
	within := []any{}
	if i < len(messages) {
		if m, ok := messages[i].(map[string]any); ok {
			if blocks, ok := m["content"].([]any); ok && j <= len(blocks) {
				within = blocks[:j]
			}
		}
	}
	prefix = append(prefix, stripCache(within))
	b, _ := json.Marshal(prefix)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// signature is the signature of a thinking block: bound to its prefix and to
// its text, and opaque to the client.
func anthropicSignature(prefix, thinking string) string {
	sum := sha256.Sum256([]byte(thinking))
	return base64.StdEncoding.EncodeToString([]byte("fakesig." + prefix + "." + hex.EncodeToString(sum[:8])))
}

// validateReasoning applies what Anthropic requires of thinking blocks in the
// latest assistant message (the errors and thinking pages): a final assistant
// message that calls a tool starts with a thinking block, the block is as it
// was received, and it was written under the same prefix.
func (a anthropicWire) validateReasoning(p *parsed) *apiError {
	req := p.request.(*anthropicRequest)
	last := -1
	for i, m := range req.Messages {
		if m.Role == "assistant" {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	blocks := req.blocks[last]
	hasUse := false
	for _, b := range blocks {
		if b["type"] == "tool_use" {
			hasUse = true
		}
	}
	dropMismatch := req.Thinking != nil && req.Thinking.BlockBinding != nil && req.Thinking.BlockBinding.PrefixMismatchBehavior == "drop_block"
	dropped := false
	for j, b := range blocks {
		kind, _ := b["type"].(string)
		if kind != "thinking" {
			continue
		}
		sig, _ := b["signature"].(string)
		text, _ := b["thinking"].(string)
		decoded, err := base64.StdEncoding.DecodeString(sig)
		parts := strings.Split(string(decoded), ".")
		if err != nil || len(parts) != 3 || parts[0] != "fakesig" {
			return a.invalid(fmt.Sprintf("messages.%d.content.%d: Invalid `signature` in `thinking` block", last, j))
		}
		sum := sha256.Sum256([]byte(text))
		if parts[2] != hex.EncodeToString(sum[:8]) {
			return a.invalid(fmt.Sprintf("messages.%d.content.%d: `thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response.", last, j))
		}
		if parts[1] != prefixHash(p.raw, last, j) {
			if dropMismatch {
				dropped = true
				continue
			}
			return a.invalid(fmt.Sprintf("messages.%d.content.%d: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\".", last, j))
		}
	}
	if hasUse && !dropped && (len(blocks) == 0 || (blocks[0]["type"] != "thinking" && blocks[0]["type"] != "redacted_thinking")) {
		return a.invalid(fmt.Sprintf("messages.%d.content.0.type: Expected `thinking` or `redacted_thinking`, but found `%v`. When `thinking` is enabled, a final `assistant` message must start with a thinking block (preceeding the lastmost set of `tool_use` and `tool_result` blocks). We recommend you include thinking blocks from previous turns. To avoid this requirement, disable `thinking`. Please consult our documentation at https://docs.claude.com/en/docs/build-with-claude/extended-thinking", last, firstType(blocks))) //nolint:misspell // the provider spells it so
	}
	return nil
}

func firstType(blocks []map[string]any) any {
	if len(blocks) == 0 {
		return "none"
	}
	return blocks[0]["type"]
}

func (anthropicWire) stream(s *sseWriter, p *parsed, pl *plan, q *quirks) {
	ev := func(kind string, fields map[string]any) {
		fields["type"] = kind
		s.event(kind, fields)
	}
	usage := map[string]any{
		"input_tokens":                pl.input - pl.cached,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     pl.cached,
		"output_tokens":               1,
	}
	ev("message_start", map[string]any{"message": map[string]any{
		"id": "msg_" + pl.id, "type": "message", "role": "assistant", "model": p.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage,
	}})
	index := 0
	// The prefix a thinking block is written under is the whole request.
	messages, _ := p.raw["messages"].([]any)
	if pl.reasoning != "" {
		ev("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
		for _, c := range chunks(pl.reasoning, 12) {
			ev("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": c}})
		}
		ev("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "signature_delta", "signature": anthropicSignature(prefixHash(p.raw, len(messages), 0), pl.reasoning)}})
		ev("content_block_stop", map[string]any{"index": index})
		index++
	}
	stop := "end_turn"
	mid := false
	closeAndStop := func(reason string) {
		ev("content_block_stop", map[string]any{"index": index})
		ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": pl.output}})
		ev("message_stop", map[string]any{})
	}
	if pl.call == nil {
		ev("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "text", "text": ""}})
		ev("ping", map[string]any{})
		for n, c := range chunks(pl.text, 5) {
			ev("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": c}})
			if n == 0 {
				if _, mid = pl.midOverflow(); mid {
					pl.outcome("overflow-midstream")
					closeAndStop("model_context_window_exceeded")
					return
				}
				if pl.cutNow("cut-after-text") {
					s.cut(pl)
				}
			}
		}
	} else {
		stop = "tool_use"
		ev("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "tool_use", "id": "toolu_" + pl.id, "name": pl.call.name, "input": map[string]any{}}})
		ev("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": ""}})
		for n, c := range chunks(pl.call.args, 6) {
			ev("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": c}})
			if n == 0 {
				if _, mid = pl.midOverflow(); mid {
					pl.outcome("overflow-midstream")
					closeAndStop("model_context_window_exceeded")
					return
				}
				if pl.cutNow("cut-in-args") {
					s.cut(pl)
				}
			}
		}
	}
	closeAndStop(stop)
}
