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

	"github.com/jinhuang712/gotato/internal/text"
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
			return runtimeError(CodeInvalidArgument, "WithTranscript", "transcript is nil", nil)
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

// renderBlocks renders Blocks one per line, attributes in sorted order so
// the output is byte-stable for identical input.
func renderBlocks(blocks []Block) string {
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

// renderSystem returns the system prompt: the instructions followed by the
// static Blocks.
func (c ModelContext) renderSystem() string {
	if len(c.System) == 0 {
		return c.SystemInstructions
	}
	rendered := renderBlocks(c.System)
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
			return runtimeError(CodeInvalidArgument, "WithContextBuilder", "builder is nil", nil)
		}
		c.contextBuilder = builder
		return nil
	}
}

// TurnPreparer is an Extension stage that runs inside the Session goroutine
// before every model call, with no Tool call open: before the first call of a
// Run, before each call after a Tool batch, and once more when a call was
// refused as too long (PendingTurn.Overflow). It is the one safe point to
// rewrite the Transcript, for example to compact a Session that has grown past
// its window, because no Turn is using it. The policy (when to rewrite) stays
// with the preparer; the hook only gives it the chance.
//
// A preparer that rewrote the Transcript returns TurnPrepared{Changed: true},
// and the Loop builds the request again from it. An error ends the Run; one
// that is a *RuntimeError ends it with that code, any other is an
// CodeExtensionFailure.
type TurnPreparer interface {
	PrepareTurn(context.Context, PendingTurn) (TurnPrepared, error)
}

// PendingTurn is what a TurnPreparer is given about the model call that is
// about to be made.
type PendingTurn struct {
	SessionID SessionID
	RunID     RunID
	Turn      TurnNumber
	// Transcript is the Session's Transcript. A preparer may rewrite it, and
	// must say so in TurnPrepared.
	Transcript Transcript
	// RunStart is how many Messages the Transcript held when the Run began:
	// the Run's own Messages follow them. A rewrite changes the Transcript,
	// so afterwards the Run's Messages are found by Message.RunID.
	RunStart int
	// Request is the request about to be sent, built from the Transcript as it
	// stands. On an overflow retry it is the request the Model refused.
	Request ModelRequest
	// Overflow is true when the Model refused Request as longer than its
	// window before any output, and this is the one chance to shorten it. A
	// preparer that cannot returns Changed false, and the Run ends with
	// CodeContextOverflow.
	Overflow bool
	// Refusal is the error the Model refused Request with, when Overflow is
	// true, so a preparer can read what the refusal names, such as the limit
	// of the request (an error method `ContextLimit() int` says it in
	// tokens, as the gateway's does when the provider's message names one).
	Refusal error
	// Flush writes the Events the Run has staged, so a preparer that appends
	// to the Transcript's store writes after them and not before.
	Flush func(context.Context) error
}

// TurnPrepared is the result of PrepareTurn.
type TurnPrepared struct {
	// Changed says the preparer rewrote the Transcript, so the Loop builds
	// the request again before it calls the Model.
	Changed bool
}

// ToolSource supplies Tools whose set may change between Turns. The Session
// asks every installed ToolSource for its Tools at the start of each Turn and
// keeps that set for the whole Turn, so a Tool the Model was shown can always
// be resolved when the Model calls it.
//
// Tools returned by a ToolSource are qualified under the root namespace like
// Tools installed with WithTools.
type ToolSource interface {
	Tools() []Tool
}

// WithToolSource installs a dynamic Tool source.
func WithToolSource(source ToolSource) Option {
	return func(c *sessionConfig) error {
		if source == nil {
			return runtimeError(CodeInvalidArgument, "WithToolSource", "source is nil", nil)
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

// ToolSetStatus is what a Session knows of one installed ToolSet.
type ToolSetStatus struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Unavailable is true when the ToolSet failed its last resolution; it then
	// holds no Tools.
	Unavailable bool `json:"unavailable,omitempty"`
	// Tools are the ToolSet's Tools in ID order, under their qualified IDs.
	Tools []ToolSetTool `json:"tools,omitempty"`
}

// ToolSetTool is one Tool of an installed ToolSet.
type ToolSetTool struct {
	Spec ToolSpec `json:"spec"`
	// Loaded is true when the Tool is loaded (a fact of the Transcript) or its
	// ToolSet is installed active. A Tool the filter refuses is still Loaded;
	// it is just not offered.
	Loaded bool `json:"loaded"`
}

// ToolSets reports every installed ToolSet as the last resolution left it, in
// install order. A Tool above the Effect cap is left out, as it is never
// offered. A Tool the WithToolFilter keep refuses is still listed, so a
// caller can name it to switch it back on.
func (a *Session) ToolSets() []ToolSetStatus {
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.toolSetStatus()
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
// bytes between Turns. A reply cut off mid-stream (StopReasonCutOff) is left out:
// the Model sees what it saw before that reply started.
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
			result := ToolResult{CallID: message.ToolResult.CallID, Status: message.ToolResult.Status, SafeError: text.EscapeTags(message.ToolResult.SafeError), Loaded: slices.Clone(message.ToolResult.Loaded)}
			result.Content = cloneContent(message.ToolResult.Content)
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
