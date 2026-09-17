// Package modelctx builds what the Model sees now.
//
// A Session is what happened; a ModelContext is the projection of it that one
// Turn sends to the Model. This package provides explicit, inspectable
// strategies that implement gotato.ContextBuilder:
//
//	modelctx.FullHistory()        every committed Message
//	modelctx.Window(n)            the last n Messages, aligned to a safe boundary
//	modelctx.SummaryRecent(n)     a synthetic summary of older Messages + the last n
//	modelctx.Chain(a, b, ...)     run builders in sequence
//
// and the compaction operation that rewrites a Session prefix into a summary
// while recording what was replaced.
package modelctx

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	gotato "github.com/jinhuang712/gotato"
)

// Strategy names reported in ModelContext.Metadata["strategy"].
const (
	StrategyFullHistory   = "full_history"
	StrategyWindow        = "window"
	StrategySummaryRecent = "summary_recent"
	StrategyChain         = "chain"
)

// MetadataCompaction marks a summary Message produced by SummaryRecent or
// Compact: ContentPart.Metadata[MetadataCompaction] == "summary".
const MetadataCompaction = "compaction"

// FullHistory returns the default strategy: the Model sees everything.
func FullHistory() gotato.ContextBuilder { return gotato.FullHistoryContext() }

// Window keeps the most recent n Messages. The cut is moved forward to the
// next user Message so an assistant tool call is never separated from its
// tool results; if no user Message follows the cut, the window is the whole
// tail from the cut, which is always a valid sequence.
func Window(n int) gotato.ContextBuilder {
	return gotato.ContextBuilderFunc(func(_ context.Context, snapshot gotato.ContextSnapshot) (gotato.ModelContext, error) {
		messages := snapshot.Messages
		cut := safeCut(messages, len(messages)-n)
		selected := messages[cut:]
		return gotato.ModelContext{
			SystemInstructions: snapshot.SystemInstructions,
			Messages:           selected,
			Metadata:           metadata(StrategyWindow, len(messages), len(selected), cut, map[string]string{"window": strconv.Itoa(n)}),
		}, nil
	})
}

// SummaryRecent replaces every Message before the last keep Messages with one
// synthetic summary Message produced by the Summarizer, without mutating the
// Session. The summary is a user-role Message tagged as a compaction summary.
// Use Compact to make the same replacement permanent.
func SummaryRecent(keep int, summarizer Summarizer) gotato.ContextBuilder {
	if summarizer == nil {
		summarizer = TruncateSummarizer{}
	}
	return gotato.ContextBuilderFunc(func(ctx context.Context, snapshot gotato.ContextSnapshot) (gotato.ModelContext, error) {
		messages := snapshot.Messages
		cut := safeCut(messages, len(messages)-keep)
		if cut <= 0 {
			return gotato.ModelContext{
				SystemInstructions: snapshot.SystemInstructions,
				Messages:           messages,
				Metadata:           metadata(StrategySummaryRecent, len(messages), len(messages), 0, map[string]string{"keep": strconv.Itoa(keep), "summarized": "0"}),
			}, nil
		}
		summary, err := summarizer.Summarize(ctx, messages[:cut])
		if err != nil {
			return gotato.ModelContext{}, err
		}
		summary = tagSummary(summary)
		selected := make([]gotato.Message, 0, len(messages)-cut+1)
		selected = append(selected, summary)
		selected = append(selected, messages[cut:]...)
		return gotato.ModelContext{
			SystemInstructions: snapshot.SystemInstructions,
			Messages:           selected,
			Metadata:           metadata(StrategySummaryRecent, len(messages), len(selected), cut, map[string]string{"keep": strconv.Itoa(keep), "summarized": strconv.Itoa(cut), "summarizer": summarizer.Name()}),
		}, nil
	})
}

// Chain runs builders in order; each receives the previous output as its
// snapshot Messages. Metadata records each strategy name.
func Chain(builders ...gotato.ContextBuilder) gotato.ContextBuilder {
	return gotato.ContextBuilderFunc(func(ctx context.Context, snapshot gotato.ContextSnapshot) (gotato.ModelContext, error) {
		current := gotato.ModelContext{SystemInstructions: snapshot.SystemInstructions, Messages: snapshot.Messages}
		names := make([]string, 0, len(builders))
		source := len(snapshot.Messages)
		for _, builder := range builders {
			if builder == nil {
				continue
			}
			view := snapshot
			view.SystemInstructions = current.SystemInstructions
			view.Messages = current.Messages
			next, err := builder.Build(ctx, view)
			if err != nil {
				return gotato.ModelContext{}, err
			}
			if next.SystemInstructions == "" {
				next.SystemInstructions = current.SystemInstructions
			}
			names = append(names, next.Metadata["strategy"])
			current = next
		}
		current.Metadata = metadata(StrategyChain, source, len(current.Messages), source-len(current.Messages), map[string]string{"chain": strings.Join(names, ">")})
		return current, nil
	})
}

