package gotato

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Transcript is the committed history an Agent runs against: it is the
// Session's record of what happened. The Agent appends every committed
// Message to it and reads it back when it builds the next Context.
//
// The Agent goroutine is the only writer while a Run is in flight. A
// Transcript may be shared between successive Runs and between Agents, but it
// must not be mutated concurrently with a Run that is using it.
//
//	Messages returns the committed Messages in order. The Agent treats the
//	returned slice as read-only and never mutates it.
//
// Len reports the committed Message count without materializing the history,
// so an Agent can enforce its Message bound without copying.
type Transcript interface {
	Len() int
	Messages() []Message
	Append(Message) error
}

// memoryTranscript is the default Transcript: a private slice that lives and
// dies with the Agent. It preserves the pre-Session behavior of NewAgent.
type memoryTranscript struct {
	messages []Message
}

func (t *memoryTranscript) Len() int { return len(t.messages) }

func (t *memoryTranscript) Messages() []Message { return t.messages }

func (t *memoryTranscript) Append(message Message) error {
	t.messages = append(t.messages, message)
	return nil
}

// WithTranscript makes the Agent commit to an external Transcript instead of
// a private in-memory one. Use it to run an Agent against a Session.
func WithTranscript(transcript Transcript) Option {
	return func(c *agentConfig) error {
		if transcript == nil {
			return runtimeError(ErrInvalidArgument, "WithTranscript", "transcript is nil", nil)
		}
		c.transcript = transcript
		return nil
	}
}

// Block is one tagged piece of prompt content. It renders as
//
//	<tag attr="value">text</tag>
//
// or as bare text when Tag is empty. XML tags mark boundaries and provenance;
// the text inside is whatever the content is: markdown for prose, JSON for
// structured data.
type Block struct {
	Tag   string            `json:"tag,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
	Text  string            `json:"text"`
}

// RenderBlocks renders Blocks one per line, attributes in sorted order so
// the output is byte-stable for identical input.
func RenderBlocks(blocks []Block) string {
	var b strings.Builder
	for i, block := range blocks {
		if i > 0 {
			b.WriteByte('\n')
		}
		if block.Tag == "" {
			b.WriteString(block.Text)
			continue
		}
		b.WriteByte('<')
		b.WriteString(block.Tag)
		keys := make([]string, 0, len(block.Attrs))
		for key := range block.Attrs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			b.WriteByte(' ')
			b.WriteString(key)
			b.WriteString(`="`)
			b.WriteString(strings.ReplaceAll(block.Attrs[key], `"`, `&quot;`))
			b.WriteByte('"')
		}
		b.WriteByte('>')
		if strings.Contains(block.Text, "\n") {
			b.WriteByte('\n')
			b.WriteString(block.Text)
			b.WriteByte('\n')
		} else {
			b.WriteString(block.Text)
		}
		b.WriteString("</")
		b.WriteString(block.Tag)
		b.WriteByte('>')
	}
	return b.String()
}

// PanelTag wraps the dynamic panel appended to the tail of a request.
const PanelTag = "panel"

