package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
)

// APIAnthropicMessages selects the Anthropic Messages API.
const APIAnthropicMessages API = "anthropic-messages"

const (
	defaultAnthropicBaseURL   = "https://api.anthropic.com"
	anthropicVersion          = "2023-06-01"
	defaultAnthropicMaxTokens = 8192
	// maxAnthropicCacheBreakpoints is the provider's limit on cache_control
	// markers per request.
	maxAnthropicCacheBreakpoints = 4
)

// streamAnthropic sends the CacheKey in its header only. There is no body
// field for it: New refuses Config.CacheKeyField for this API, which caches by
// cache_control.
func (c *Client) streamAnthropic(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	body, names, err := encodeAnthropicRequest(c.model, request, c.rules)
	if err != nil {
		return nil, err
	}
	open := c.withCacheKeyHeader(c.do, request.CacheKey)
	response, err := open(ctx, body)
	if err != nil {
		return nil, err
	}
	return &anthropicStream{
		sseBody:  newSSEBody(response),
		nameMap:  names,
		blocks:   make(map[int]*anthropicBlockState),
		restart:  restarter{client: c, body: body, open: open},
		overflow: c.overflow,
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
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	Stream      bool               `json:"stream"`
}

// anthropicThinking is sent only to carry block_binding, on the models whose
// default is "adaptive" (thinkingPrefixModels), so it changes nothing else.
type anthropicThinking struct {
	Type         string                `json:"type"`
	BlockBinding anthropicBlockBinding `json:"block_binding"`
}

type anthropicBlockBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
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
	// DeferLoading keeps the tool out of the Model's context until a
	// tool_reference block names it (the native form of deferred tools).
	DeferLoading bool `json:"defer_loading,omitempty"`
}

// blockRef locates the last content block a gotato Message produced.
type blockRef struct{ message, block int }