// safeCut returns the first index >= want that starts a user Message, or 0
// when want <= 0, or len(messages) when no user Message follows want. A cut
// at a user Message never separates a tool call from its results.
func safeCut(messages []gotato.Message, want int) int {
	if want <= 0 {
		return 0
	}
	if want >= len(messages) {
		want = len(messages)
	}
	for i := want; i < len(messages); i++ {
		if messages[i].Role == gotato.RoleUser && !isSummary(messages[i]) {
			return i
		}
	}
	// Fall back to the first user Message strictly before want, so the
	// Context stays valid rather than empty.
	for i := want - 1; i > 0; i-- {
		if messages[i].Role == gotato.RoleUser {
			return i
		}
	}
	return 0
}

func isSummary(message gotato.Message) bool {
	for _, part := range message.Parts {
		if part.Metadata[MetadataCompaction] == "summary" {
			return true
		}
	}
	return false
}

func tagSummary(summary gotato.Message) gotato.Message {
	summary = summary.Clone()
	if summary.Role == "" {
		summary.Role = gotato.RoleUser
	}
	if len(summary.Parts) == 0 {
		summary.Parts = []gotato.ContentPart{{Kind: gotato.ContentText, Text: "(empty summary)"}}
	}
	for i := range summary.Parts {
		if summary.Parts[i].Metadata == nil {
			summary.Parts[i].Metadata = map[string]string{}
		}
		summary.Parts[i].Metadata[MetadataCompaction] = "summary"
	}
	return summary
}

func metadata(strategy string, source, selected, dropped int, extra map[string]string) map[string]string {
	out := map[string]string{
		"strategy":          strategy,
		"source_messages":   strconv.Itoa(source),
		"selected_messages": strconv.Itoa(selected),
		"dropped_messages":  strconv.Itoa(dropped),
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

// Report is the inspectable result of building a Context without running a
// Model.
type Report struct {
	Strategy         string              `json:"strategy"`
	SourceMessages   int                 `json:"source_messages"`
	SelectedMessages int                 `json:"selected_messages"`
	DroppedMessages  int                 `json:"dropped_messages"`
	ApproxTokens     int                 `json:"approx_tokens"`
	ApproxBytes      int                 `json:"approx_bytes"`
	Metadata         map[string]string   `json:"metadata,omitempty"`
	Context          gotato.ModelContext `json:"context"`
}

// Inspect builds the Context for snapshot and describes it. ApproxTokens is a
// bytes/4 heuristic over the encoded Context; provider-reported usage lives in
// the Session's Run records.
func Inspect(ctx context.Context, builder gotato.ContextBuilder, snapshot gotato.ContextSnapshot) (Report, error) {
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
	encoded, _ := json.Marshal(built)
	report := Report{
		Strategy:         built.Metadata["strategy"],
		SourceMessages:   len(snapshot.Messages),
		SelectedMessages: len(built.Messages),
		ApproxBytes:      len(encoded),
		ApproxTokens:     (len(encoded) + 3) / 4,
		Metadata:         built.Metadata,
		Context:          built,
	}
	if dropped, err := strconv.Atoi(built.Metadata["dropped_messages"]); err == nil {
		report.DroppedMessages = dropped
	} else {
		report.DroppedMessages = len(snapshot.Messages) - len(built.Messages)
	}
	return report, nil
}

// Parse turns a CLI-style strategy spec into a builder:
//
//	full | window:N | summary:N
//
// The Summarizer is used by summary:N; nil means TruncateSummarizer.
func Parse(spec string, summarizer Summarizer) (gotato.ContextBuilder, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "full" || spec == StrategyFullHistory {
		return FullHistory(), nil
	}
	name, arg, _ := strings.Cut(spec, ":")
	n, err := strconv.Atoi(arg)
	if err != nil || n <= 0 {
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "modelctx: strategy "+name+" needs a positive count, e.g. "+name+":8")
	}
	switch name {
	case StrategyWindow:
		return Window(n), nil
	case "summary", StrategySummaryRecent:
		return SummaryRecent(n, summarizer), nil
	default:
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "modelctx: unknown strategy "+name+" (use full, window:N, summary:N)")
	}
}
