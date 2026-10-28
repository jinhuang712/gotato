package gotato

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"

	"github.com/jinhuang712/gotato/internal/text"
)

// AssembleRequest lays out one ModelRequest from a built Context and the
// visible Tools, in cache-friendly order, and reports the prefix hash. It is
// what the Session sends; it is exported so inspection tools show exactly the
// same bytes.
//
//   - Messages are reduced to what a provider needs (ForModel).
//   - When a Message holds a Part marked MetaTag, the system part ends with
//     TagsSentence; a request without one is unchanged.
//   - Cache breakpoints are placed after the system prompt, after the tools,
//     and on the last Message, so the whole request is cached for the next
//     Turn; a provider finds the previous Turn's entry by looking back from
//     there.
//   - PrefixHash covers system + tools + every Message but the last: two
//     consecutive Turns with the same PrefixHash present an identical cacheable
//     prefix to the provider.
func AssembleRequest(built ModelContext, tools []ToolSpec) (ModelRequest, string) {
	messages := ForModel(built.Messages)
	system := built.renderSystem()
	if hasTagMeta(built.Messages) {
		if system != "" {
			system += "\n\n"
		}
		system += TagsSentence
	}
	request := ModelRequest{
		SystemInstructions: system,
		Messages:           messages,
		Tools:              cloneToolSpecs(tools),
	}
	sort.Slice(request.Tools, func(i, j int) bool { return request.Tools[i].ID < request.Tools[j].ID })
	if request.SystemInstructions != "" {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAnchorAfterSystem})
	}
	if len(request.Tools) > 0 {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAnchorAfterTools})
	}
	if len(messages) > 0 {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAnchorAfterMessage, Index: len(messages) - 1})
	}
	return request, prefixHash(request)
}

// ForModel reduces Messages to the fields a provider needs: role, parts
// (without runtime metadata), tool calls, and tool results. Runtime fields
// (IDs, usage, stop reasons) are dropped so they can never perturb the prompt
// bytes between Turns. A reply cut off mid-stream (StopReasonCutOff) is left
// out: the Model sees what it saw before that reply started.
//
// Text that imitates one of Gotato's four tags (see EscapeTags) is escaped in
// user and assistant text, json Parts (with the JSON escape for "<", so the
// value stays), Tool Result content and SafeError, so neither a
// user, outside data nor the model's own words can speak as the system. A Part
// marked MetaTag is Gotato's own tagged text and is left as it is.
func ForModel(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.StopReason == StopReasonCutOff {
			continue
		}
		if message.Role == RoleNotice {
			// A notice reaches the model as tagged text the system wrote.
			out = append(out, Message{Role: RoleNotice, Parts: []ContentPart{{Kind: ContentKindText, Text: renderNotice(message)}}})
			continue
		}
		m := Message{Role: message.Role, ToolCalls: cloneToolCalls(message.ToolCalls)}
		m.Parts = make([]ContentPart, 0, len(message.Parts))
		for _, part := range message.Parts {
			if _, tagged := part.Metadata[MetaTag]; !tagged {
				part.Text = escapePartText(part)
			}
			part.Metadata = nil
			part.Data = slices.Clone(part.Data)
			part.Signature = slices.Clone(part.Signature)
			m.Parts = append(m.Parts, part)
		}
		if message.ToolResult != nil {
			result := ToolResult{CallID: message.ToolResult.CallID, Status: message.ToolResult.Status, SafeError: text.EscapeTags(message.ToolResult.SafeError), LoadedTools: slices.Clone(message.ToolResult.LoadedTools)}
			result.Content = cloneParts(message.ToolResult.Content)
			for j := range result.Content {
				if _, tagged := result.Content[j].Metadata[MetaTag]; !tagged {
					result.Content[j].Text = escapePartText(result.Content[j])
				}
				result.Content[j].Metadata = nil
			}
			m.ToolResult = &result
		}
		out = append(out, m)
	}
	return out
}

// escapePartText escapes the text of a text or json Part. A json Part is sent
// to the model as text, so it is escaped too, with the JSON escape for "<"
// (backslash, u, 0, 0, 3, c) in place of "&lt;" so the JSON keeps its value.
func escapePartText(part ContentPart) string {
	switch part.Kind {
	case ContentKindText:
		return text.EscapeTags(part.Text)
	case ContentKindJSON:
		return text.EscapeTagsAs(part.Text, jsonLessThan)
	}
	return part.Text
}

// prefixHash hashes system + tools + messages[:len-1].
func prefixHash(request ModelRequest) string {
	h := sha256.New()
	h.Write([]byte(request.SystemInstructions))
	h.Write([]byte{0})
	if encoded, err := json.Marshal(request.Tools); err == nil {
		h.Write(encoded)
	}
	h.Write([]byte{0})
	if len(request.LoadedTools) > 0 || len(request.UnloadedTools) > 0 {
		if encoded, err := json.Marshal([]any{request.LoadedTools, request.UnloadedTools}); err == nil {
			h.Write(encoded)
		}
		h.Write([]byte{0})
	}
	if len(request.Messages) > 1 {
		if encoded, err := json.Marshal(request.Messages[:len(request.Messages)-1]); err == nil {
			h.Write(encoded)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildContext runs the ContextBuilder under the same panic guard as an
// Extension and falls back to the snapshot's system instructions when the
// builder leaves them empty.
func (a *Session) buildContext(ctx context.Context, snapshot ContextSnapshot) (ModelContext, *RuntimeError) {
	var built ModelContext
	if err := guard("ContextBuilder", func() error {
		result, failure := a.contextBuilder.Build(ctx, snapshot)
		built = result
		return failure
	}); err != nil {
		return ModelContext{}, err
	}
	if built.SystemInstructions == "" {
		built.SystemInstructions = snapshot.SystemInstructions
	}
	if built.Messages == nil {
		built.Messages = []Message{}
	}
	return built, nil
}

// assembleTurn builds the request one Turn sends. The ContextBuilder decides
// what the Model sees now, and the ContextTransformer and MessageConverter
// Extensions shape that view further. Neither output is ever committed as the
// transcript: Session is what happened, Context is what the Model sees now.
// The request is laid out for prompt caching: static system and tools first,
// the append-only history last. A Turn and InspectContext both call it, so an
// inspection shows the bytes a Turn sends.
func (a *Session) assembleTurn(ctx context.Context, snapshot ContextSnapshot) (ModelContext, ModelRequest, string, *RuntimeError) {
	built, err := a.buildContext(ctx, snapshot)
	if err != nil {
		return ModelContext{}, ModelRequest{}, "", err
	}
	if !a.extensions.empty() {
		view := snapshot
		view.SystemInstructions = built.SystemInstructions
		view.Messages = built.Messages
		transformed, err := a.extensions.transformContext(ctx, view)
		if err != nil {
			return ModelContext{}, ModelRequest{}, "", err
		}
		built.Messages = transformed
	}
	request, _ := AssembleRequest(built, a.registry.visibleSpecs())
	request.LoadedTools, request.UnloadedTools = a.registry.catalogView()
	prefix := prefixHash(request)
	request.Config = a.modelConfig
	request.CacheKey = a.cacheKey
	return built, request, prefix, nil
}
