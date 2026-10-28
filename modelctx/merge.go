package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/session"
)

// MergeRecapWriter is a RecapWriter that also writes the Recap of a merge of
// the oldest Segments, from their Recaps alone. A writer that is not one, such
// as TruncateRecap, leaves the merged Segment with a digest built
// by code (mergeDigest). ModelRecap is one.
// [SEAM] a Recap writer opts in to writing merged Recaps; ModelRecap is the one that does.
type MergeRecapWriter interface {
	RecapWriter
	WriteMergeRecap(context.Context, MergeRequest) (gotato.Message, error)
}

// MergeReason says why a merge did nothing, or failed, in MergeResult.Reason.
type MergeReason string

const (
	// MergeReasonUnderBudget means the Segments together fit their budget.
	MergeReasonUnderBudget MergeReason = "under_budget"
	// MergeReasonNothingToMerge means fewer than two Segments, so none can be
	// merged.
	MergeReasonNothingToMerge MergeReason = "nothing_to_merge"
	// MergeReasonRecapFailed means the Recap of the merge could not be written,
	// and nothing was merged.
	MergeReasonRecapFailed MergeReason = "recap_failed"
	// MergeReasonWriteFailed means the Record could not be written, and nothing
	// was merged.
	MergeReasonWriteFailed MergeReason = "write_failed"
)

// mergeDigestName is the Recap name recorded for a merge whose Recap is the
// digest built by code.
const mergeDigestName = "digest"

// MergeConfig controls a merge of the oldest Segments.
type MergeConfig struct {
	// BudgetTokens is the most the Segments together may hold, in estimated
	// tokens. 0 means segmentsBudget(CeilingTokens).
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

// MergeResult reports a merge. Merged is true only when the
// Segments were merged; Reason says why not otherwise.
type MergeResult struct {
	Merged bool        `json:"merged"`
	Reason MergeReason `json:"reason,omitempty"`
	// Error says why the Recap or the write failed, in text a client may read:
	// a foreign error is not copied into it.
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

// mergeSegments writes the merge. A test replaces it to make the write fail:
// a Record's own writes cannot fail in memory, and Compact reaches this call
// too, so the seam is the one place a test can fault both paths.
var mergeSegments = (*session.Record).MergeSegments

// mergeOldest merges the oldest Segments of the Context into one merged
// Segment when the Segments together pass their budget (cfg.BudgetTokens). It
// takes the oldest until the Segments left fit in half the budget, and at least
// two, and the merged Segment, which keeps their numbers in <segment n="1-3">,
// holds one Recap and opens the Context. If the Context already opens with a
// merged Segment, it is the first to merge, and its Recap goes into the new
// one. The Segments it replaces stay in the log, readable by their own numbers
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
func mergeOldest(ctx context.Context, s *session.Record, cfg MergeConfig) (MergeResult, error) {
	if s == nil {
		return MergeResult{}, errors.New("modelctx: session is nil")
	}
	budget := cfg.BudgetTokens
	if budget <= 0 {
		budget = segmentsBudget(cfg.CeilingTokens)
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

	recapName, recap, recapErr, err := mergeRecap(ctx, s, cfg, replaced, why, &result)
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
	return commitMerge(s, cfg, why, result, segments, count, recapName, recap), nil
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
func mergeRecap(ctx context.Context, s *session.Record, cfg MergeConfig, replaced []gotato.Segment, why gotato.CompactionWhy, result *MergeResult) (name, recap string, recapErr, err error) {
	writer, ok := cfg.Recap.(MergeRecapWriter)
	if !ok {
		return mergeDigestName, mergeDigest(replaced, cfg.Location), nil, nil
	}
	message, writeErr := guardedCall2(ctx, writer.WriteMergeRecap, MergeRequest{Segments: replaced, Location: cfg.Location})
	if ctxErr := ctx.Err(); ctxErr != nil {
		s.Emit(gotato.CompactionFailed{Header: sessionHeader(s), Why: why, Cause: causeOf(ctxErr)})
		return "", "", nil, ctxErr
	}
	if message.Usage != (gotato.Usage{}) {
		usage := message.Usage
		result.RecapUsage = &usage
	}
	if writeErr == nil {
		recap, writeErr = ValidateRecap(gotato.TextOf(message))
	}
	if writeErr != nil {
		return "", "", writeErr, nil
	}
	return writer.Name(), recap, nil, nil
}

// commitMerge builds the merged Segment from the first count segments and
// writes it with its Checkpoint. A write that fails leaves the Segments
// unmerged and says so in the result.
func commitMerge(s *session.Record, cfg MergeConfig, why gotato.CompactionWhy, result MergeResult, segments []gotato.Segment, count int, recapName, recap string) MergeResult {
	replaced := segments[:count]
	before := s.Messages()
	merged := buildMerged(s.NextSegment(), replaced, recap, cfg.Location)
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
	record := gotato.Compaction{
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
	result.Reason, result.Error = reason, publicMergeError(reason, err)
	return result
}

// publicMergeError is the text of a failed merge that MergeResult carries to
// a caller, and from there to a client. A Recap writer and a Store are
// foreign, and their errors may hold a host, a query or a secret, so only what
// the error chooses to make public is kept: its PublicError text, or a
// RuntimeError, whose Message is written to be read. Any other error is a
// fixed text for its reason; the error itself is not kept.
// [SECURITY] a Recap writer's or Store's error can carry a host, a query or a secret that a client must not read.
func publicMergeError(reason MergeReason, err error) string {
	fallback := "the merge could not be written"
	if reason == MergeReasonRecapFailed {
		fallback = "the Recap failed"
	}
	var runtime *gotato.RuntimeError
	if errors.As(err, &runtime) && runtime != nil {
		fallback = runtime.Error()
	}
	return gotato.PublicText(err, fallback)
}

// flatten returns the rendered Messages of segments, in order.
func flatten(segments []gotato.Segment) []gotato.Message {
	var out []gotato.Message
	for _, segment := range segments {
		out = append(out, segment.Messages...)
	}
	return out
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
	// [DATA-LOSS] a stretch that spans two Records, or two epochs of one, cannot be read back as one source.
	if first.RecordRef == last.RecordRef {
		merged.Source = gotato.SegmentSource{RecordRef: first.RecordRef, FromNumber: first.FromNumber, ToNumber: last.ToNumber}
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
