package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// DefaultSegmentsBudget is the Segments' budget with no ceiling: the most the
// Segments together may hold, in estimated tokens (bytes ÷ 4), when the model
// has no known window and neither the Agent nor the Session sets a ceiling.
// With a ceiling, the budget is half of it (SegmentsBudget).
const DefaultSegmentsBudget = 32000

// SegmentsBudget returns the most the Segments of a Context may hold, in
// estimated tokens, under the ceiling in force: half of it, or
// DefaultSegmentsBudget when there is no ceiling (ceiling 0). When the
// Segments pass it, the oldest are merged (MergeSegments).
func SegmentsBudget(ceiling int) int {
	if ceiling > 0 {
		return max(ceiling/2, 1)
	}
	return DefaultSegmentsBudget
}

// MergeRecapWriter is a RecapWriter that also writes the Recap of a merge of
// the oldest Segments, from their Recaps alone. A writer that is not one, such
// as NoRecap and TruncateRecap, leaves the merged Segment with a digest built
// by code (MergeDigest). ModelRecap is one.
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

// Why a merge did nothing, or failed, in MergeResult.Reason.
const (
	// MergeUnderBudget: the Segments together fit their budget.
	MergeUnderBudget = "under_budget"
	// MergeNothingToMerge: fewer than two Segments, so none can be merged.
	MergeNothingToMerge = "nothing_to_merge"
	// MergeRecapFailed: the Recap of the merge could not be written, and
	// nothing was merged.
	MergeRecapFailed = "recap_failed"
	// MergeWriteFailed: the Record could not be written, and nothing was
	// merged.
	MergeWriteFailed = "write_failed"
)

// MergeDigestName is the Recap name recorded for a merge whose Recap is the
// digest built by code.
const MergeDigestName = "digest"

// MergeOptions controls MergeSegments.
type MergeOptions struct {
	// Budget is the most the Segments together may hold, in estimated
	// tokens. 0 means SegmentsBudget(Ceiling).
	Budget int
	// Ceiling is the ceiling in force, 0 for none, which sets the default
	// Budget and is recorded.
	Ceiling int
	// Recap writes the merged Recap when it is a MergeRecapWriter; any
	// other writer, or nil, leaves a digest built by code.
	Recap RecapWriter
	// Why says why the merge runs, for its Events; the default is
	// gotato.CompactionPastBudget.
	Why gotato.CompactionWhy
	// Location is the time zone of the times the merged Segment shows.
	Location *time.Location
	// Now overrides the clock (tests).
	Now func() time.Time
}

