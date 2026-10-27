package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/session"
)

// DefaultSegmentsBudgetTokens is the Segments' budget with no ceiling: the most the
// Segments together may hold, in estimated tokens (bytes ÷ 4), when the model
// has no known window and neither the Agent nor the Session sets a ceiling.
// With a ceiling, the budget is half of it (SegmentsBudget).
const DefaultSegmentsBudgetTokens = 32000

// SegmentsBudget returns the most the Segments of a Context may hold, in
// estimated tokens, under the ceiling in force: half of it, or
// DefaultSegmentsBudgetTokens when there is no ceiling (ceiling 0). When the
// Segments pass it, the oldest are merged (MergeSegments).
func SegmentsBudget(ceiling int) int {
	if ceiling > 0 {
		return max(ceiling/2, 1)
	}
	return DefaultSegmentsBudgetTokens
}

// MergeRecapWriter is a RecapWriter that also writes the Recap of a merge of
// the oldest Segments, from their Recaps alone. A writer that is not one, such
// as NoRecap and TruncateRecap, leaves the merged Segment with a digest built
// by code (mergeDigest). ModelRecap is one.
type MergeRecapWriter interface {
	RecapWriter
	WriteMergeRecap(context.Context, MergeRequest) (gotato.Message, error)
}

// MergeRequest is what a MergeRecapWriter is asked to write a Recap of.
type MergeRequest struct {
	// Segments are the Segments the merge replaces, oldest first. The first
	// may itself be a merged Segment, and then its Recap comes before the
	// others.
	Segments []gotato.Segment
	// Location is the time zone of the times the ask shows; nil means UTC.
	Location *time.Location
}

// Numbers returns the numbers of the Segments the merge stands for, oldest
// first, which read_segment reads once it is merged.
func (r MergeRequest) Numbers() []int { return numbersOf(r.Segments) }

// MergeReason says why a merge did nothing, or failed, in MergeResult.Reason.
type MergeReason string

const (
	// MergeReasonUnderBudget means the Segments together fit their budget.
	MergeReasonUnderBudget MergeReason = "under_budget"
	// MergeReasonNothingToMerge means fewer than two Segments, so none can be merged.
	MergeReasonNothingToMerge MergeReason = "nothing_to_merge"
	// MergeReasonRecapFailed means the Recap of the merge could not be written, and
	// nothing was merged.
	MergeReasonRecapFailed MergeReason = "recap_failed"
	// MergeReasonWriteFailed means the Record could not be written, and nothing was
	// merged.
	MergeReasonWriteFailed MergeReason = "write_failed"
)

// MergeDigestName is the Recap name recorded for a merge whose Recap is the
// digest built by code.
const MergeDigestName = "digest"

// MergeConfig controls MergeSegments.
type MergeConfig struct {
	// BudgetTokens is the most the Segments together may hold, in estimated
	// tokens. 0 means SegmentsBudget(CeilingTokens).
	BudgetTokens int
	// CeilingTokens is the ceiling in force, 0 for none, which sets the
	// default BudgetTokens and is recorded.
	CeilingTokens int
	// Recap writes the merged Recap when it is a MergeRecapWriter; any
	// other writer, or nil, leaves a digest built by code.
	Recap RecapWriter
	// Why says why the merge runs, for its Events; the default is
	// gotato.CompactionWhyPastBudget.
	Why gotato.CompactionWhy
	// Location is the time zone of the times the merged Segment shows.
	Location *time.Location
}

