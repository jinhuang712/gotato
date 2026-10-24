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

// Transcript is the committed history a Session runs against: it is the
// Session's Record of what happened. The Session appends every committed
// Message to it and reads it back when it builds the next Context.
//
// The Session goroutine is the only writer while a Run is in flight. A
// Transcript may be shared between successive Runs and between Sessions, but it
// must not be mutated concurrently with a Run that is using it.
//
// Messages returns the committed Messages in order. The Session treats the
// returned slice as read-only and never mutates it. Len reports the committed
// Message count without materializing the history, so a Session can enforce its
// Message bound without copying.
type Transcript interface {
	Len() int
	Messages() []Message
	Append(Message) error
}

// memoryTranscript is the default Transcript: a private slice that lives and
// dies with the Session.
type memoryTranscript struct {
	messages []Message
}

func (t *memoryTranscript) Len() int { return len(t.messages) }

func (t *memoryTranscript) Messages() []Message { return t.messages }

func (t *memoryTranscript) Append(message Message) error {
	t.messages = append(t.messages, message)
	return nil
}

// WithTranscript makes the Session commit to an external Transcript, such as
// the session.Record it is loaded from, instead of a private in-memory one.
func WithTranscript(transcript Transcript) Option {
	return func(c *sessionConfig) error {
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

// ModelContext is what the Model sees for one Turn, laid out for prompt
// caching: the most static content first, the history last.
//
//	SystemInstructions + System  →  system prompt      (stable across Runs)
//	tools                        →  tool definitions   (stable across Turns)
//	Messages                     →  history            (append-only prefix)
//
// It is a projection built from the Transcript by a ContextBuilder; nothing
// in it is committed back.
type ModelContext struct {
	SystemInstructions string    `json:"system_instructions,omitempty"`
	System             []Block   `json:"system,omitempty"`
	Messages           []Message `json:"messages"`
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

// ContextBuilder decides what the Model sees now. It receives a read-only
// snapshot of the committed Transcript and returns the layout for this Turn.
// The Session applies ContextTransformer and MessageConverter Extensions to the
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
	return func(c *sessionConfig) error {
		if builder == nil {
			return runtimeError(ErrInvalidArgument, "WithContextBuilder", "builder is nil", nil)
		}
		c.contextBuilder = builder
		return nil
	}
}

// RunPreparer is an Extension stage that runs inside the Session goroutine at
// the start of every Run, before the prompt is committed. It is the one safe
// point to rewrite the Transcript (for example to compact a Session that has
// grown past a token budget), because no Turn is using it yet.
type RunPreparer interface {
	PrepareRun(context.Context, Transcript) error
}

// ToolSource supplies Tools whose set may change between Turns. The Session
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
	return func(c *sessionConfig) error {
		if source == nil {
			return runtimeError(ErrInvalidArgument, "WithToolSource", "source is nil", nil)
		}
		c.toolSources = append(c.toolSources, source)
		return nil
	}
}

// Tools reports the Tools the Session exposes to its Model, as a snapshot
// sorted by ID. While the Session is idle it is the set its next Turn offers;
// during a Run it reflects the set of the most recent Turn.
func (a *Session) Tools() []ToolSpec {
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	specs := a.registry.visibleSpecs()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}

// ContextInspection is the request a Session's next Turn would send, built
// without calling the Model.
type ContextInspection struct {
	// SourceMessages is the number of committed Messages it was built from.
	SourceMessages int `json:"source_messages"`
	// Context is the built Context, after the ContextTransformer and
	// MessageConverter Extensions.
	Context ModelContext `json:"context"`
	// Request is what the Model would receive, model options included.
	Request ModelRequest `json:"request"`
	// PrefixHash is the hash a context_built Event reports for this request.
	PrefixHash string `json:"prefix_hash"`
}

// Transcript returns the Transcript this Session commits to.
func (a *Session) Transcript() Transcript { return a.transcript }

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
	system := built.RenderSystem()
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
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAfterSystem})
	}
	if len(request.Tools) > 0 {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAfterTools})
	}
	if len(messages) > 0 {
		request.CacheBreakpoints = append(request.CacheBreakpoints, CacheBreakpoint{After: CacheAfterMessage, Index: len(messages) - 1})
	}
	return request, prefixHash(request)
}

// ForModel reduces Messages to the fields a provider needs: role, parts
// (without runtime metadata), tool calls, and tool results. Runtime fields
// (IDs, usage, stop reasons) are dropped so they can never perturb the prompt
// bytes between Turns. A reply cut off mid-stream (StopCutOff) is left out:
// the Model sees what it saw before that reply started.
//
// Text that imitates one of Gotato's four tags (see EscapeTags) is escaped in
// user and assistant text, Tool Result content and SafeError, so neither a
// user, outside data nor the model's own words can speak as the system. A Part
// marked MetaTag is Gotato's own tagged text and is left as it is.
func ForModel(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.StopReason == StopCutOff {
			continue
		}
		m := Message{Role: message.Role, ToolCalls: cloneToolCalls(message.ToolCalls)}
		m.Parts = make([]ContentPart, 0, len(message.Parts))
		for _, part := range message.Parts {
			if _, tagged := part.Metadata[MetaTag]; !tagged && part.Kind == ContentText {
				part.Text = EscapeTags(part.Text)
			}
			part.Metadata = nil
			part.Data = slices.Clone(part.Data)
			part.Signature = slices.Clone(part.Signature)
			m.Parts = append(m.Parts, part)
		}
		if message.ToolResult != nil {
			result := ToolResult{CallID: message.ToolResult.CallID, Status: message.ToolResult.Status, SafeError: EscapeTags(message.ToolResult.SafeError)}
			result.Content = cloneContent(message.ToolResult.Content)
			for j := range result.Content {
				if _, tagged := result.Content[j].Metadata[MetaTag]; !tagged && result.Content[j].Kind == ContentText {
					result.Content[j].Text = EscapeTags(result.Content[j].Text)
				}
				result.Content[j].Metadata = nil
			}
			m.ToolResult = &result
		}
		out = append(out, m)
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
		out[i].Signature = slices.Clone(call.Signature)
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
