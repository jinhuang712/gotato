package record

import (
	"context"
	"encoding/json"

	"github.com/jinhuang712/gotato/gotato"
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
// CompactCeilingTokens is always present: 0 means the ceiling trigger is off
// (no window and no CeilingTokens), so the Session compacts only after a
// refusal as too long.
//
// When CompactionPending is true, the next Run compacts the Session before
// its first Turn, so that Turn sends a shorter request than this one.
// PrefixHash covers the system prompt, the tools, and every Message but the
// last, so two Reports with equal PrefixHash present an identical cacheable
// prefix, and it equals the prefix_hash of the context_built Event the Turn
// emits.
type Report struct {
	SessionID        string `json:"session_id,omitempty"`
	Strategy         string `json:"strategy"`
	SourceMessages   int    `json:"source_messages"`
	SelectedMessages int    `json:"selected_messages"`
	ApproxTokens     int    `json:"approx_tokens"`
	SystemBytes      int    `json:"system_bytes"`
	// The estimates are in tokens (bytes over 4, as ApproxTokens): the
	// instruction, the tool definitions, the Segment and Recap Messages, the
	// user and assistant Messages, and the Tool Results. They add up to
	// EstimateTokens of the request's Messages plus the estimate of its
	// instruction and tools, within the rounding of each part (ApproxTokens
	// counts the whole encoding of the request, so it is a little larger).
	// They are the numbers the context_built Event of that Turn carries.
	EstimateSystemTokens      int                 `json:"estimate_system_tokens"`
	EstimateToolDefsTokens    int                 `json:"estimate_tool_defs_tokens"`
	EstimateSegmentsTokens    int                 `json:"estimate_segments_tokens"`
	EstimateHistoryTokens     int                 `json:"estimate_history_tokens"`
	EstimateToolResultsTokens int                 `json:"estimate_tool_results_tokens"`
	PrefixHash                string              `json:"prefix_hash"`
	CompactionPending         bool                `json:"compaction_pending"`
	CompactCeilingTokens      int                 `json:"compact_ceiling_tokens"`
	Metadata                  map[string]string   `json:"metadata,omitempty"`
	Compactions               []gotato.Compaction `json:"compactions,omitempty"`
	Context                   gotato.ModelContext `json:"context"`
	Request                   gotato.ModelRequest `json:"request"`
}

// ReportOf describes the ContextInspection a gotato.Session returns from
// InspectContext.
func ReportOf(inspection gotato.ContextInspection) Report {
	encoded, _ := json.Marshal(inspection.Request)
	estimate := gotato.EstimateContext(inspection.Context, inspection.Request)
	return Report{
		EstimateSystemTokens:      estimate.System,
		EstimateToolDefsTokens:    estimate.ToolDefs,
		EstimateSegmentsTokens:    estimate.Segments,
		EstimateHistoryTokens:     estimate.History,
		EstimateToolResultsTokens: estimate.ToolResults,
		Strategy:                  inspection.Context.Metadata["strategy"],
		SourceMessages:            inspection.SourceMessages,
		SelectedMessages:          len(inspection.Context.Messages),
		ApproxTokens:              (len(encoded) + 3) / 4,
		SystemBytes:               len(inspection.Request.SystemInstructions),
		PrefixHash:                inspection.PrefixHash,
		Metadata:                  inspection.Context.Metadata,
		Context:                   inspection.Context,
		Request:                   inspection.Request,
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
func InspectSession(ctx context.Context, builder gotato.ContextBuilder, s *Record, systemInstructions string, tools []gotato.ToolSpec) (Report, error) {
	report, err := Inspect(ctx, builder, gotato.ContextSnapshot{SystemInstructions: systemInstructions, Messages: s.Messages()}, tools)
	if err != nil {
		return Report{}, err
	}
	report.SessionID = s.ID()
	report.Compactions = s.Compactions()
	return report, nil
}
