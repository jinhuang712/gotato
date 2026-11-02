package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
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
	// MergeReasonPickFailed means the Compactor's Picker failed, panicked or
	// returned a count the frame refuses (fewer than two Segments, or more than
	// there are), and nothing was merged.
	MergeReasonPickFailed MergeReason = "pick_failed"
)

// mergeDigestName is the Recap name recorded for a merge whose Recap is the
// digest built by code.
const mergeDigestName = "digest"

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

// mergeOldest merges the oldest Segments of the Context into one merged
// Segment when the Segments together pass budget, in estimated tokens (0 means
// segmentsBudget of the ceiling in force). The Compactor's Picker says how
// many of the oldest to take, two or more; the merged Segment, which keeps their
// numbers in <segment n="1-3">, holds one Recap and opens the Context. If the
// Context already opens with a merged Segment, it is the first to merge, and its
// Recap goes into the new one. The Segments it replaces stay in the log,
// readable by their own numbers (read_segment). The Segments left, and the
// tail, keep their bytes.
//
// The Recap comes from the Compactor's Recap when it is a MergeRecapWriter
// (ModelRecap asks the model from the Recaps alone, validated by the five-heading
// rules), else from mergeDigest. A Recap that fails, a Picker that fails or
// breaks the rules, or a write that fails, leaves the Segments unmerged: the
// result says so, and the error is nil. Only a Run stopped during the merge
// returns an error. The merge is one Checkpoint, a compaction_started and a
// compaction_ended Event, or a compaction_failed.
//
// It must not run while a Run is using the Session.
func mergeOldest(ctx context.Context, store SegmentStore, c Compactor, budget int, why gotato.CompactionWhy) (MergeResult, error) {
	if isNilStore(store) {
		return MergeResult{}, errors.New("record: store is nil")
	}
	if budget <= 0 {
		budget = segmentsBudget(c.EffectiveCeilingTokens())
	}
	if why == "" {
		why = gotato.CompactionWhyPastBudget
	}
	result := MergeResult{BudgetTokens: budget}
	segments := store.Segments()
	for _, segment := range segments {
		result.SegmentTokens += EstimateTokens(segment.Messages)
	}
	switch {
	case result.SegmentTokens <= budget:
		result.Reason = MergeReasonUnderBudget
		return result, nil
	case len(segments) < 2:
		result.Reason = MergeReasonNothingToMerge
		return result, nil
	}
	picker := c.picker()
	count, err := callPart("Picker", func() (int, error) { return picker.Pick(cloneSegments(segments), budget), nil })
	if err == nil && (count < 0 || count == 1 || count > len(segments)) {
		err = fmt.Errorf("%w: the Picker returned %d for %d Segments; a merge replaces two or more of the first", ErrBadPart, count, len(segments))
	}
	if err != nil {
		store.Emit(gotato.CompactionStarted{Header: sessionHeader(store), Why: why})
		return failMerge(store, why, result, MergeReasonPickFailed, causeOf(err), err), nil
	}
	if count == 0 {
		result.Reason = MergeReasonNothingToMerge
		return result, nil
	}
	replaced := segments[:count]

	store.Emit(gotato.CompactionStarted{Header: sessionHeader(store), Why: why})

	recapName, recap, recapErr, err := mergeRecap(ctx, store, c, cloneSegments(replaced), why, &result)
	if err != nil {
		return result, err
	}
	if recapErr != nil {
		cause := gotato.CodeModelFailure
		if errors.Is(recapErr, ErrBadRecap) {
			cause = gotato.CodeModelProtocolFailure
		}
		return failMerge(store, why, result, MergeReasonRecapFailed, cause, recapErr), nil
	}
	return commitMerge(store, c, why, result, segments, count, recapName, recap), nil
}

// cloneSegments copies Segments deeply, so a part cannot change what the frame
// keeps.
func cloneSegments(segments []gotato.Segment) []gotato.Segment {
	out := make([]gotato.Segment, len(segments))
	for i, segment := range segments {
		out[i] = segment.Clone()
	}
	return out
}

// mergeRecap writes the Recap of the merge: the model's when the Compactor's
// Recap is a MergeRecapWriter, else the digest. It records the model call's
// usage in result. recapErr is a Recap that failed or broke the rules, which
// leaves the Segments unmerged; err is a canceled ctx, after a
// compaction_failed Event.
func mergeRecap(ctx context.Context, store SegmentStore, c Compactor, replaced []gotato.Segment, why gotato.CompactionWhy, result *MergeResult) (name, recap string, recapErr, err error) {
	writer, ok := c.Recap.(MergeRecapWriter)
	if !ok {
		return mergeDigestName, mergeDigest(replaced, c.Location), nil, nil
	}
	message, writeErr := guardedCall2(ctx, writer.WriteMergeRecap, MergeRequest{Segments: replaced, Location: c.Location})
	if ctxErr := ctx.Err(); ctxErr != nil {
		store.Emit(gotato.CompactionFailed{Header: sessionHeader(store), Why: why, Cause: causeOf(ctxErr)})
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
func commitMerge(store SegmentStore, c Compactor, why gotato.CompactionWhy, result MergeResult, segments []gotato.Segment, count int, recapName, recap string) MergeResult {
	replaced := segments[:count]
	tail := store.Tail()
	before := slices.Concat(flatten(segments), tail)
	merged := buildMerged(store.NextSegment(), replaced, recap, c.Location)
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
	rec := gotato.Compaction{
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
		CeilingTokens:    c.EffectiveCeilingTokens(),
		LoadedTools:      store.LoadedTools(),
	}
	numbers := make([]int, count)
	for i, segment := range replaced {
		numbers[i] = segment.Number
	}
	if _, err := callStore("SegmentStore.MergeSegments", func() (gotato.Segment, error) {
		return store.MergeSegments(why, merged, numbers, tail, &rec)
	}); err != nil {
		return failMerge(store, why, result, MergeReasonWriteFailed, causeOf(err), err)
	}
	result.Merged, result.Reason = true, ""
	result.Segment, result.Replaced = merged.Number, merged.Numbers()
	result.Recap = recapName
	result.TokensBefore, result.TokensAfter = rec.TokensBefore, rec.TokensAfter
	return result
}

// failMerge emits the compaction_failed Event of a merge and returns result
// with the reason and the error text.
func failMerge(store SegmentStore, why gotato.CompactionWhy, result MergeResult, reason MergeReason, cause gotato.ErrorCode, err error) MergeResult {
	store.Emit(gotato.CompactionFailed{Header: sessionHeader(store), Why: why, Cause: cause})
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
	switch reason {
	case MergeReasonRecapFailed:
		fallback = "the Recap failed"
	case MergeReasonPickFailed:
		fallback = "the Picker failed"
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
