// Package modelctx builds what the Model sees now.
//
// A Session is what happened; a ModelContext is the projection of it that one
// Turn sends to the Model. Within a Session the history is append-only and the
// Model always sees all of it, laid out so provider prompt caches hit:
//
//	system  = instruction + static Blocks   stable across Runs
//	tools   = visible ToolSpecs, sorted      stable across Turns
//	history = every committed Message        append-only prefix
//
// Nothing that changes every Turn is added: such content is never cached,
// so it costs its full price on every Turn. A fact the model needs now, such
// as the current time, comes from a tool the model calls.
//
// History shrinks only through compaction, an explicit and recorded rewrite
// of the Session that replaces a prefix by a summary. Compaction can be
// invoked directly (Compact) or triggered by a token budget at the start of
// a Run (AutoCompact).
//
//	builder := modelctx.WithStatic(modelctx.FullHistory(), modelctx.Resource("AGENTS.md", agentsMD))
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

// EstimateTokens is the bytes/4 heuristic over the JSON encoding of the
// Messages a provider would receive. Provider-reported usage lives in the
// Session's Run records; this estimate exists for budgets and inspection.
func EstimateTokens(messages []gotato.Message) int {
	encoded, _ := json.Marshal(gotato.ForModel(messages))
	return (len(encoded) + 3) / 4
}

// Report is the inspectable result of building a Context without running a
// Model. Request is what the next Turn sends for the Session as committed
// right now.
//
// A Report from ReportOf is built by the Agent itself, with the code its
// Turns run, so it includes the ContextTransformer and MessageConverter
// Extensions, the visible Tools and the model options; service.Runner.Inspect,
// the CLI and the HTTP and gRPC endpoints return that kind. A Report from
// Inspect or InspectSession runs the ContextBuilder alone.
//
// When CompactionPending is true, the next Run compacts the Session before
// its first Turn, so that Turn sends a shorter request than this one.
// PrefixHash covers the system prompt, the tools, and every Message but the
// last, so two Reports with equal PrefixHash present an identical cacheable
// prefix, and it equals the prefix_hash of the context_built Event the Turn
// emits.
type Report struct {
	SessionID         string               `json:"session_id,omitempty"`
	Strategy          string               `json:"strategy"`
	SourceMessages    int                  `json:"source_messages"`
	SelectedMessages  int                  `json:"selected_messages"`
	ApproxTokens      int                  `json:"approx_tokens"`
	SystemBytes       int                  `json:"system_bytes"`
	PrefixHash        string               `json:"prefix_hash"`
	CompactionPending bool                 `json:"compaction_pending"`
	CompactCeiling    int                  `json:"compact_ceiling,omitempty"`
	Metadata          map[string]string    `json:"metadata,omitempty"`
	Compactions       []session.Compaction `json:"compactions,omitempty"`
	Context           gotato.ModelContext  `json:"context"`
	Request           gotato.ModelRequest  `json:"request"`
}

// ReportOf describes an Agent's ContextInspection. Use it with the
// gotato.ContextInspector the core Agent implements.
func ReportOf(inspection gotato.ContextInspection) Report {
	encoded, _ := json.Marshal(inspection.Request)
	return Report{
		Strategy:         inspection.Context.Metadata["strategy"],
		SourceMessages:   inspection.SourceMessages,
		SelectedMessages: len(inspection.Context.Messages),
		ApproxTokens:     (len(encoded) + 3) / 4,
		SystemBytes:      len(inspection.Request.SystemInstructions),
		PrefixHash:       inspection.PrefixHash,
		Metadata:         inspection.Context.Metadata,
		Context:          inspection.Context,
		Request:          inspection.Request,
	}
}

// Inspect builds the Context for snapshot with the given visible tools and
// describes the request that would be sent. It runs builder alone: no
// Extension and no model options.
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
	return ReportOf(gotato.ContextInspection{
		SourceMessages: len(snapshot.Messages),
		Context:        built,
		Request:        request,
		PrefixHash:     prefix,
	}), nil
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
