package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	gotato "github.com/jinhuang712/gotato"
)

// APIAnthropicMessages selects the Anthropic Messages API.
const APIAnthropicMessages = "anthropic-messages"

const (
	defaultAnthropicBaseURL   = "https://api.anthropic.com"
	anthropicVersion          = "2023-06-01"
	defaultAnthropicMaxTokens = 8192
	// maxAnthropicCacheBreakpoints is the provider's limit on cache_control
	// markers per request.
	maxAnthropicCacheBreakpoints = 4
)

func (c *Client) streamAnthropic(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	body, names, err := encodeAnthropicRequest(c.model, request)
	if err != nil {
		return nil, err
	}
	response, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	return &anthropicStream{
		response: response,
		reader:   bufio.NewReader(response.Body),
		nameMap:  names,
		blocks:   make(map[int]*anthropicBlockState),
	}, nil
}

// anthropicBlock is one content block. A map keeps provider blocks, such as a
// replayed thinking block, intact and lets a cache_control marker attach to
// any block kind.
type anthropicBlock = map[string]any

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   uint32             `json:"max_tokens"`
	System      []anthropicBlock   `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl anthropicBlock  `json:"cache_control,omitempty"`
}

// blockRef locates the last content block a gotato Message produced.
type blockRef struct{ message, block int }

func encodeAnthropicRequest(model string, request gotato.ModelRequest) ([]byte, map[string]string, error) {
	names := make(map[string]string, len(request.Tools))
	payload := anthropicRequest{
		Model:       model,
		MaxTokens:   defaultAnthropicMaxTokens,
		Temperature: request.Options.Temperature,
		Stream:      true,
		Messages:    make([]anthropicMessage, 0, len(request.Messages)),
	}
	if request.Options.MaxTokens != 0 {
		payload.MaxTokens = request.Options.MaxTokens
	}
	if request.SystemInstructions != "" {
		payload.System = []anthropicBlock{{"type": "text", "text": request.SystemInstructions}}
	}
	for _, spec := range request.Tools {
		name := gatewayFunctionName(spec.ID)
		if previous, exists := names[name]; exists && previous != spec.ID {
			return nil, nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, spec.ID)
		}
		names[name] = spec.ID
		schema := spec.InputSchema
		if len(schema) == 0 {
			schema = []byte(`{"type":"object"}`)
		}
		if !json.Valid(schema) {
			return nil, nil, fmt.Errorf("gateway: Tool %q has invalid InputSchema", spec.ID)
		}
		payload.Tools = append(payload.Tools, anthropicTool{Name: name, Description: spec.Description, InputSchema: json.RawMessage(schema)})
	}

	ends := make([]blockRef, len(request.Messages))
	for i, message := range request.Messages {
		role, blocks, err := anthropicBlocks(message, names)
		if err != nil {
			return nil, nil, err
		}
		if len(blocks) == 0 {
			ends[i] = blockRef{message: -1}
			continue
		}
		// The API alternates roles: consecutive Tool Results and user turns
		// join one user message, in order.
		if last := len(payload.Messages) - 1; last >= 0 && payload.Messages[last].Role == role {
			payload.Messages[last].Content = append(payload.Messages[last].Content, blocks...)
		} else {
			payload.Messages = append(payload.Messages, anthropicMessage{Role: role, Content: blocks})
		}
		last := len(payload.Messages) - 1
		ends[i] = blockRef{message: last, block: len(payload.Messages[last].Content) - 1}
	}
	applyAnthropicCacheBreakpoints(&payload, request.CacheBreakpoints, ends)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode request: %w", err)
	}
	return body, names, nil
}

func anthropicBlocks(message gotato.Message, names map[string]string) (string, []anthropicBlock, error) {
	switch message.Role {
	case gotato.RoleUser:
		var blocks []anthropicBlock
		for _, part := range message.Parts {
			switch part.Kind {
			case gotato.ContentText, gotato.ContentJSON:
				if part.Text != "" {
					blocks = append(blocks, anthropicBlock{"type": "text", "text": part.Text})
				}
			case gotato.ContentImage:
				if len(part.Data) == 0 {
					continue
				}
				blocks = append(blocks, anthropicBlock{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": part.MIMEType, "data": base64.StdEncoding.EncodeToString(part.Data),
				}})
			}
		}
		return "user", blocks, nil
	case gotato.RoleToolResult:
		content := toolResultText(message)
		callID := ""
		failed := false
		if message.ToolResult != nil {
			callID = string(message.ToolResult.CallID)
			if content == "" {
				content = message.ToolResult.SafeError
			}
			failed = message.ToolResult.Status != "" && message.ToolResult.Status != gotato.ToolResultOK
		}
		if content == "" {
			content = "(no tool output)"
		}
		block := anthropicBlock{"type": "tool_result", "tool_use_id": callID, "content": content}
		if failed {
			block["is_error"] = true
		}
		return "user", []anthropicBlock{block}, nil
	case gotato.RoleAssistant:
		var blocks []anthropicBlock
		for _, part := range message.Parts {
			switch part.Kind {
			case gotato.ContentReasoning:
				// Only a signed provider block can be replayed; plain
				// reasoning text stays in the transcript.
				if len(part.Signature) == 0 {
					continue
				}
				var block anthropicBlock
				if err := json.Unmarshal(part.Signature, &block); err != nil {
					return "", nil, fmt.Errorf("gateway: assistant reasoning artifact is invalid: %w", err)
				}
				if kind, _ := block["type"].(string); kind != "thinking" && kind != "redacted_thinking" {
					return "", nil, fmt.Errorf("gateway: assistant reasoning artifact has type %q, want thinking or redacted_thinking", kind)
				}
				blocks = append(blocks, block)
			case gotato.ContentText:
				if part.Text != "" {
					blocks = append(blocks, anthropicBlock{"type": "text", "text": part.Text})
				}
			case gotato.ContentImage, gotato.ContentJSON:
				if len(part.Data) > 0 || part.Text != "" {
					return "", nil, fmt.Errorf("gateway: assistant content kind %q is unsupported", part.Kind)
				}
			}
		}
		for _, call := range message.ToolCalls {
			name := gatewayFunctionName(call.ToolID)
			if previous, exists := names[name]; exists && previous != call.ToolID {
				return "", nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, call.ToolID)
			}
			names[name] = call.ToolID
			input := call.Arguments
			if len(input) == 0 {
				input = []byte(`{}`)
			}
			if !json.Valid(input) {
				return "", nil, fmt.Errorf("gateway: Tool Call %q has invalid arguments", call.ID)
			}
			blocks = append(blocks, anthropicBlock{"type": "tool_use", "id": string(call.ID), "name": name, "input": json.RawMessage(input)})
		}
		return "assistant", blocks, nil
	default:
		return "", nil, fmt.Errorf("gateway: unsupported Message role %q", message.Role)
	}
}

// applyAnthropicCacheBreakpoints places cache_control markers. When a request
// carries more breakpoints than the provider accepts, the last ones win: they
// cover the longest prefixes.
func applyAnthropicCacheBreakpoints(payload *anthropicRequest, breakpoints []gotato.CacheBreakpoint, ends []blockRef) {
	marker := func() anthropicBlock { return anthropicBlock{"type": "ephemeral"} }
	type target struct {
		anchor gotato.CacheAnchor
		ref    blockRef
	}
	var targets []target
	seen := make(map[target]bool)
	for _, point := range breakpoints {
		t := target{anchor: point.After, ref: blockRef{message: -1}}
		switch point.After {
		case gotato.CacheAfterSystem:
			if len(payload.System) == 0 {
				continue
			}
		case gotato.CacheAfterTools:
			if len(payload.Tools) == 0 {
				continue
			}
		case gotato.CacheAfterMessage:
			if point.Index < 0 || point.Index >= len(ends) || ends[point.Index].message < 0 {
				continue
			}
			t.ref = ends[point.Index]
		default:
			continue
		}
		if !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	if len(targets) > maxAnthropicCacheBreakpoints {
		targets = targets[len(targets)-maxAnthropicCacheBreakpoints:]
	}
	for _, t := range targets {
		switch t.anchor {
		case gotato.CacheAfterSystem:
			payload.System[len(payload.System)-1]["cache_control"] = marker()
		case gotato.CacheAfterTools:
			payload.Tools[len(payload.Tools)-1].CacheControl = marker()
		case gotato.CacheAfterMessage:
			payload.Messages[t.ref.message].Content[t.ref.block]["cache_control"] = marker()
		}
	}
}

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
	response  *http.Response
	reader    *bufio.Reader
	nameMap   map[string]string
	blocks    map[int]*anthropicBlockState
	queue     []gotato.ModelEvent
	usage     anthropicUsage
	stop      gotato.StopReason
	finished  bool
	closeOnce sync.Once
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
			if errors.Is(err, io.EOF) {
				return gotato.ModelEvent{}, fmt.Errorf("gateway: stream ended before message_stop: %w", io.ErrUnexpectedEOF)
			}
			return gotato.ModelEvent{}, err
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
	return event, nil
}

func (s *anthropicStream) process(event anthropicEvent) error {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			s.usage = event.Message.Usage
		}
	case "content_block_start":
		if event.ContentBlock == nil {
			return nil
		}
		block := &anthropicBlockState{kind: event.ContentBlock.Type, id: event.ContentBlock.ID, name: event.ContentBlock.Name,
			signature: event.ContentBlock.Signature, data: event.ContentBlock.Data}
		s.blocks[event.Index] = block
		switch block.kind {
		case "text":
			if event.ContentBlock.Text != "" {
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: event.ContentBlock.Text})
			}
		case "thinking":
			if event.ContentBlock.Thinking != "" {
				block.thinking.WriteString(event.ContentBlock.Thinking)
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDelta, Text: event.ContentBlock.Thinking})
			}
		}
	case "content_block_delta":
		block := s.blocks[event.Index]
		if block == nil || event.Delta == nil {
			return nil
		}
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: event.Delta.Text})
			}
		case "input_json_delta":
			block.input.WriteString(event.Delta.PartialJSON)
		case "thinking_delta":
			if event.Delta.Thinking != "" {
				block.thinking.WriteString(event.Delta.Thinking)
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDelta, Text: event.Delta.Thinking})
			}
		case "signature_delta":
			block.signature += event.Delta.Signature
		}
	case "content_block_stop":
		block := s.blocks[event.Index]
		if block == nil {
			return nil
		}
		delete(s.blocks, event.Index)
		return s.finishBlock(block)
	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			s.stop = anthropicStopReason(event.Delta.StopReason)
		}
		if event.Usage != nil {
			s.mergeUsage(*event.Usage)
		}
	case "message_stop":
		usage := s.gotatoUsage()
		if s.stop == "" {
			s.stop = gotato.StopEndTurn
		}
		s.queue = append(s.queue,
			gotato.ModelEvent{Kind: gotato.ModelUsage, Usage: usage},
			gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: s.stop, Usage: usage})
		s.finished = true
	case "error":
		message, kind := "stream error", ""
		if event.Error != nil {
			message, kind = event.Error.Message, event.Error.Type
		}
		return &Error{Retryable: kind == "overloaded_error" || kind == "api_error" || kind == "rate_limit_error", Message: message}
	}
	return nil
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
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelToolCall, ToolCall: &gotato.ToolCall{
			ID: gotato.ToolCallID(block.id), ToolID: name, Arguments: []byte(arguments),
		}})
	case "thinking":
		artifact, err := json.Marshal(anthropicBlock{"type": "thinking", "thinking": block.thinking.String(), "signature": block.signature})
		if err != nil {
			return err
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDone, ReasoningArtifact: artifact})
	case "redacted_thinking":
		artifact, err := json.Marshal(anthropicBlock{"type": "redacted_thinking", "data": block.data})
		if err != nil {
			return err
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDone, ReasoningArtifact: artifact})
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

func (s *anthropicStream) Close() error {
	s.closeOnce.Do(func() {
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	return nil
}

func anthropicStopReason(reason string) gotato.StopReason {
	switch reason {
	case "tool_use":
		return gotato.StopToolCalls
	case "max_tokens":
		return gotato.StopMaxTokens
	case "refusal":
		return gotato.StopError
	default:
		return gotato.StopEndTurn
	}
}
