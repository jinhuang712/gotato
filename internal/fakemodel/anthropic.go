package fakemodel

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

func (a anthropicWire) parse(_ *Server, body []byte, h http.Header) (*parsed, *apiError) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, a.invalid("Request body is not valid JSON: " + err.Error())
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	if e := a.checkRequest(&req, h); e != nil {
		return nil, e
	}
	p := &parsed{model: req.Model, stream: req.Stream, maxOutput: *req.MaxTokens, header: h, raw: raw, request: &req, body: body}
	p.loaded = map[string]bool{}
	deferredTools, e := a.readTools(p, req.Tools)
	if e != nil {
		return nil, e
	}
	if e := a.decodeBlocks(&req); e != nil {
		return nil, e
	}
	if e := a.checkToolLoop(&req); e != nil {
		return nil, e
	}
	if e := a.checkMidSystem(p, &req); e != nil {
		return nil, e
	}
	r := &anthropicEvents{p: p, deferred: deferredTools, useNames: map[string]string{}}
	if e := r.readEvents(&req); e != nil {
		return nil, e
	}
	return p, nil
}

// checkRequest refuses a request that lacks a required field, opens with a
// message that is not the user's, or asks for block_binding without its beta.
func (a anthropicWire) checkRequest(req *anthropicRequest, h http.Header) *apiError {
	if req.MaxTokens == nil {
		return a.invalid("max_tokens: Field required")
	}
	if len(req.Messages) == 0 {
		return a.invalid("messages: at least one message is required")
	}
	if req.Messages[0].Role != "user" {
		return a.invalid(`messages: first message must use the "user" role`)
	}
	if req.Thinking != nil && req.Thinking.BlockBinding != nil && !strings.Contains(h.Get("anthropic-beta"), betaBinding) {
		return a.invalid("thinking.block_binding: Extra inputs are not permitted")
	}
	return nil
}

// readTools records the request's tools and its search tool, and returns the
// deferred ones by name. It refuses a deferred tool with cache_control, and a
// list whose tools are all deferred.
func (a anthropicWire) readTools(p *parsed, tools []anthropicTool) (map[string]bool, *apiError) {
	deferredTools, plainTools := map[string]bool{}, 0
	for i, t := range tools {
		p.tools = append(p.tools, toolDef{name: t.Name, schema: t.InputSchema, deferred: t.DeferLoading})
		if t.DeferLoading {
			deferredTools[t.Name] = true
			if t.CacheControl != nil {
				return nil, a.invalid(fmt.Sprintf("tools.%d.cache_control: a tool with defer_loading true cannot take cache_control", i))
			}
			continue
		}
		plainTools++
		if t.Name == "search_tools" || strings.HasSuffix(t.Name, "_search_tools") {
			p.searchName = t.Name
		}
	}
	if len(deferredTools) > 0 && plainTools == 0 {
		return nil, a.invalid("tools: at least one tool must be non-deferred (defer_loading true is set on every tool)")
	}
	return deferredTools, nil
}

// decodeBlocks checks each message's role and decodes its content blocks.
func (a anthropicWire) decodeBlocks(req *anthropicRequest) *apiError {
	req.blocks = make([][]map[string]any, len(req.Messages))
	for i, m := range req.Messages {
		switch m.Role {
		case "user", "assistant", "system":
		default:
			return a.invalid(fmt.Sprintf("messages.%d.role: Input should be 'user' or 'assistant'", i))
		}
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			return a.invalid(fmt.Sprintf("messages.%d.content: %s", i, err))
		}
		req.blocks[i] = blocks
	}
	return nil
}

// blockIDs returns the field of each block of the kind, "" where it is
// missing.
func blockIDs(blocks []map[string]any, kind, field string) []string {
	var ids []string
	for _, b := range blocks {
		if b["type"] == kind {
			id, _ := b[field].(string)
			ids = append(ids, id)
		}
	}
	return ids
}

// checkToolLoop refuses a malformed tool loop.
func (a anthropicWire) checkToolLoop(req *anthropicRequest) *apiError {
	// Tool loop: each tool_use is answered by a tool_result in the next
	// message, and each tool_result answers a tool_use of the previous one.
	for i, m := range req.Messages {
		var e *apiError
		switch m.Role {
		case "assistant":
			e = a.checkUsesAnswered(req, i)
		case "user":
			e = a.checkResultsAnswer(req, i)
		}
		if e != nil {
			return e
		}
	}
	return nil
}

