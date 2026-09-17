package gotato

import (
	"context"
	"sort"
	"strconv"
)

// Transcript is the committed history an Agent runs against: it is the
// Session's record of what happened. The Agent appends every committed
// Message to it and reads it back when it builds the next Context.
//
// The Agent goroutine is the only writer while a Run is in flight. A
// Transcript may be shared between successive Runs and between Agents, but it
// must not be mutated concurrently with a Run that is using it.
//
// Messages returns the committed Messages in order. The Agent treats the
// returned slice as read-only and never mutates it.
type Transcript interface {
	Messages() []Message
	Append(Message) error
}

// memoryTranscript is the default Transcript: a private slice that lives and
// dies with the Agent. It preserves the pre-Session behavior of NewAgent.
type memoryTranscript struct {
	messages []Message
}

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

// ModelContext is what the Model sees for one Turn. It is a projection built
// from the Transcript by a ContextBuilder; it is never committed back.
type ModelContext struct {
	SystemInstructions string    `json:"system_instructions,omitempty"`
	Messages           []Message `json:"messages"`
	// Metadata describes how the Context was built so the decision can be
	// inspected. Builders in package modelctx set at least "strategy",
	// "source_messages", "selected_messages", and "dropped_messages".
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Clone returns a deep copy.
func (c ModelContext) Clone() ModelContext {
	out := c
	out.Messages = cloneMessages(c.Messages)
	if c.Metadata != nil {
		out.Metadata = make(map[string]string, len(c.Metadata))
		for k, v := range c.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// ContextBuilder decides what the Model sees now. It receives a read-only
// snapshot of the committed Transcript and returns the Messages for this
// Turn. The Agent applies ContextTransformer and MessageConverter Extensions
// to the builder's output afterwards, so a builder is the primary strategy
// and Extensions remain post-processing.
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
// committed Transcript. Package modelctx provides windowed, summarized, and
// chained strategies.
func FullHistoryContext() ContextBuilder {
	return ContextBuilderFunc(func(_ context.Context, snapshot ContextSnapshot) (ModelContext, error) {
		count := itoa(len(snapshot.Messages))
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

func itoa(n int) string { return strconv.Itoa(n) }
