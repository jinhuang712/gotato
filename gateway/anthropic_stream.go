package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

type anthropicUsage struct {
	InputTokens              uint64 `json:"input_tokens"`
	OutputTokens             uint64 `json:"output_tokens"`
	CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Text      string `json:"text"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		Data      string `json:"data"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicBlockState struct {
	kind      string
	id        string
	name      string
	input     strings.Builder
	thinking  strings.Builder
	signature string
	data      string
}

type anthropicStream struct {
	sseBody
	nameMap  map[string]string
	blocks   map[int]*anthropicBlockState
	queue    []gotato.ModelEvent
	usage    anthropicUsage
	stop     gotato.StopReason
	model    string
	finished bool
	restart  restarter
	overflow overflowMatcher
}

func (s *anthropicStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.finished {
			return gotato.ModelEvent{}, io.EOF
		}
		data, err := readSSEEvent(ctx, s.reader)
		if err != nil {
			response, rerr := s.restart.recover(ctx, err)
			if rerr != nil {
				var gerr *Error
				if errors.Is(err, io.EOF) && errors.As(rerr, &gerr) {
					gerr.Message = "Anthropic stream ended before message_stop (" + gerr.Message + ")"
				}
				return gotato.ModelEvent{}, rerr
			}
			s.reopen(response)
			continue
		}
		if data == "" {
			continue
		}
		var event anthropicEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return gotato.ModelEvent{}, fmt.Errorf("gateway: invalid SSE event: %w", err)
		}
		if err := s.process(event); err != nil {
			return gotato.ModelEvent{}, err
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	s.restart.delivered = true
	return event, nil
}

// reopen continues on a re-issued response, discarding everything decoded
// from the interrupted one (nothing of it reached the caller).
func (s *anthropicStream) reopen(response *http.Response) {
	s.replace(response)
	s.blocks = make(map[int]*anthropicBlockState)
	s.queue = nil
	s.usage = anthropicUsage{}
	s.stop = ""
}

// process applies one decoded SSE event to the stream's state, queueing the
// ModelEvents it yields.
func (s *anthropicStream) process(event anthropicEvent) error {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			s.usage = event.Message.Usage
			s.model = event.Message.Model
		}
	case "content_block_start":
		s.startBlock(event)
	case "content_block_delta":
		s.blockDelta(event)
	case "content_block_stop":
		block := s.blocks[event.Index]
		if block == nil {
			return nil
		}
		delete(s.blocks, event.Index)
		return s.finishBlock(block)
	case "message_delta":
		return s.messageDelta(event)
	case "message_stop":
		s.stopMessage()
	case "error":
		return s.streamError(event)
	}
	return nil
}

// startBlock opens a content block; a text or thinking block may carry its
// first characters already.
func (s *anthropicStream) startBlock(event anthropicEvent) {
	if event.ContentBlock == nil {
		return
	}
	block := &anthropicBlockState{kind: event.ContentBlock.Type, id: event.ContentBlock.ID, name: event.ContentBlock.Name,
		signature: event.ContentBlock.Signature, data: event.ContentBlock.Data}
	s.blocks[event.Index] = block
	switch block.kind {
	case "text":
		if event.ContentBlock.Text != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: event.ContentBlock.Text})
		}
	case "thinking":
		if event.ContentBlock.Thinking != "" {
			block.thinking.WriteString(event.ContentBlock.Thinking)
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDelta, Text: event.ContentBlock.Thinking})
		}
	}
}

// blockDelta adds a delta to its open block: text and thinking are queued as
// they arrive, tool input and the signature are kept until the block stops.
func (s *anthropicStream) blockDelta(event anthropicEvent) {
	block := s.blocks[event.Index]
	if block == nil || event.Delta == nil {
		return
	}
	switch event.Delta.Type {
	case "text_delta":
		if event.Delta.Text != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: event.Delta.Text})
		}
	case "input_json_delta":
		block.input.WriteString(event.Delta.PartialJSON)
	case "thinking_delta":
		if event.Delta.Thinking != "" {
			block.thinking.WriteString(event.Delta.Thinking)
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDelta, Text: event.Delta.Thinking})
		}
	case "signature_delta":
		block.signature += event.Delta.Signature
	}
}