// checkUsesAnswered refuses an assistant message i whose tool_use blocks have
// no tool_result in the next message that is not a system message.
func (a anthropicWire) checkUsesAnswered(req *anthropicRequest, i int) *apiError {
	answered := map[string]bool{}
	next := i + 1
	for next < len(req.Messages) && req.Messages[next].Role == "system" {
		next++
	}
	if next < len(req.Messages) {
		for _, id := range blockIDs(req.blocks[next], "tool_result", "tool_use_id") {
			answered[id] = true
		}
	}
	var missing []string
	for _, id := range blockIDs(req.blocks[i], "tool_use", "id") {
		if !answered[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return a.invalid(fmt.Sprintf("messages.%d: `tool_use` ids were found without `tool_result` blocks immediately after: %s. Each `tool_use` block must have a corresponding `tool_result` block in the next message.", i, strings.Join(missing, ", ")))
	}
	return nil
}

// checkResultsAnswer refuses a user message i with a tool_result that answers
// no tool_use of the previous message that is not a system message.
func (a anthropicWire) checkResultsAnswer(req *anthropicRequest, i int) *apiError {
	prev := map[string]bool{}
	j := i - 1
	for j >= 0 && req.Messages[j].Role == "system" {
		j--
	}
	if j >= 0 && req.Messages[j].Role == "assistant" {
		for _, id := range blockIDs(req.blocks[j], "tool_use", "id") {
			prev[id] = true
		}
	}
	for _, id := range blockIDs(req.blocks[i], "tool_result", "tool_use_id") {
		if !prev[id] {
			return a.invalid(fmt.Sprintf("messages.%d.content.0: unexpected `tool_use_id` found in `tool_result` blocks: %s. Each `tool_result` block must have a corresponding `tool_use` block in the previous message.", i, id))
		}
	}
	return nil
}

// checkMidSystem refuses a system message in the middle that the model does
// not take or that breaks the placement rules, and records the position of
// each one.
func (a anthropicWire) checkMidSystem(p *parsed, req *anthropicRequest) *apiError {
	// System messages in the middle: placement and model.
	for i, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		if e := a.checkSystemAt(req, i); e != nil {
			return e
		}
		p.midSystem = append(p.midSystem, i)
	}
	return nil
}

// checkSystemAt refuses the system message i when the model does not take
// one, it holds a block that is not text, or it is out of place.
func (a anthropicWire) checkSystemAt(req *anthropicRequest, i int) *apiError {
	if !takesMidSystem(req.Model) {
		return a.invalid(fmt.Sprintf("messages.%d.role: Input should be 'user' or 'assistant'", i))
	}
	for _, b := range req.blocks[i] {
		if b["type"] != "text" {
			return a.invalid(fmt.Sprintf("messages.%d.content: a system message holds text blocks only", i))
		}
	}
	afterUser := i > 0 && (req.Messages[i-1].Role == "user" || req.Messages[i-1].Role == "system")
	beforeAssistant := i == len(req.Messages)-1 || req.Messages[i+1].Role == "assistant" || req.Messages[i+1].Role == "system"
	if !afterUser || !beforeAssistant {
		return a.invalid(fmt.Sprintf("messages.%d: a system message must immediately follow a user turn, and must be the last message or be followed by an assistant turn", i))
	}
	return nil
}

// anthropicEvents reads the conversation's events from the blocks of an
// Anthropic request.
type anthropicEvents struct {
	p        *parsed
	deferred map[string]bool
	// useNames holds the tool name of each tool_use, by id.
	useNames map[string]string
}

// readEvents reads the events of every message that is not a system message.
func (r *anthropicEvents) readEvents(req *anthropicRequest) *apiError {
	for i, m := range req.Messages {
		if m.Role == "system" {
			continue
		}
		for j, b := range req.blocks[i] {
			if e := r.block(i, j, m.Role, b); e != nil {
				return e
			}
		}
	}
	return nil
}

// block reads block j of message i.
func (r *anthropicEvents) block(i, j int, role string, b map[string]any) *apiError {
	switch b["type"] {
	case "text":
		if role == "user" {
			text, _ := b["text"].(string)
			r.p.events = append(r.p.events, event{text: text})
		}
	case "tool_use":
		id, _ := b["id"].(string)
		name, _ := b["name"].(string)
		r.useNames[id] = name
	case "tool_reference":
		return anthropicWire{}.invalid(fmt.Sprintf("messages.%d.content.%d: a tool_reference block belongs in the content of a tool_result", i, j))
	case "tool_result":
		return r.toolResult(i, j, b)
	}
	return nil
}

