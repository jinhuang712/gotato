package fakemodel

func (anthropicWire) stream(s *sseWriter, p *parsed, pl *plan, _ *quirks) {
	a := &anthropicStream{s: s, p: p, pl: pl}
	a.start()
	stop := "end_turn"
	if pl.call != nil {
		stop = "tool_use"
	}
	if a.content() {
		a.closeAndStop(stop)
	}
}

// anthropicStream writes one reply as Anthropic's stream events.
type anthropicStream struct {
	s     *sseWriter
	p     *parsed
	pl    *plan
	index int
}

// ev writes one event of the kind.
func (a *anthropicStream) ev(kind string, fields map[string]any) {
	fields["type"] = kind
	a.s.event(kind, fields)
}

// start writes message_start, and the thinking block when the reply reasons.
func (a *anthropicStream) start() {
	pl := a.pl
	usage := map[string]any{
		"input_tokens":                pl.input - pl.cached,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     pl.cached,
		"output_tokens":               1,
	}
	a.ev("message_start", map[string]any{"message": map[string]any{
		"id": "msg_" + pl.id, "type": "message", "role": "assistant", "model": a.p.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage,
	}})
	if pl.reasoning == "" {
		return
	}
	a.ev("content_block_start", map[string]any{"index": a.index, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
	for _, c := range chunks(pl.reasoning, reasoningChunkBytes) {
		a.ev("content_block_delta", map[string]any{"index": a.index, "delta": map[string]any{"type": "thinking_delta", "thinking": c}})
	}
	a.ev("content_block_delta", map[string]any{"index": a.index, "delta": map[string]any{"type": "signature_delta", "signature": anthropicSignature(requestPrefix(a.p.raw), pl.reasoning)}})
	a.ev("content_block_stop", map[string]any{"index": a.index})
	a.index++
}

// content writes the text block or the tool_use block, and reports false
// when a mid-stream overflow ended the stream.
func (a *anthropicStream) content() bool {
	pl := a.pl
	if pl.call == nil {
		a.ev("content_block_start", map[string]any{"index": a.index, "content_block": map[string]any{"type": "text", "text": ""}})
		a.ev("ping", map[string]any{})
		return pl.play(a.s, textChunks, pl.text, func(c string) {
			a.ev("content_block_delta", map[string]any{"index": a.index, "delta": map[string]any{"type": "text_delta", "text": c}})
		}, a.overflow)
	}
	a.ev("content_block_start", map[string]any{"index": a.index, "content_block": map[string]any{"type": "tool_use", "id": "toolu_" + pl.id, "name": pl.call.name, "input": map[string]any{}}})
	a.ev("content_block_delta", map[string]any{"index": a.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": ""}})
	return pl.play(a.s, argsChunks, pl.call.args, func(c string) {
		a.ev("content_block_delta", map[string]any{"index": a.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": c}})
	}, a.overflow)
}

// overflow ends the stream as Anthropic does when the context window runs
// out.
func (a *anthropicStream) overflow(string) {
	a.closeAndStop("model_context_window_exceeded")
}

// closeAndStop closes the open block and ends the message with the reason.
func (a *anthropicStream) closeAndStop(reason string) {
	a.ev("content_block_stop", map[string]any{"index": a.index})
	a.ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": a.pl.output}})
	a.ev("message_stop", map[string]any{})
}
