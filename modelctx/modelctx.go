// Package modelctx builds what the Model sees now.
//
// A Session is what happened; a ModelContext is the projection of it that one
// Turn sends to the Model. Within a Session the history is append-only and the
// Model always sees all of it, laid out so provider prompt caches hit:
//
//	system  = instruction + static Blocks        stable across Runs
//	tools   = visible ToolSpecs, sorted           stable across Turns
//	history = every committed Message             append-only prefix
//	panel   = dynamic Blocks on the tail Message  changes every Turn
//
// History shrinks only through compaction, an explicit and recorded rewrite
// of the Session that replaces a prefix by a summary. Compaction can be
// invoked directly (Compact) or triggered by a token budget at the start of
// a Run (AutoCompact).
//
//	builder := modelctx.WithPanel(
//	    modelctx.WithStatic(modelctx.FullHistory(), modelctx.Resource("AGENTS.md", agentsMD)),
//	    func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error) {
//	        return []gotato.Block{modelctx.Time(time.Now())}, nil
//	    },
//	)
//	agent, _ := gotato.NewAgent(
//	    gotato.WithModel(m),
//	    gotato.WithTranscript(s),
//	    gotato.WithContextBuilder(builder),
//	    gotato.WithExtension(modelctx.AutoCompact(s, modelctx.CompactPolicy{Ceiling: 60000, Floor: 30000})),
//	)
package modelctx

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// StrategyFullHistory is the one selection strategy: everything, append-only.
const StrategyFullHistory = "full_history"

// MetadataCompaction marks a summary Message produced by Compact:
// ContentPart.Metadata[MetadataCompaction] == "summary".
const MetadataCompaction = "compaction"

// FullHistory returns the default builder: the Model sees the whole Session.
func FullHistory() gotato.ContextBuilder { return gotato.FullHistoryContext() }

// WithStatic appends static Blocks to the system prompt produced by inner.
// Static content belongs at the top of the request: it is identical across
// Runs and forms the most valuable part of the cacheable prefix. Keep
// anything that changes per Turn out of it.
func WithStatic(inner gotato.ContextBuilder, blocks ...gotato.Block) gotato.ContextBuilder {
	if inner == nil {
		inner = FullHistory()
	}
	return gotato.ContextBuilderFunc(func(ctx context.Context, snapshot gotato.ContextSnapshot) (gotato.ModelContext, error) {
		built, err := inner.Build(ctx, snapshot)
		if err != nil {
			return gotato.ModelContext{}, err
		}
		built.System = append(built.System, blocks...)
		if built.Metadata == nil {
			built.Metadata = map[string]string{}
		}
		built.Metadata["static_blocks"] = strconv.Itoa(len(built.System))
		return built, nil
	})
}

// PanelFunc produces the dynamic Blocks for one Turn.
type PanelFunc func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error)

// WithPanel attaches a dynamic panel to the Context produced by inner. The
// panel is rendered as a <panel> element on the tail Message, so it changes
// nothing before it and is never committed to the Session.
func WithPanel(inner gotato.ContextBuilder, panel PanelFunc) gotato.ContextBuilder {
	if inner == nil {
		inner = FullHistory()
	}
	return gotato.ContextBuilderFunc(func(ctx context.Context, snapshot gotato.ContextSnapshot) (gotato.ModelContext, error) {
		built, err := inner.Build(ctx, snapshot)
		if err != nil {
			return gotato.ModelContext{}, err
		}
		if panel != nil {
			blocks, err := panel(ctx, snapshot)
			if err != nil {
				return gotato.ModelContext{}, err
			}
			built.Panel = append(built.Panel, blocks...)
		}
		if built.Metadata == nil {
			built.Metadata = map[string]string{}
		}
		built.Metadata["panel_blocks"] = strconv.Itoa(len(built.Panel))
		return built, nil
	})
}