// toolResult reads the tool_result block j of message i, and loads the
// deferred tools its tool_reference blocks name.
func (r *anthropicEvents) toolResult(i, j int, b map[string]any) *apiError {
	id, _ := b["tool_use_id"].(string)
	ev := event{toolResult: true, text: toolResultText(b["content"]), search: r.p.searchName != "" && r.useNames[id] == r.p.searchName}
	content, _ := b["content"].([]any)
	for _, c := range content {
		ref, _ := c.(map[string]any)
		if ref["type"] != "tool_reference" {
			continue
		}
		name, _ := ref["tool_name"].(string)
		if !r.deferred[name] {
			return anthropicWire{}.invalid(fmt.Sprintf("messages.%d.content.%d: tool reference '%s' is not a deferred tool in the tools list", i, j, name))
		}
		r.p.loaded[name] = true
		ev.search = true
	}
	r.p.events = append(r.p.events, ev)
	return nil
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
	last := req.lastAssistant()
	if last < 0 {
		return nil
	}
	blocks := req.blocks[last]
	dropMismatch := req.dropsMismatch()
	dropped := false
	for j, b := range blocks {
		if b["type"] != "thinking" {
			continue
		}
		mismatch, e := a.checkThinking(p.raw, last, j, b)
		if e != nil {
			return e
		}
		if !mismatch {
			continue
		}
		if !dropMismatch {
			return a.invalid(fmt.Sprintf("messages.%d.content.%d: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\".", last, j))
		}
		dropped = true
	}
	if len(blockIDs(blocks, "tool_use", "id")) > 0 && !dropped && !startsWithThinking(blocks) {
		return a.invalid(fmt.Sprintf("messages.%d.content.0.type: Expected `thinking` or `redacted_thinking`, but found `%v`. When `thinking` is enabled, a final `assistant` message must start with a thinking block (preceding the lastmost set of `tool_use` and `tool_result` blocks). We recommend you include thinking blocks from previous turns. To avoid this requirement, disable `thinking`. Please consult our documentation at https://docs.claude.com/en/docs/build-with-claude/extended-thinking", last, firstType(blocks))) //nolint:misspell // the provider spells it so
	}
	return nil
}

// lastAssistant returns the index of the last assistant message, or -1.
func (req *anthropicRequest) lastAssistant() int {
	last := -1
	for i, m := range req.Messages {
		if m.Role == "assistant" {
			last = i
		}
	}
	return last
}

// dropsMismatch reports whether the request asks to drop a thinking block
// bound to another prefix (block_binding with drop_block).
func (req *anthropicRequest) dropsMismatch() bool {
	return req.Thinking != nil && req.Thinking.BlockBinding != nil && req.Thinking.BlockBinding.PrefixMismatchBehavior == "drop_block"
}

// checkThinking refuses thinking block j of message i when its signature is
// not the fake's or its text is not as signed, and reports whether it was
// written under another prefix.
func (a anthropicWire) checkThinking(raw map[string]any, i, j int, b map[string]any) (bool, *apiError) {
	sig, _ := b["signature"].(string)
	text, _ := b["thinking"].(string)
	decoded, err := base64.StdEncoding.DecodeString(sig)
	parts := strings.Split(string(decoded), ".")
	if err != nil || len(parts) != 3 || parts[0] != "fakesig" {
		return false, a.invalid(fmt.Sprintf("messages.%d.content.%d: Invalid `signature` in `thinking` block", i, j))
	}
	sum := sha256.Sum256([]byte(text))
	if parts[2] != hex.EncodeToString(sum[:8]) {
		return false, a.invalid(fmt.Sprintf("messages.%d.content.%d: `thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response.", i, j))
	}
	return parts[1] != prefixHash(raw, i, j), nil
}

// startsWithThinking reports whether the first block is a thinking block.
func startsWithThinking(blocks []map[string]any) bool {
	return len(blocks) > 0 && (blocks[0]["type"] == "thinking" || blocks[0]["type"] == "redacted_thinking")
}

func firstType(blocks []map[string]any) any {
	if len(blocks) == 0 {
		return "none"
	}
	return blocks[0]["type"]
}

// requestPrefix is the prefix a thinking block is written under: the whole
// request.
func requestPrefix(raw map[string]any) string {
	messages, _ := raw["messages"].([]any)
	return prefixHash(raw, len(messages), 0)
}
