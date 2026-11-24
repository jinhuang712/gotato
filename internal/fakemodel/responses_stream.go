package fakemodel

import (
	"encoding/json"
	"slices"
)

func (responsesWire) stream(s *sseWriter, p *parsed, pl *plan, _ *quirks) {
	r := &responsesStream{s: s, p: p, pl: pl}
	r.ev("response.created", map[string]any{"response": r.response("in_progress", []any{})})
	r.ev("response.in_progress", map[string]any{"response": r.response("in_progress", []any{})})
	tag := pl.id
	if pl.reasoning != "" && pl.call != nil {
		tag += "r"
	}
	if pl.reasoning != "" {
		r.reasoning()
	}
	switch {
	case pl.call == nil:
		if !r.message() {
			return
		}
	case pl.call.search:
		r.searchCall(tag)
	default:
		if !r.functionCall(tag) {
			return
		}
	}
	completed := r.response("completed", r.output)
	completed["usage"] = r.usage()
	r.ev("response.completed", map[string]any{"response": completed})
}

// responsesStream writes one reply as the Responses API's stream events.
type responsesStream struct {
	s  *sseWriter
	p  *parsed
	pl *plan
	// seq numbers the events, index the output items.
	seq, index int
	output     []any
}

// ev writes one event of the kind, with its sequence number.
func (r *responsesStream) ev(kind string, fields map[string]any) {
	fields["type"] = kind
	fields["sequence_number"] = r.seq
	r.seq++
	r.s.event(kind, fields)
}

// response is the response object in the status, with no usage.
func (r *responsesStream) response(status string, output []any) map[string]any {
	return map[string]any{"id": "resp_" + r.pl.id, "object": "response", "created_at": 1, "status": status, "model": r.p.model, "output": output, "usage": nil, "error": nil, "incomplete_details": nil}
}

// usage is the usage of the reply.
func (r *responsesStream) usage() map[string]any {
	pl := r.pl
	return map[string]any{
		"input_tokens":          pl.input,
		"input_tokens_details":  map[string]any{"cached_tokens": pl.cached},
		"output_tokens":         pl.output,
		"output_tokens_details": map[string]any{"reasoning_tokens": len(pl.reasoning) / bytesPerToken},
		"total_tokens":          pl.input + pl.output,
	}
}

// reasoning writes the reasoning item, with its encrypted content when the
// request stores it or asks for it.
func (r *responsesStream) reasoning() {
	pl, p, index := r.pl, r.p, r.index
	id := "rs_" + pl.id
	item := map[string]any{"id": id, "type": "reasoning", "summary": []any{}}
	r.ev("response.output_item.added", map[string]any{"output_index": index, "item": item})
	r.ev("response.reasoning_summary_part.added", map[string]any{"item_id": id, "output_index": index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
	for _, c := range chunks(pl.reasoning, reasoningChunkBytes) {
		r.ev("response.reasoning_summary_text.delta", map[string]any{"item_id": id, "output_index": index, "summary_index": 0, "delta": c})
	}
	r.ev("response.reasoning_summary_text.done", map[string]any{"item_id": id, "output_index": index, "summary_index": 0, "text": pl.reasoning})
	r.ev("response.reasoning_summary_part.done", map[string]any{"item_id": id, "output_index": index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": pl.reasoning}})
	done := map[string]any{"id": id, "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": pl.reasoning}}}
	if p.store == nil || *p.store || slices.Contains(p.include, "reasoning.encrypted_content") {
		done["encrypted_content"] = encrypted(id)
	}
	r.ev("response.output_item.done", map[string]any{"output_index": index, "item": done})
	r.output = append(r.output, done)
	r.index++
}

// overflow ends the stream as the Responses API does when the context window
// runs out: an error event, or response.failed with the flag "failed".
func (r *responsesStream) overflow(flags string) {
	msg := "Your input exceeds the context window of this model. Please adjust your input and try again."
	if hasFlag(flags, "failed") {
		failed := r.response("failed", r.output)
		failed["error"] = map[string]any{"code": "context_length_exceeded", "message": msg}
		r.ev("response.failed", map[string]any{"response": failed})
		return
	}
	r.ev("error", map[string]any{"code": "context_length_exceeded", "message": msg, "param": "input"})
}

// message writes the message item of the text, and reports false when a
// mid-stream overflow ended the stream.
func (r *responsesStream) message() bool {
	pl, index := r.pl, r.index
	id := "msg_" + pl.id
	r.ev("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"id": id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}})
	r.ev("response.content_part.added", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	if !pl.play(r.s, textChunks, pl.text, func(c string) {
		r.ev("response.output_text.delta", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "delta": c, "logprobs": []any{}})
	}, r.overflow) {
		return false
	}
	r.ev("response.output_text.done", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "text": pl.text, "logprobs": []any{}})
	part := map[string]any{"type": "output_text", "text": pl.text, "annotations": []any{}}
	r.ev("response.content_part.done", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "part": part})
	item := map[string]any{"id": id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
	r.ev("response.output_item.done", map[string]any{"output_index": index, "item": item})
	r.output = append(r.output, item)
	return true
}

// searchCall writes the model's search: a tool_search_call item, whose
// arguments are an object and which streams no argument deltas.
func (r *responsesStream) searchCall(tag string) {
	pl, index := r.pl, r.index
	id := "tsc_" + tag
	callID := "call_" + pl.id
	var args map[string]any
	_ = json.Unmarshal([]byte(pl.call.args), &args)
	item := map[string]any{"id": id, "type": "tool_search_call", "status": "in_progress", "call_id": callID, "execution": "client", "arguments": args}
	r.ev("response.output_item.added", map[string]any{"output_index": index, "item": item})
	done := map[string]any{"id": id, "type": "tool_search_call", "status": "completed", "call_id": callID, "execution": "client", "arguments": args}
	r.ev("response.output_item.done", map[string]any{"output_index": index, "item": done})
	r.output = append(r.output, done)
}

// functionCall writes the function_call item, and reports false when a
// mid-stream overflow ended the stream.
func (r *responsesStream) functionCall(tag string) bool {
	pl, index := r.pl, r.index
	id := "fc_" + tag
	callID := "call_" + pl.id
	r.ev("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"id": id, "type": "function_call", "status": "in_progress", "call_id": callID, "name": pl.call.name, "arguments": ""}})
	if !pl.play(r.s, argsChunks, pl.call.args, func(c string) {
		r.ev("response.function_call_arguments.delta", map[string]any{"item_id": id, "output_index": index, "delta": c})
	}, r.overflow) {
		return false
	}
	r.ev("response.function_call_arguments.done", map[string]any{"item_id": id, "output_index": index, "name": pl.call.name, "arguments": pl.call.args})
	item := map[string]any{"id": id, "type": "function_call", "status": "completed", "call_id": callID, "name": pl.call.name, "arguments": pl.call.args}
	r.ev("response.output_item.done", map[string]any{"output_index": index, "item": item})
	r.output = append(r.output, item)
	return true
}
