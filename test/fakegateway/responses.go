package fakegateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// OpenAI Responses, from https://developers.openai.com/api/reference/resources/responses
// and the streaming guide. The documentation names the events and the usage
// fields; the error bodies and the item-pairing errors are as the API returns
// them (see the report of the change that added this package).

type responsesWire struct{}

type responsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Stream          bool            `json:"stream"`
	Store           *bool           `json:"store"`
	Include         []string        `json:"include"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	Tools           []struct {
		Type       string         `json:"type"`
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	} `json:"tools"`

	items []map[string]any
}

func (responsesWire) api() string { return APIResponses }

func openAIErr(status int, message, param, code string) *apiError {
	e := map[string]any{"message": message, "type": "invalid_request_error", "param": nil, "code": nil}
	if param != "" {
		e["param"] = param
	}
	if code != "" {
		e["code"] = code
	}
	return &apiError{status: status, body: map[string]any{"error": e}}
}

func (responsesWire) invalid(message string) *apiError {
	return openAIErr(http.StatusBadRequest, message, "", "")
}

func openAIAuthorize(h http.Header) *apiError {
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer ") || strings.TrimSpace(strings.TrimPrefix(h.Get("Authorization"), "Bearer ")) == "" {
		e := openAIErr(http.StatusUnauthorized, "You didn't provide an API key. You need to provide your API key in an Authorization header using Bearer auth (i.e. Authorization: Bearer YOUR_KEY).", "", "")
		return e
	}
	return nil
}

func (responsesWire) authorize(h http.Header) *apiError { return openAIAuthorize(h) }

func (responsesWire) tooManyOutputTokens(p *parsed, limit int) *apiError {
	return openAIErr(http.StatusBadRequest,
		fmt.Sprintf("Invalid 'max_output_tokens': integer above maximum value. Expected a value <= %d, but got %d instead.", limit, p.maxOutput),
		"max_output_tokens", "integer_above_max_value")
}

func (responsesWire) tooLong(_, _ int) *apiError {
	return openAIErr(http.StatusBadRequest, "Your input exceeds the context window of this model. Please adjust your input and try again.", "input", "context_length_exceeded")
}

func (r responsesWire) parse(_ *Gateway, body []byte, h http.Header) (*parsed, *apiError) {
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, r.invalid("We could not parse the JSON body of your request.")
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	p := &parsed{model: req.Model, stream: req.Stream, maxOutput: req.MaxOutputTokens, store: req.Store, include: req.Include, header: h, raw: raw, request: &req, body: body}
	for _, t := range req.Tools {
		if t.Type == "function" && t.Name != "" {
			p.tools = append(p.tools, toolDef{name: t.Name, schema: t.Parameters})
		}
	}
	var text string
	if json.Unmarshal(req.Input, &text) == nil {
		p.events = append(p.events, event{text: text})
		return p, nil
	}
	if err := json.Unmarshal(req.Input, &req.items); err != nil {
		return nil, openAIErr(http.StatusBadRequest, "Invalid type for 'input': expected one of a string or array of input items.", "input", "invalid_type")
	}
	calls := map[string]bool{}
	outputs := map[string]bool{}
	seenOther := false
	for i, it := range req.items {
		kind, _ := it["type"].(string)
		role, _ := it["role"].(string)
		if kind == "" && role != "" {
			kind = "message"
		}
		switch kind {
		case "message":
			switch role {
			case "developer", "system":
				if seenOther {
					p.midSystem = append(p.midSystem, i)
				}
				continue
			case "user":
				p.events = append(p.events, event{text: responsesText(it["content"])})
			}
		case "function_call":
			id, _ := it["call_id"].(string)
			calls[id] = true
		case "function_call_output":
			id, _ := it["call_id"].(string)
			outputs[id] = true
			if !calls[id] {
				return nil, openAIErr(http.StatusBadRequest, "No tool call found for function call output with call_id "+id+".", "input", "")
			}
			out, _ := it["output"].(string)
			p.events = append(p.events, event{toolResult: true, text: out})
		}
		seenOther = true
	}
	for id := range calls {
		if !outputs[id] {
			return nil, openAIErr(http.StatusBadRequest, "No tool output found for function call "+id+".", "input", "")
		}
	}
	return p, nil
}

func responsesText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// encrypted is the encrypted_content of a reasoning item: opaque to the
// client, and valid only for the item it came with.
func encrypted(id string) string {
	sum := sha256.Sum256([]byte("fake-enc/" + id))
	return "fake-enc:" + hex.EncodeToString(sum[:12])
}

// validateReasoning applies the pairing the API requires of reasoning items:
// a function call that came with a reasoning item is sent with it, a
// reasoning item is followed by the output it came with, and its encrypted
// content is as it was received.
func (r responsesWire) validateReasoning(p *parsed) *apiError {
	items := p.request.(*responsesRequest).items
	seen := map[string]bool{}
	for i, it := range items {
		kind, _ := it["type"].(string)
		id, _ := it["id"].(string)
		switch kind {
		case "reasoning":
			if enc, ok := it["encrypted_content"].(string); ok && enc != encrypted(id) {
				return openAIErr(http.StatusBadRequest, fmt.Sprintf("The encrypted content for item %s could not be verified. Reason: Encrypted content could not be decrypted or parsed.", id), "", "invalid_encrypted_content")
			}
			next := ""
			if i+1 < len(items) {
				next, _ = items[i+1]["type"].(string)
				if role, _ := items[i+1]["role"].(string); next == "" && role == "assistant" {
					next = "message"
				}
			}
			if next != "function_call" && next != "message" {
				return openAIErr(http.StatusBadRequest, fmt.Sprintf("Item '%s' of type 'reasoning' was provided without its required following item.", id), "input", "")
			}
			seen[id] = true
		case "function_call":
			if rest, ok := strings.CutPrefix(id, "fc_"); ok && strings.HasSuffix(id, "r") {
				want := "rs_" + strings.TrimSuffix(rest, "r")
				if !seen[want] {
					return openAIErr(http.StatusBadRequest, fmt.Sprintf("Item '%s' of type 'function_call' was provided without its required 'reasoning' item: '%s'.", id, want), "input", "")
				}
			}
		}
	}
	return nil
}

func (responsesWire) stream(s *sseWriter, p *parsed, pl *plan, q *quirks) {
	seq := 0
	ev := func(kind string, fields map[string]any) {
		fields["type"] = kind
		fields["sequence_number"] = seq
		seq++
		s.event(kind, fields)
	}
	respID := "resp_" + pl.id
	response := func(status string, output []any, withUsage bool) map[string]any {
		r := map[string]any{"id": respID, "object": "response", "created_at": 1, "status": status, "model": p.model, "output": output, "usage": nil, "error": nil, "incomplete_details": nil}
		if withUsage {
			r["usage"] = map[string]any{
				"input_tokens":          pl.input,
				"input_tokens_details":  map[string]any{"cached_tokens": pl.cached},
				"output_tokens":         pl.output,
				"output_tokens_details": map[string]any{"reasoning_tokens": len(pl.reasoning) / 4},
				"total_tokens":          pl.input + pl.output,
			}
		}
		return r
	}
	ev("response.created", map[string]any{"response": response("in_progress", []any{}, false)})
	ev("response.in_progress", map[string]any{"response": response("in_progress", []any{}, false)})
	var output []any
	index := 0
	tag := pl.id
	withReasoning := pl.reasoning != ""
	if withReasoning && pl.call != nil {
		tag += "r"
	}
	if withReasoning {
		id := "rs_" + pl.id
		item := map[string]any{"id": id, "type": "reasoning", "summary": []any{}}
		ev("response.output_item.added", map[string]any{"output_index": index, "item": item})
		done := map[string]any{"id": id, "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": pl.reasoning}}}
		if p.store == nil || *p.store || slices.Contains(p.include, "reasoning.encrypted_content") {
			done["encrypted_content"] = encrypted(id)
		}
		ev("response.output_item.done", map[string]any{"output_index": index, "item": done})
		output = append(output, done)
		index++
	}
	mid := func() bool {
		flags, ok := pl.midOverflow()
		if !ok {
			return false
		}
		pl.outcome("overflow-midstream")
		msg := "Your input exceeds the context window of this model. Please adjust your input and try again."
		if hasFlag(flags, "failed") {
			failed := response("failed", output, false)
			failed["error"] = map[string]any{"code": "context_length_exceeded", "message": msg}
			ev("response.failed", map[string]any{"response": failed})
		} else {
			ev("error", map[string]any{"code": "context_length_exceeded", "message": msg, "param": "input"})
		}
		return true
	}
	if pl.call == nil {
		id := "msg_" + pl.id
		ev("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}})
		ev("response.content_part.added", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
		for n, c := range chunks(pl.text, 5) {
			ev("response.output_text.delta", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "delta": c, "logprobs": []any{}})
			if n == 0 {
				if mid() {
					return
				}
				if pl.cutNow("cut-after-text") {
					s.cut(pl)
				}
			}
		}
		ev("response.output_text.done", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "text": pl.text, "logprobs": []any{}})
		part := map[string]any{"type": "output_text", "text": pl.text, "annotations": []any{}}
		ev("response.content_part.done", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "part": part})
		item := map[string]any{"id": id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
		ev("response.output_item.done", map[string]any{"output_index": index, "item": item})
		output = append(output, item)
	} else {
		id := "fc_" + tag
		callID := "call_" + pl.id
		ev("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"id": id, "type": "function_call", "status": "in_progress", "call_id": callID, "name": pl.call.name, "arguments": ""}})
		for n, c := range chunks(pl.call.args, 6) {
			ev("response.function_call_arguments.delta", map[string]any{"item_id": id, "output_index": index, "delta": c})
			if n == 0 {
				if mid() {
					return
				}
				if pl.cutNow("cut-in-args") {
					s.cut(pl)
				}
			}
		}
		ev("response.function_call_arguments.done", map[string]any{"item_id": id, "output_index": index, "name": pl.call.name, "arguments": pl.call.args})
		item := map[string]any{"id": id, "type": "function_call", "status": "completed", "call_id": callID, "name": pl.call.name, "arguments": pl.call.args}
		ev("response.output_item.done", map[string]any{"output_index": index, "item": item})
		output = append(output, item)
	}
	ev("response.completed", map[string]any{"response": response("completed", output, true)})
}