// MergeResult reports a MergeSegments call. Merged is true only when the
// Segments were merged; Reason says why not otherwise.
type MergeResult struct {
	Merged bool   `json:"merged"`
	Reason string `json:"reason,omitempty"`
	// Error says why the Recap or the write failed.
	Error string `json:"error,omitempty"`
	// Budget is the Segments' budget in force, in estimated tokens, and
	// SegmentTokens what the Segments held before.
	Budget        int `json:"budget"`
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
// Segment when the Segments together pass their budget (opts.Budget). It takes
// the oldest until the Segments left fit in half the budget, and at least two,
// and the merged Segment, which keeps their numbers in <segment n="1-3">, holds
// one Recap and opens the Context. If the Context already opens with a merged
// Segment, it is the first to merge, and its Recap goes into the new one. The
// Segments it replaces stay in the log, readable by their own numbers
// (ReadSegmentTool). The Segments left, and the tail, keep their bytes.
//
// The Recap comes from opts.Recap when it is a MergeRecapWriter (ModelRecap
// asks the model from the Recaps alone, validated by the five-heading rules),
// else from MergeDigest. A Recap that fails, or a write that fails, leaves the
// Segments unmerged: the result says so, and the error is nil. Only a Run
// stopped during the merge returns an error. The merge is one Checkpoint, a
// compaction_started and a compaction_ended Event, or a compaction_failed.
//
// It must not run while a Run is using the Session.
func MergeSegments(ctx context.Context, s *session.Record, opts MergeOptions) (MergeResult, error) {
	if s == nil {
		return MergeResult{}, errors.New("modelctx: session is nil")
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = SegmentsBudget(opts.Ceiling)
	}
	result := MergeResult{Budget: budget}
	segments := s.Segments()
	sizes := make([]int, len(segments))
	for i, segment := range segments {
		sizes[i] = EstimateTokens(segment.Messages)
		result.SegmentTokens += sizes[i]
	}
	switch {
	case result.SegmentTokens <= budget:
		result.Reason = MergeUnderBudget
		return result, nil
	case len(segments) < 2:
		result.Reason = MergeNothingToMerge
		return result, nil
	}
	// The oldest merge until the Segments left fit in half the budget; at
	// least two, so a merge always shortens the Context.
	count, left := len(segments), 0
	for count > 2 && left+sizes[count-1] <= budget/2 {
		count--
		left += sizes[count]
	}
	replaced := segments[:count]

	why := opts.Why
	if why == "" {
		why = gotato.CompactionPastBudget
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	header := func() gotato.Header { return gotato.NewHeader(gotato.SessionID(s.ID()), "", 0) }
	s.Emit(gotato.CompactionStarted{Header: header(), Why: why})
	fail := func(reason string, cause gotato.ErrorCode, err error) MergeResult {
		s.Emit(gotato.CompactionFailed{Header: header(), Why: why, Cause: cause})
		result.Reason, result.Error = reason, err.Error()
		return result
	}

	recapName, text := MergeDigestName, ""
	if writer, ok := opts.Recap.(MergeRecapWriter); ok && writer != nil {
		message, err := writer.WriteMergeRecap(ctx, MergeRequest{Segments: replaced, Location: opts.Location})
		if ctxErr := ctx.Err(); ctxErr != nil {
			s.Emit(gotato.CompactionFailed{Header: header(), Why: why, Cause: causeOf(ctxErr)})
			return result, ctxErr
		}
		if message.Usage != (gotato.Usage{}) {
			usage := message.Usage
			result.RecapUsage = &usage
		}
		if err == nil {
			text, err = ValidateRecap(gotato.TextOf(message))
		}
		if err != nil {
			cause := gotato.ErrModelFailure
			if errors.Is(err, ErrBadRecap) {
				cause = gotato.ErrModelProtocolFailure
			}
			return fail(MergeRecapFailed, cause, err), nil
		}
		recapName = writer.Name()
	} else {
		text = MergeDigest(replaced, opts.Location)
	}

	before := s.Messages()
	merged := buildMerged(s.NextSegment(), replaced, text, opts.Location)
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
		At:               now().UTC(),
		ReplacedMessages: rendered,
		Recap:            recapName,
		RecapUsage:       result.RecapUsage,
		Segment:          merged.Number,
		Merged:           merged.Numbers(),
		TokensBefore:     EstimateTokens(before),
		TokensAfter:      EstimateTokens(after),
		BytesBefore:      len(beforeBytes),
		BytesAfter:       len(afterBytes),
		Ceiling:          opts.Ceiling,
		Tools:            s.LoadedTools(),
	}
	numbers := make([]int, count)
	for i, segment := range replaced {
		numbers[i] = segment.Number
	}
	if _, err := mergeSegments(s, why, merged, numbers, tail, &record); err != nil {
		return fail(MergeWriteFailed, causeOf(err), err), nil
	}
	result.Merged, result.Reason = true, ""
	result.Segment, result.Replaced = merged.Number, merged.Numbers()
	result.Recap = recapName
	result.TokensBefore, result.TokensAfter = record.TokensBefore, record.TokensAfter
	return result, nil
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
		for _, id := range segment.Tools {
			if !slices.Contains(tools, id) {
				tools = append(tools, id)
			}
		}
	}
	merged.Tools = tools
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
	out.add(gotato.RoleUser, taggedPart(gotato.TagRecap, [][2]string{{"n", label}}, gotato.EscapeTags(recap)))
	for i := range out.messages {
		out.messages[i].ID = gotato.MessageID(fmt.Sprintf("segment-%d-%d", number, i+1))
	}
	merged.Messages = out.messages
	return merged
}