// MergeResult reports a MergeSegments call. Merged is true only when the
// Segments were merged; Reason says why not otherwise.
type MergeResult struct {
	Merged bool        `json:"merged"`
	Reason MergeReason `json:"reason,omitempty"`
	// Error says why the Recap or the write failed.
	Error string `json:"error,omitempty"`
	// BudgetTokens is the Segments' budget in force, in estimated tokens, and
	// SegmentTokens what the Segments held before.
	BudgetTokens  int `json:"budget_tokens"`
	SegmentTokens int `json:"segment_tokens"`
	// Segment is the number of the merged Segment, and Replaced the numbers
	// of the Segments it stands for.
	Segment  int   `json:"segment,omitempty"`
	Replaced []int `json:"replaced,omitempty"`
	// Recap names what wrote the merged Recap: the writer, or "digest".
	Recap      string        `json:"recap,omitempty"`
	RecapUsage *gotato.Usage `json:"recap_usage,omitempty"`
	// TokensBefore and TokensAfter estimate the whole Context around the
	// merge.
	TokensBefore int `json:"tokens_before,omitempty"`
	TokensAfter  int `json:"tokens_after,omitempty"`
}

// mergeSegments writes the merge. A test replaces it to make the write fail.
var mergeSegments = (*session.Record).MergeSegments

// MergeSegments merges the oldest Segments of the Context into one merged
// Segment when the Segments together pass their budget (cfg.BudgetTokens). It takes
// the oldest until the Segments left fit in half the budget, and at least two,
// and the merged Segment, which keeps their numbers in <segment n="1-3">, holds
// one Recap and opens the Context. If the Context already opens with a merged
// Segment, it is the first to merge, and its Recap goes into the new one. The
// Segments it replaces stay in the log, readable by their own numbers
// (ReadSegmentTool). The Segments left, and the tail, keep their bytes.
//
// The Recap comes from cfg.Recap when it is a MergeRecapWriter (ModelRecap
// asks the model from the Recaps alone, validated by the five-heading rules),
// else from mergeDigest. A Recap that fails, or a write that fails, leaves the
// Segments unmerged: the result says so, and the error is nil. Only a Run
// stopped during the merge returns an error. The merge is one Checkpoint, a
// compaction_started and a compaction_ended Event, or a compaction_failed.
//
// It must not run while a Run is using the Session.
func MergeSegments(ctx context.Context, s *session.Record, cfg MergeConfig) (MergeResult, error) {
	if s == nil {
		return MergeResult{}, errors.New("modelctx: session is nil")
	}
	budget := cfg.BudgetTokens
	if budget <= 0 {
		budget = SegmentsBudget(cfg.CeilingTokens)
	}
	result := MergeResult{BudgetTokens: budget}
	segments := s.Segments()
	count := pickOldest(segments, budget, &result)
	if count == 0 {
		return result, nil
	}
	replaced := segments[:count]

	why := cfg.Why
	if why == "" {
		why = gotato.CompactionWhyPastBudget
	}
	s.Emit(gotato.CompactionStarted{Header: sessionHeader(s), Why: why})

	recapName, text, recapErr, err := mergeRecap(ctx, s, cfg, replaced, why, &result)
	if err != nil {
		return result, err
	}
	if recapErr != nil {
		cause := gotato.CodeModelFailure
		if errors.Is(recapErr, ErrBadRecap) {
			cause = gotato.CodeModelProtocolFailure
		}
		return failMerge(s, why, result, MergeReasonRecapFailed, cause, recapErr), nil
	}
	return commitMerge(s, cfg, why, result, segments, count, recapName, text), nil
}

// pickOldest decides how many of the oldest segments to merge, for budget. It
// returns 0 and sets result.Reason when nothing merges; result.SegmentTokens
// is set either way.
func pickOldest(segments []gotato.Segment, budget int, result *MergeResult) int {
	sizes := make([]int, len(segments))
	for i, segment := range segments {
		sizes[i] = EstimateTokens(segment.Messages)
		result.SegmentTokens += sizes[i]
	}
	switch {
	case result.SegmentTokens <= budget:
		result.Reason = MergeReasonUnderBudget
		return 0
	case len(segments) < 2:
		result.Reason = MergeReasonNothingToMerge
		return 0
	}
	// The oldest merge until the Segments left fit in half the budget; at
	// least two, so a merge always shortens the Context.
	count, left := len(segments), 0
	for count > 2 && left+sizes[count-1] <= budget/2 {
		count--
		left += sizes[count]
	}
	return count
}