// ModelContext is what the Model sees for one Turn, laid out for prompt
// caching: the most static content first, the most dynamic content last.
//
//	SystemInstructions + System  →  system prompt      (stable across Runs)
//	tools                        →  tool definitions   (stable across Turns)
//	Messages                     →  history            (append-only prefix)
//	Panel                        →  <panel> appended to the last Message (changes every Turn)
//
// It is a projection built from the Transcript by a ContextBuilder; nothing
// in it is committed back. In particular the Panel never enters the Session.
type ModelContext struct {
	SystemInstructions string    `json:"system_instructions,omitempty"`
	System             []Block   `json:"system,omitempty"`
	Messages           []Message `json:"messages"`
	Panel              []Block   `json:"panel,omitempty"`
	// Metadata describes how the Context was built so the decision can be
	// inspected. Builders set at least "strategy", "source_messages",
	// "selected_messages", and "dropped_messages".
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Clone returns a deep copy.
func (c ModelContext) Clone() ModelContext {
	out := c
	out.Messages = cloneMessages(c.Messages)
	out.System = cloneBlocks(c.System)
	out.Panel = cloneBlocks(c.Panel)
	if c.Metadata != nil {
		out.Metadata = make(map[string]string, len(c.Metadata))
		for k, v := range c.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

func cloneBlocks(blocks []Block) []Block {
	if blocks == nil {
		return nil
	}
	out := make([]Block, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.Attrs != nil {
			out[i].Attrs = make(map[string]string, len(block.Attrs))
			for k, v := range block.Attrs {
				out[i].Attrs[k] = v
			}
		}
	}
	return out
}

// RenderSystem returns the system prompt: the instructions followed by the
// static Blocks.
func (c ModelContext) RenderSystem() string {
	if len(c.System) == 0 {
		return c.SystemInstructions
	}
	rendered := RenderBlocks(c.System)
	if c.SystemInstructions == "" {
		return rendered
	}
	return c.SystemInstructions + "\n\n" + rendered
}

// RenderPanel returns the dynamic panel as one <panel> element, or "" when
// there is no panel.
func (c ModelContext) RenderPanel() string {
	if len(c.Panel) == 0 {
		return ""
	}
	return "<" + PanelTag + ">\n" + RenderBlocks(c.Panel) + "\n</" + PanelTag + ">"
}

// ContextBuilder decides what the Model sees now. It receives a read-only
// snapshot of the committed Transcript and returns the layout for this Turn.
// The Agent applies ContextTransformer and MessageConverter Extensions to the
// builder's Messages afterwards, so a builder is the primary strategy and
// Extensions remain post-processing.
type ContextBuilder interface {
	Build(context.Context, ContextSnapshot) (ModelContext, error)
}

// ContextBuilderFunc adapts a function to ContextBuilder.
type ContextBuilderFunc func(context.Context, ContextSnapshot) (ModelContext, error)

// Build implements ContextBuilder.
func (f ContextBuilderFunc) Build(ctx context.Context, snapshot ContextSnapshot) (ModelContext, error) {
	return f(ctx, snapshot)
}

// FullHistoryContext is the default ContextBuilder: the Model sees the whole
// committed Transcript, append-only. History shrinks only through an
// explicit compaction of the Session, never by selection here, so the request
// prefix stays byte-stable across Turns and provider prompt caches hit.
func FullHistoryContext() ContextBuilder {
	return ContextBuilderFunc(func(_ context.Context, snapshot ContextSnapshot) (ModelContext, error) {
		count := strconv.Itoa(len(snapshot.Messages))
		return ModelContext{
			SystemInstructions: snapshot.SystemInstructions,
			Messages:           snapshot.Messages,
			Metadata: map[string]string{
				"strategy":          "full_history",
				"source_messages":   count,
				"selected_messages": count,
				"dropped_messages":  "0",
			},
		}, nil
	})
}

// WithContextBuilder selects the strategy that turns the Transcript into the
// Model's Context for each Turn. The default is FullHistoryContext.
func WithContextBuilder(builder ContextBuilder) Option {
	return func(c *agentConfig) error {
		if builder == nil {
			return runtimeError(ErrInvalidArgument, "WithContextBuilder", "builder is nil", nil)
		}
		c.contextBuilder = builder
		return nil
	}
}

// RunPreparer is an Extension stage that runs inside the Agent goroutine at
// the start of every Run, before the prompt is committed. It is the one safe
// point to rewrite the Transcript (for example to compact a Session that has
// grown past a token budget), because no Turn is using it yet.
type RunPreparer interface {
	PrepareRun(context.Context, Transcript) error
}

// ToolSource supplies Tools whose set may change between Turns. The Agent
// asks every installed ToolSource for its Tools at the start of each Turn and
// keeps that set for the whole Turn, so a Tool the Model was shown can always
// be resolved when the Model calls it.
//
// Tools returned by a ToolSource are qualified under the root namespace like
// Tools installed with WithTool. Package toolregistry provides a registry
// with register/unregister/activate/deactivate that implements ToolSource.
type ToolSource interface {
	Tools() []Tool
}

// WithToolSource installs a dynamic Tool source.
func WithToolSource(source ToolSource) Option {
	return func(c *agentConfig) error {
		if source == nil {
			return runtimeError(ErrInvalidArgument, "WithToolSource", "source is nil", nil)
		}
		c.toolSources = append(c.toolSources, source)
		return nil
	}
}

// ToolInspector reports the Tools an Agent currently exposes to its Model.
// The core Agent implements it; the result is a snapshot sorted by ID.
type ToolInspector interface {
	Tools() []ToolSpec
}

// Tools implements ToolInspector. It is safe to call from any goroutine while
// the Agent is idle; during a Run it reflects the set of the most recent Turn.
func (a *coreAgent) Tools() []ToolSpec {
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	specs := a.registry.visibleSpecs()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}

// Transcript returns the Transcript this Agent commits to.
func (a *coreAgent) Transcript() Transcript { return a.transcript }

// AssembleRequest lays out one ModelRequest from a built Context and the
// visible Tools, in cache-friendly order, and reports the prefix hash. It is
// what the Agent sends; it is exported so inspection tools show exactly the
// same bytes.
//
//   - Messages are reduced to what a provider needs (ForModel).
//   - The panel is appended as a text part to the last Message, so it never
//     changes the prefix and never creates a second consecutive user turn.
//   - Cache breakpoints are placed after the system prompt, after the tools,
//     and before the last Message.
//   - PrefixHash covers system + tools + every Message but the last: two
//     consecutive Turns with the same PrefixHash present an identical cacheable
//     prefix to the provider.
func AssembleRequest(built ModelContext, tools []ToolSpec) (ModelRequest, string) {
	messages := ForModel(built.Messages)
	if panel := built.RenderPanel(); panel != "" && len(messages) > 0 {
		last := &messages[len(messages)-1]
		if last.Role != RoleAssistant {
			last.Parts = append(last.Parts, ContentPart{Kind: ContentText, Text: "\n\n" + panel})
		}
	}
	request := ModelRequest{
		SystemInstructions: built.RenderSystem(),
		Messages:           messages,
		Tools:              cloneToolSpecs(tools),
	}
	sort.Slice(request.Tools, func(i, j int) bool { return request.Tools[i].ID < request.Tools[j].ID })
	if request.SystemInstructions != "" {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAfterSystem})
	}
	if len(request.Tools) > 0 {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAfterTools})
	}
	if len(messages) >= 2 {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAfterMessage, Index: len(messages) - 2})
	}
	return request, prefixHash(request)
}

