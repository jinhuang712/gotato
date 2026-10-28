package modelctx

import (
	"context"
	"encoding/json"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Report is the inspectable result of building a Context without running a
// Model. Request is what the next Turn sends for the Session as committed
// right now.
//
// A Report from ReportOf is built by the live Session itself, with the code its
// Turns run, so it includes the ContextTransformer and MessageConverter
// Extensions, the visible Tools and the model options; service.Runner.Inspect,
// the CLI and the HTTP endpoints return that kind. A Report from
// Inspect or InspectSession runs the ContextBuilder alone.
//
// When CompactionPending is true, the next Run compacts the Session before
// its first Turn, so that Turn sends a shorter request than this one.
// PrefixHash covers the system prompt, the tools, and every Message but the
// last, so two Reports with equal PrefixHash present an identical cacheable
// prefix, and it equals the prefix_hash of the context_built Event the Turn
// emits.
type Report struct {
	SessionID            string              `json:"session_id,omitempty"`
	Strategy             string              `json:"strategy"`
	SourceMessages       int                 `json:"source_messages"`
	SelectedMessages     int                 `json:"selected_messages"`
	ApproxTokens         int                 `json:"approx_tokens"`
	SystemBytes          int                 `json:"system_bytes"`
	PrefixHash           string              `json:"prefix_hash"`
	CompactionPending    bool                `json:"compaction_pending"`
	CompactCeilingTokens int                 `json:"compact_ceiling_tokens,omitempty"`
	Metadata             map[string]string   `json:"metadata,omitempty"`
	Compactions          []gotato.Compaction `json:"compactions,omitempty"`
	Context              gotato.ModelContext `json:"context"`
	Request              gotato.ModelRequest `json:"request"`
}

// ReportOf describes the ContextInspection a gotato.Session returns from
// InspectContext.
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
// Extension, no model options and no Tool catalog, so its PrefixHash equals a
// Session's only when the Session has no catalog (see gotato.AssembleRequest).
func Inspect(ctx context.Context, builder gotato.ContextBuilder, snapshot gotato.ContextSnapshot, tools []gotato.ToolSpec) (Report, error) {
	if builder == nil {
		builder = gotato.FullHistoryContext()
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
func InspectSession(ctx context.Context, builder gotato.ContextBuilder, s *session.Record, systemInstructions string, tools []gotato.ToolSpec) (Report, error) {
	report, err := Inspect(ctx, builder, gotato.ContextSnapshot{SystemInstructions: systemInstructions, Messages: s.Messages()}, tools)
	if err != nil {
		return Report{}, err
	}
	report.SessionID = s.ID()
	report.Compactions = s.Compactions()
	return report, nil
}