// mergeRecap writes the Recap of the merge: the model's when cfg.Recap is a
// MergeRecapWriter, else the digest. It records the model call's usage in
// result. recapErr is a Recap that failed or broke the rules, which leaves the
// Segments unmerged; err is a canceled ctx, after a compaction_failed Event.
func mergeRecap(ctx context.Context, s *session.Record, cfg MergeConfig, replaced []gotato.Segment, why gotato.CompactionWhy, result *MergeResult) (name, text string, recapErr, err error) {
	writer, ok := cfg.Recap.(MergeRecapWriter)
	if !ok || writer == nil {
		return MergeDigestName, mergeDigest(replaced, cfg.Location), nil, nil
	}
	message, writeErr := writer.WriteMergeRecap(ctx, MergeRequest{Segments: replaced, Location: cfg.Location})
	if ctxErr := ctx.Err(); ctxErr != nil {
		s.Emit(gotato.CompactionFailed{Header: sessionHeader(s), Why: why, Cause: causeOf(ctxErr)})
		return "", "", nil, ctxErr
	}
	if message.Usage != (gotato.Usage{}) {
		usage := message.Usage
		result.RecapUsage = &usage
	}
	if writeErr == nil {
		text, writeErr = ValidateRecap(gotato.TextOf(message))
	}
	if writeErr != nil {
		return "", "", writeErr, nil
	}
	return writer.Name(), text, nil, nil
}

// commitMerge builds the merged Segment from the first count segments and
// writes it with its Checkpoint. A write that fails leaves the Segments
// unmerged and says so in the result.
func commitMerge(s *session.Record, cfg MergeConfig, why gotato.CompactionWhy, result MergeResult, segments []gotato.Segment, count int, recapName, text string) MergeResult {
	replaced := segments[:count]
	before := s.Messages()
	merged := buildMerged(s.NextSegment(), replaced, text, cfg.Location)
	tail := s.Tail()
	for i := range tail {
		// The usage a reply reported was for a request that is no more.
		tail[i].Usage = gotato.Usage{}
	}
	after := append(append(slices.Clone(merged.Messages), flatten(segments[count:])...), tail...)
	beforeBytes, _ := json.Marshal(before)
	afterBytes, _ := json.Marshal(after)
	rendered := 0
	for _, segment := range replaced {
		rendered += len(segment.Messages)
	}
	record := session.Compaction{
		At:               time.Now().UTC(),
		ReplacedMessages: rendered,
		Recap:            recapName,
		RecapUsage:       result.RecapUsage,
		Segment:          merged.Number,
		Merged:           merged.Numbers(),
		TokensBefore:     EstimateTokens(before),
		TokensAfter:      EstimateTokens(after),
		BytesBefore:      len(beforeBytes),
		BytesAfter:       len(afterBytes),
		CeilingTokens:    cfg.CeilingTokens,
		LoadedTools:      s.LoadedTools(),
	}
	numbers := make([]int, count)
	for i, segment := range replaced {
		numbers[i] = segment.Number
	}
	if _, err := mergeSegments(s, why, merged, numbers, tail, &record); err != nil {
		return failMerge(s, why, result, MergeReasonWriteFailed, causeOf(err), err)
	}
	result.Merged, result.Reason = true, ""
	result.Segment, result.Replaced = merged.Number, merged.Numbers()
	result.Recap = recapName
	result.TokensBefore, result.TokensAfter = record.TokensBefore, record.TokensAfter
	return result
}

// failMerge emits the compaction_failed Event of a merge and returns result
// with the reason and the error text.
func failMerge(s *session.Record, why gotato.CompactionWhy, result MergeResult, reason MergeReason, cause gotato.ErrorCode, err error) MergeResult {
	s.Emit(gotato.CompactionFailed{Header: sessionHeader(s), Why: why, Cause: cause})
	result.Reason, result.Error = reason, err.Error()
	return result
}