// The sizes of a digest built by code (MergeDigest), in bytes.
const (
	digestMaxBytes     = 4000
	digestUserChars    = 160
	digestUsersPerSeg  = 3
	digestRecapChars   = 300
	digestMergedChars  = 1200
	digestToolsPerLine = 5
)

// MergeDigest is the Recap of a merge when no model writes it: a digest built
// by code, deterministic, under `## What happened`, so it passes the same rules
// as a Recap the model writes. For each Segment it has one line with its times,
// the start of its user Messages, its tool calls counted by tool, and its own
// Recap cut short; a merged Segment among them contributes the start of its
// Recap. The whole is cut at 4,000 bytes by dropping the oldest lines.
func MergeDigest(segments []gotato.Segment, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	lines := make([]string, 0, len(segments))
	for _, segment := range segments {
		lines = append(lines, digestLine(segment, loc))
	}
	const heading = "## What happened\n"
	dropped := 0
	for len(lines) > 1 && len(heading)+len(strings.Join(lines, "\n")) > digestMaxBytes {
		lines = lines[1:]
		dropped++
	}
	var b strings.Builder
	b.WriteString(heading)
	if dropped > 0 {
		fmt.Fprintf(&b, "- … %d earlier lines left out; read_segment returns the Segments.\n", dropped)
	}
	b.WriteString(strings.Join(lines, "\n"))
	return cutRunes(b.String(), digestMaxBytes+120)
}

func digestLine(segment gotato.Segment, loc *time.Location) string {
	label := "Segment " + rangeLabel(segment.Numbers())
	if len(segment.Numbers()) > 1 {
		label = "Segments " + rangeLabel(segment.Numbers()) + " (merged earlier)"
	}
	span := ""
	if !segment.From.IsZero() {
		span = ", " + segment.From.In(loc).Format("2006-01-02 15:04")
		if !segment.To.IsZero() {
			span += " to " + segment.To.In(loc).Format("2006-01-02 15:04")
		}
	}
	line := "- " + label + span + ":"
	if len(segment.Merged) > 0 {
		return line + " " + clip(withoutHeading(segment.Recap), digestMergedChars)
	}
	if users := userTexts(segment); len(users) > 0 {
		shown := users[:min(len(users), digestUsersPerSeg)]
		for i := range shown {
			shown[i] = `"` + clip(shown[i], digestUserChars) + `"`
		}
		line += " the user wrote " + strings.Join(shown, " / ")
		if more := len(users) - len(shown); more > 0 {
			line += fmt.Sprintf(" / and %d more.", more)
		} else {
			line += "."
		}
	}
	if tools := toolCounts(segment.ToolCalls); tools != "" {
		line += " " + tools + "."
	}
	if recap := withoutHeading(segment.Recap); recap != "" {
		line += " Its recap: " + clip(recap, digestRecapChars)
	}
	return line
}

// userTexts returns the user's own words in a Segment, one per Message: its
// untagged user text.
func userTexts(segment gotato.Segment) []string {
	var out []string
	for _, message := range segment.Messages {
		if message.Role != gotato.RoleUser {
			continue
		}
		var parts []string
		for _, part := range message.Parts {
			if _, tagged := part.Metadata[gotato.MetaTag]; !tagged && part.Kind == gotato.ContentText && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
		if text := oneLine(strings.Join(parts, " ")); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// toolCounts writes a Segment's tool call lines as a count by tool.
func toolCounts(lines []gotato.ToolCallLine) string {
	counts := map[string]int{}
	total := 0
	for _, line := range lines {
		n := max(line.Count, 1)
		counts[line.ToolID] += n
		total += n
	}
	if total == 0 {
		return ""
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	shown := names[:min(len(names), digestToolsPerLine)]
	parts := make([]string, len(shown))
	for i, name := range shown {
		parts[i] = fmt.Sprintf("%s ×%d", name, counts[name])
	}
	if len(names) > len(shown) {
		parts = append(parts, "…")
	}
	return fmt.Sprintf("%d tool calls: %s", total, strings.Join(parts, ", "))
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