// ForModel reduces Messages to the fields a provider needs: role, parts
// (without runtime metadata), tool calls, and tool results. Runtime fields
// (IDs, usage, stop reasons) are dropped so they can never perturb the prompt
// bytes between Turns.
func ForModel(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, message := range messages {
		m := Message{Role: message.Role, ToolCalls: cloneToolCalls(message.ToolCalls)}
		m.Parts = make([]ContentPart, 0, len(message.Parts))
		for _, part := range message.Parts {
			part.Metadata = nil
			part.Data = slices.Clone(part.Data)
			part.Signature = slices.Clone(part.Signature)
			m.Parts = append(m.Parts, part)
		}
		if message.ToolResult != nil {
			result := ToolResult{CallID: message.ToolResult.CallID, Status: message.ToolResult.Status, SafeError: message.ToolResult.SafeError}
			result.Content = cloneContent(message.ToolResult.Content)
			for j := range result.Content {
				result.Content[j].Metadata = nil
			}
			m.ToolResult = &result
		}
		out[i] = m
	}
	return out
}

func cloneToolCalls(calls []ToolCall) []ToolCall {
	if calls == nil {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, call := range calls {
		out[i] = call
		out[i].Arguments = append([]byte(nil), call.Arguments...)
	}
	return out
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
	if len(request.Messages) > 1 {
		if encoded, err := json.Marshal(request.Messages[:len(request.Messages)-1]); err == nil {
			h.Write(encoded)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
