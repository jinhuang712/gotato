package gotato

import (
	"context"
	"maps"
	"sort"
	"strconv"
	"strings"
)

// ContextBlock is one tagged piece of prompt content. It renders as
//
//	<tag attr="value">text</tag>
//
// or as bare text when Tag is empty. XML tags mark boundaries and provenance;
// the text inside is whatever the content is: markdown for prose, JSON for
// structured data.
type ContextBlock struct {
	Tag   string            `json:"tag,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
	Text  string            `json:"text"`
}

// renderBlocks renders Blocks one per line, attributes in sorted order so
// the output is byte-stable for identical input.
func renderBlocks(blocks []ContextBlock) string {
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
	SystemInstructions string         `json:"system_instructions,omitempty"`
	System             []ContextBlock `json:"system,omitempty"`
	Messages           []Message      `json:"messages"`
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
		maps.Copy(out.Metadata, c.Metadata)
	}
	return out
}

func cloneBlocks(blocks []ContextBlock) []ContextBlock {
	if blocks == nil {
		return nil
	}
	out := make([]ContextBlock, len(blocks))
	for i, block := range blocks {
		out[i] = block
		if block.Attrs != nil {
			out[i].Attrs = make(map[string]string, len(block.Attrs))
			maps.Copy(out[i].Attrs, block.Attrs)
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
func WithContextBuilder(builder ContextBuilder) SessionOption {
	return func(c *sessionConfig) error {
		if builder == nil {
			return runtimeError(CodeInvalidArgument, "WithContextBuilder", "builder is nil", nil)
		}
		c.contextBuilder = builder
		return nil
	}
}