// messageDelta records the stop reason and the usage the message ends with.
func (s *anthropicStream) messageDelta(event anthropicEvent) error {
	if event.Delta != nil && event.Delta.StopReason == "model_context_window_exceeded" {
		// The window ran out during the reply. That is an overflow, not
		// an output limit: a continuation would ask the model again on
		// a full window. The text received stays as a cut-off reply.
		return &Error{Message: "the context window was exceeded during the reply", Overflow: true}
	}
	if event.Delta != nil && event.Delta.StopReason != "" {
		stop, ok := anthropicStopReason(event.Delta.StopReason)
		if !ok {
			return protocolFailure("Anthropic stream ended with the unknown stop_reason %q", event.Delta.StopReason)
		}
		s.stop = stop
	}
	if event.Usage != nil {
		s.mergeUsage(*event.Usage)
	}
	return nil
}

// stopMessage ends the stream with its Usage and Done events.
func (s *anthropicStream) stopMessage() {
	usage := s.gotatoUsage()
	if s.stop == "" {
		s.stop = gotato.StopReasonEndTurn
	}
	s.queue = append(s.queue,
		gotato.ModelEvent{Kind: gotato.ModelEventKindUsage, Usage: usage},
		gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: s.stop, Usage: usage, Model: s.model})
	s.finished = true
}

// streamError is the Error an "error" event stands for.
func (s *anthropicStream) streamError(event anthropicEvent) error {
	message, kind := "stream error", ""
	if event.Error != nil {
		message, kind = event.Error.Message, event.Error.Type
	}
	return &Error{
		retryable: transientStream(kind),
		Message:   message,
		Overflow:  kind == "invalid_request_error" && s.overflow.mentions(message) || s.overflow.mentions(kind),
	}
}

func (s *anthropicStream) finishBlock(block *anthropicBlockState) error {
	switch block.kind {
	case "tool_use":
		arguments := block.input.String()
		if arguments == "" {
			arguments = "{}"
		}
		name := block.name
		if original, ok := s.nameMap[name]; ok {
			name = original
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindToolCall, ToolCall: &gotato.ToolCall{
			ID: gotato.ToolCallID(block.id), ToolID: name, Arguments: []byte(arguments),
		}})
	case "thinking":
		artifact, err := json.Marshal(anthropicBlock{"type": "thinking", "thinking": block.thinking.String(), "signature": block.signature})
		if err != nil {
			return err
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: artifact})
	case "redacted_thinking":
		artifact, err := json.Marshal(anthropicBlock{"type": "redacted_thinking", "data": block.data})
		if err != nil {
			return err
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: artifact})
	}
	return nil
}

// mergeUsage folds the cumulative counts of a message_delta into the counts
// from message_start.
func (s *anthropicStream) mergeUsage(delta anthropicUsage) {
	if delta.OutputTokens != 0 {
		s.usage.OutputTokens = delta.OutputTokens
	}
	if delta.InputTokens != 0 {
		s.usage.InputTokens = delta.InputTokens
	}
	if delta.CacheReadInputTokens != 0 {
		s.usage.CacheReadInputTokens = delta.CacheReadInputTokens
	}
	if delta.CacheCreationInputTokens != 0 {
		s.usage.CacheCreationInputTokens = delta.CacheCreationInputTokens
	}
}

// gotatoUsage converts provider counts: the provider reports uncached input
// separately from cache reads and writes, and gotato.Usage counts all of them
// as input.
func (s *anthropicStream) gotatoUsage() gotato.Usage {
	input := s.usage.InputTokens + s.usage.CacheReadInputTokens + s.usage.CacheCreationInputTokens
	return gotato.Usage{
		InputTokens:      input,
		OutputTokens:     s.usage.OutputTokens,
		TotalTokens:      input + s.usage.OutputTokens,
		CacheReadTokens:  s.usage.CacheReadInputTokens,
		CacheWriteTokens: s.usage.CacheCreationInputTokens,
	}
}

// anthropicStopReason maps a stop_reason; ok is false for one it does not
// know, which fails the stream rather than ending the Turn cleanly.
func anthropicStopReason(reason string) (stop gotato.StopReason, ok bool) {
	switch reason {
	case "tool_use":
		return gotato.StopReasonToolCalls, true
	case "max_tokens":
		return gotato.StopReasonMaxTokens, true
	case "refusal":
		return gotato.StopReasonError, true
	case "end_turn", "stop_sequence":
		return gotato.StopReasonEndTurn, true
	}
	return "", false
}