// flatten returns the rendered Messages of segments, in order.
func flatten(segments []gotato.Segment) []gotato.Message {
	var out []gotato.Message
	for _, segment := range segments {
		out = append(out, segment.Messages...)
	}
	return out
}

// numbersOf lists the numbers the Segments stand for, oldest first.
func numbersOf(segments []gotato.Segment) []int {
	var out []int
	for _, segment := range segments {
		out = append(out, segment.Numbers()...)
	}
	return out
}

// rangeLabel writes numbers as ranges, such as "1-3" or "1-3,5": the label a
// merged Segment carries in its tags.
func rangeLabel(numbers []int) string {
	var parts []string
	for i := 0; i < len(numbers); {
		j := i
		for j+1 < len(numbers) && numbers[j+1] == numbers[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d-%d", numbers[i], numbers[j]))
		} else {
			parts = append(parts, fmt.Sprint(numbers[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// buildMerged renders the merged Segment number for segments: its header, then
// the Recap, in tags that carry the range it stands for. The Record freezes the
// Messages when it closes the Segment, so every instance sends the same bytes.
func buildMerged(number int, segments []gotato.Segment, recap string, loc *time.Location) gotato.Segment {
	if loc == nil {
		loc = time.UTC
	}
	merged := gotato.Segment{Number: number, Recap: recap}
	var tools []string
	for _, segment := range segments {
		if len(segment.Merged) > 0 {
			merged.Merged = append(merged.Merged, segment.Merged...)
		} else {
			merged.Merged = append(merged.Merged, gotato.MergedSegment{Number: segment.Number, From: segment.From, To: segment.To, Source: segment.Source})
		}
		if merged.From.IsZero() || (!segment.From.IsZero() && segment.From.Before(merged.From)) {
			merged.From = segment.From
		}
		if segment.To.After(merged.To) {
			merged.To = segment.To
		}
		for _, id := range segment.LoadedTools {
			if !slices.Contains(tools, id) {
				tools = append(tools, id)
			}
		}
	}
	merged.LoadedTools = tools
	first, last := merged.Merged[0].Source, merged.Merged[len(merged.Merged)-1].Source
	if first.SessionID == last.SessionID {
		merged.Source = gotato.SegmentSource{SessionID: first.SessionID, FromNumber: first.FromNumber, ToNumber: last.ToNumber}
	}

	label := rangeLabel(merged.Numbers())
	attrs := [][2]string{{"n", label}}
	if !merged.From.IsZero() {
		attrs = append(attrs, [2]string{"from", merged.From.In(loc).Format(time.RFC3339)})
	}
	if !merged.To.IsZero() {
		attrs = append(attrs, [2]string{"to", merged.To.In(loc).Format(time.RFC3339)})
	}
	var out messageList
	out.add(gotato.RoleUser, taggedPart(gotato.TagSegment, attrs,
		fmt.Sprintf("Segments %s, merged. Only their recap is kept here; the conversation, reasoning, intermediate\nsteps and tool results were left out. read_segment(n) returns each of them in full, for n in %s.", label, label)))
	out.add(gotato.RoleUser, taggedPart(gotato.TagRecap, [][2]string{{"n", label}}, text.EscapeTags(recap)))
	for i := range out.messages {
		out.messages[i].ID = gotato.MessageID(fmt.Sprintf("segment-%d-%d", number, i+1))
	}
	merged.Messages = out.messages
	return merged
}

// withoutHeading returns a Recap on one line, without the `## ` headings.
func withoutHeading(recap string) string {
	var kept []string
	for _, line := range strings.Split(recap, "\n") {
		if strings.HasPrefix(line, "## ") {
			continue
		}
		kept = append(kept, strings.TrimPrefix(strings.TrimSpace(line), "- "))
	}
	return oneLine(strings.Join(kept, " "))
}

// clip cuts s at limit characters with an ellipsis.
func clip(s string, limit int) string {
	if runes := []rune(s); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return s
}