func encodeAnthropicRequest(model string, request gotato.ModelRequest, rules providerRules) ([]byte, map[string]string, error) {
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	payload := anthropicRequest{
		Model:       model,
		MaxTokens:   defaultAnthropicMaxTokens,
		Temperature: request.Config.Temperature,
		Stream:      true,
		Messages:    make([]anthropicMessage, 0, len(request.Messages)),
	}
	if request.Config.MaxTokens != 0 {
		payload.MaxTokens = request.Config.MaxTokens
	}
	if rules.blockBinding {
		payload.Thinking = &anthropicThinking{Type: "adaptive", BlockBinding: anthropicBlockBinding{PrefixMismatchBehavior: "drop_block"}}
	}
	if request.SystemInstructions != "" {
		payload.System = []anthropicBlock{{"type": "text", "text": request.SystemInstructions}}
	}
	// Deferred tools, per
	// https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool :
	// every tool goes in the tools array on every request, and the ones with
	// defer_loading true reach the Model's context only when a tool_reference
	// block names them. Search is our own client-side tool, which answers with
	// tool_reference blocks ("Custom tool search implementation"). The deferred
	// tools come last, so the cache_control marker sits on a tool that is not
	// deferred, as the API requires.
	layout := newDeferredLayout(request, rules)
	ordered := orderedTools(layout, request)
	for _, entry := range ordered {
		spec := entry.spec
		name, err := assignFunctionName(names, spec.ID)
		if err != nil {
			return nil, nil, err
		}
		schema, err := toolSchema(spec, `{"type":"object"}`)
		if err != nil {
			return nil, nil, err
		}
		payload.Tools = append(payload.Tools, anthropicTool{Name: name, Description: spec.Description, InputSchema: schema, DeferLoading: entry.deferred})
	}

	ends := make([]blockRef, len(request.Messages))
	start := roundStart(request.Messages)
	for i, message := range request.Messages {
		role, blocks, err := anthropicBlocks(message, names, rules.keepsReasoning(i, start))
		if err != nil {
			return nil, nil, err
		}
		if refs := layout.references(message.ToolResult); len(refs) > 0 && len(blocks) == 1 {
			content := []anthropicBlock{{"type": "text", "text": blocks[0]["content"]}}
			for _, id := range refs {
				name, err := assignFunctionName(names, id)
				if err != nil {
					return nil, nil, err
				}
				content = append(content, anthropicBlock{"type": "tool_reference", "tool_name": name})
			}
			blocks[0]["content"] = content
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
	breakpoints := request.CacheBreakpoints
	if rules.roundReasoning {
		breakpoints = withRoundBreakpoint(breakpoints, request.Messages)
	}
	applyAnthropicCacheBreakpoints(&payload, breakpoints, ends, rules.anthropicCacheTTL)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode request: %w", err)
	}
	return body, names, nil
}

func anthropicBlocks(message gotato.Message, names map[string]string, keepReasoning bool) (string, []anthropicBlock, error) {
	switch message.Role {
	case gotato.RoleUser, gotato.RoleNotice:
		// A notice (G-F29) is tagged text in the adjacent user turn.
		var blocks []anthropicBlock
		for _, part := range message.Parts {
			switch part.Kind {
			case gotato.ContentKindText, gotato.ContentKindJSON:
				if part.Text != "" {
					blocks = append(blocks, anthropicBlock{"type": "text", "text": part.Text})
				}
			case gotato.ContentKindImage:
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
		content := toolResultOutput(message)
		callID := ""
		failed := false
		if message.ToolResult != nil {
			callID = string(message.ToolResult.CallID)
			failed = message.ToolResult.Status != "" && message.ToolResult.Status != gotato.ToolResultStatusOK
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
			case gotato.ContentKindReasoning:
				// Only a signed provider block can be replayed; plain
				// reasoning text stays in the transcript.
				if len(part.Signature) == 0 || !keepReasoning {
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
			case gotato.ContentKindText:
				if part.Text != "" {
					blocks = append(blocks, anthropicBlock{"type": "text", "text": part.Text})
				}
			case gotato.ContentKindImage, gotato.ContentKindJSON:
				if len(part.Data) > 0 || part.Text != "" {
					return "", nil, fmt.Errorf("gateway: assistant content kind %q is unsupported", part.Kind)
				}
			}
		}
		for _, call := range message.ToolCalls {
			name, err := assignFunctionName(names, call.ToolID)
			if err != nil {
				return "", nil, err
			}
			blocks = append(blocks, anthropicBlock{"type": "tool_use", "id": string(call.ID), "name": name, "input": json.RawMessage(replayArguments(call.Arguments))})
		}
		return "assistant", blocks, nil
	default:
		return "", nil, fmt.Errorf("gateway: unsupported Message role %q", message.Role)
	}
}

// withRoundBreakpoint adds a cache marker on the last user Message before the
// final Message, in prefix order. When a new Round leaves the last Round's
// reasoning out, the request differs from the last one at its first thinking
// block, so the entry cached at the end of the last request no longer
// matches, and the provider looks back only about 20 blocks from a marker for
// an earlier one. This marker sits where the last Round started, whose prefix
// every request since has shared: the first request of a Round reads the
// cache up to the Round before it, and a request in a tool loop up to its own
// Round's start.
func withRoundBreakpoint(breakpoints []gotato.CacheBreakpoint, messages []gotato.Message) []gotato.CacheBreakpoint {
	if len(messages) < 2 {
		return breakpoints
	}
	start := roundStart(messages[:len(messages)-1])
	if start < 0 {
		return breakpoints
	}
	marker := gotato.CacheBreakpoint{After: gotato.CacheAnchorAfterMessage, Index: start}
	out := make([]gotato.CacheBreakpoint, 0, len(breakpoints)+1)
	placed := false
	for _, point := range breakpoints {
		if !placed && point.After == gotato.CacheAnchorAfterMessage && point.Index >= start {
			if point.Index > start {
				out = append(out, marker)
			}
			placed = true
		}
		out = append(out, point)
	}
	if !placed {
		out = append(out, marker)
	}
	return out
}

// applyAnthropicCacheBreakpoints places cache_control markers, each with ttl
// when it is set. When a request carries more breakpoints than the provider
// accepts, the last ones win: they cover the longest prefixes.
func applyAnthropicCacheBreakpoints(payload *anthropicRequest, breakpoints []gotato.CacheBreakpoint, ends []blockRef, ttl string) {
	marker := func() anthropicBlock {
		if ttl != "" {
			return anthropicBlock{"type": "ephemeral", "ttl": ttl}
		}
		return anthropicBlock{"type": "ephemeral"}
	}
	type target struct {
		anchor gotato.CacheAnchor
		ref    blockRef
	}
	var targets []target
	seen := make(map[target]bool)
	for _, point := range breakpoints {
		t := target{anchor: point.After, ref: blockRef{message: -1}}
		switch point.After {
		case gotato.CacheAnchorAfterSystem:
			if len(payload.System) == 0 {
				continue
			}
		case gotato.CacheAnchorAfterTools:
			if len(payload.Tools) == 0 {
				continue
			}
		case gotato.CacheAnchorAfterMessage:
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
		case gotato.CacheAnchorAfterSystem:
			payload.System[len(payload.System)-1]["cache_control"] = marker()
		case gotato.CacheAnchorAfterTools:
			end := len(payload.Tools) - 1
			for end > 0 && payload.Tools[end].DeferLoading {
				end--
			}
			payload.Tools[end].CacheControl = marker()
		case gotato.CacheAnchorAfterMessage:
			payload.Messages[t.ref.message].Content[t.ref.block]["cache_control"] = marker()
		}
	}
}