// Resource is a Block carrying a file or document:
//
//	<resource path="AGENTS.md">…markdown…</resource>
func Resource(path, content string) gotato.Block {
	return gotato.Block{Tag: "resource", Attrs: map[string]string{"path": path}, Text: content}
}

// Text is a tagged prose Block; an empty tag renders bare text.
func Text(tag, text string) gotato.Block { return gotato.Block{Tag: tag, Text: text} }

// JSON is a tagged Block whose body is the JSON encoding of value.
func JSON(tag string, value any) gotato.Block {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(`{"error":"unencodable"}`)
	}
	return gotato.Block{Tag: tag, Text: string(encoded)}
}

// Time is the dynamic <time> Block in RFC 3339 UTC.
func Time(now time.Time) gotato.Block {
	return gotato.Block{Tag: "time", Text: now.UTC().Format(time.RFC3339)}
}

// EstimateTokens is the bytes/4 heuristic over the JSON encoding of the
// Messages a provider would receive. Provider-reported usage lives in the
// Session's Run records; this estimate exists for budgets and inspection.
func EstimateTokens(messages []gotato.Message) int {
	encoded, _ := json.Marshal(gotato.ForModel(messages))
	return (len(encoded) + 3) / 4
}

// Report is the inspectable result of building a Context without running a
// Model. Request is exactly what the Agent would send for the Session as
// committed right now; when an auto-compaction budget is configured and
// exceeded, the next Run compacts first, so that request will differ.
// PrefixHash covers the system prompt, the tools, and every Message but the
// last, so two Reports with equal PrefixHash present an identical cacheable
// prefix.
type Report struct {
	SessionID        string               `json:"session_id,omitempty"`
	Strategy         string               `json:"strategy"`
	SourceMessages   int                  `json:"source_messages"`
	SelectedMessages int                  `json:"selected_messages"`
	ApproxTokens     int                  `json:"approx_tokens"`
	SystemBytes      int                  `json:"system_bytes"`
	PanelBytes       int                  `json:"panel_bytes"`
	PrefixHash       string               `json:"prefix_hash"`
	Metadata         map[string]string    `json:"metadata,omitempty"`
	Compactions      []session.Compaction `json:"compactions,omitempty"`
	Context          gotato.ModelContext  `json:"context"`
	Request          gotato.ModelRequest  `json:"request"`
}

// Inspect builds the Context for snapshot with the given visible tools and
// describes the request that would be sent.
func Inspect(ctx context.Context, builder gotato.ContextBuilder, snapshot gotato.ContextSnapshot, tools []gotato.ToolSpec) (Report, error) {
	if builder == nil {
		builder = FullHistory()
	}
	built, err := builder.Build(ctx, snapshot)
	if err != nil {
		return Report{}, err
	}
	if built.SystemInstructions == "" {
		built.SystemInstructions = snapshot.SystemInstructions
	}
	request, prefix := gotato.AssembleRequest(built, tools)
	encoded, _ := json.Marshal(request)
	return Report{
		Strategy:         built.Metadata["strategy"],
		SourceMessages:   len(snapshot.Messages),
		SelectedMessages: len(built.Messages),
		ApproxTokens:     (len(encoded) + 3) / 4,
		SystemBytes:      len(request.SystemInstructions),
		PanelBytes:       len(built.RenderPanel()),
		PrefixHash:       prefix,
		Metadata:         built.Metadata,
		Context:          built,
		Request:          request,
	}, nil
}

// InspectSession is Inspect over a Session's committed history; the report
// also carries the Session's compaction records.
func InspectSession(ctx context.Context, builder gotato.ContextBuilder, s *session.Session, systemInstructions string, tools []gotato.ToolSpec) (Report, error) {
	report, err := Inspect(ctx, builder, gotato.ContextSnapshot{SystemInstructions: systemInstructions, Messages: s.Messages()}, tools)
	if err != nil {
		return Report{}, err
	}
	report.SessionID = s.ID()
	report.Compactions = s.Compactions()
	return report, nil
}
