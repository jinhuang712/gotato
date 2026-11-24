package record

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/ref"
)

// readSegmentInput is the input of read_segment.
type readSegmentInput struct {
	Segment int `json:"segment" description:"the number of the segment, as in <segment n=\"3\">; for a merged segment such as n=\"1-3\", the number of one of the segments it merged"`
	Page    int `json:"page,omitempty" description:"the page to read, from 1; a long segment is read page by page"`
}

// segmentTool returns the read_segment tool. It reads a Segment's Messages
// back from the Originals, tool calls and tool results included, so what a
// compaction left out of the Context is still there to read, and a Segment
// that was merged stays readable by its own number. Its Effect is read, so
// every Effect cap keeps it.
//
// The result is text that carries none of the four tags: a prefix says whose
// each Message is, such as `[user]`, `[assistant]`, `[call payments.get_batch]`
// and `[tool payments.get_batch]`; a line of the text that itself starts with
// such a prefix is escaped with a backslash, and the tags in it are escaped as
// in any Tool Result, so neither a user nor a tool can speak as the assistant
// or the system in it. It is cut into pages of PageBytes. When the store no
// longer holds the Messages (the team pruned it), the result says so.
func (r ReadBack) segmentTool() gotato.Tool {
	tool, err := gotato.NewFuncTool(ref.ReadSegment,
		"Returns the messages of a segment of this conversation word for word, tool calls and tool results included, as they were before the segment was closed or merged. "+
			"Use it when a recap leaves out a detail you need, such as an ID or a number a tool returned.",
		r.readSegment)
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.ToolEffectRead)
}

// segmentRef says where one Segment's originals are.
type segmentRef struct {
	number   int
	from, to time.Time
	source   gotato.SegmentSource
}

// refsOf lists, oldest first, every Segment the Context stands for, with
// where its Messages are: the Segments it holds, and those a merged Segment
// stands for.
func refsOf(segments []gotato.Segment) (refs []segmentRef, merged map[int][]int) {
	merged = map[int][]int{}
	for _, segment := range segments {
		if len(segment.Merged) == 0 {
			refs = append(refs, segmentRef{segment.Number, segment.From, segment.To, segment.Source})
			continue
		}
		merged[segment.Number] = segment.Numbers()
		for _, part := range segment.Merged {
			refs = append(refs, segmentRef{part.Number, part.From, part.To, part.Source})
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].number < refs[j].number })
	return refs, merged
}

func (r ReadBack) readSegment(ctx context.Context, in readSegmentInput) (string, error) {
	found, err := r.findRef(in.Segment)
	if err != nil {
		return "", err
	}
	page := max(in.Page, 1)

	messages, complete, err := r.readOriginals(ctx, in.Segment)
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return fmt.Sprintf("The messages of segment %d were removed from the Record, so only what the conversation keeps of it is left: its recap and its tool call lines.", in.Segment), nil
	}

	loc := r.Location
	if loc == nil {
		loc = time.UTC
	}
	limit := r.PageBytes()
	pages := text.Paginate(renderOriginals(messages, limit/2), limit)
	if page > len(pages) {
		return "", readErrorf("segment %d has %d pages; there is no page %d", in.Segment, len(pages), page)
	}

	var b strings.Builder
	writeRefHeading(&b, in.Segment, found, loc)
	fmt.Fprintf(&b, ". Its messages as they were, tool calls and tool results included; reasoning is left out. Page %d of %d.\n", page, len(pages))
	if !complete {
		b.WriteString("Some of this segment's messages were removed from the Record, so the first ones may be missing.\n")
	}
	b.WriteString(strings.Join(pages[page-1], "\n"))
	if page < len(pages) {
		fmt.Fprintf(&b, "\nMore: %s(segment=%d, page=%d).", ref.ReadSegment, in.Segment, page+1)
	} else {
		fmt.Fprintf(&b, "\nEnd of segment %d.", in.Segment)
	}
	return b.String(), nil
}

// findRef is the Segment numbered segment among those the Context stands
// for, or the error that tells the model which numbers it can read. It is nil,
// with no error, when the Segments are not listed.
func (r ReadBack) findRef(segment int) (*segmentRef, error) {
	// With the Segments listed, a number no Segment has is answered with the
	// numbers one can read, and a merged Segment's own number with the ones it
	// stands for.
	var found *segmentRef
	segments, listed := r.segments()
	refs, merged := refsOf(segments)
	if !listed {
		return nil, nil
	}
	for i := range refs {
		if refs[i].number == segment {
			found = &refs[i]
		}
	}
	if found != nil {
		return found, nil
	}
	numbers := make([]int, len(refs))
	for i, at := range refs {
		numbers[i] = at.number
	}
	if standsFor, ok := merged[segment]; ok {
		return nil, readErrorf("segment %d is the merge of segments %s; read one of them by its own number", segment, rangeLabel(standsFor))
	}
	return nil, readErrorf("there is no segment %d in this conversation; the segments you can read are %s", segment, rangeLabel(numbers))
}

// readOriginals reads the Messages of the Segment numbered segment back from
// the Originals, and whether they are all there; none when they were removed.
func (r ReadBack) readOriginals(ctx context.Context, segment int) ([]gotato.Message, bool, error) {
	messages, complete, err := r.Originals.Segment(ctx, segment)
	switch {
	case errors.Is(err, ErrNoOriginal):
		return nil, false, readErrorf("there is no segment %d in this conversation", segment)
	case err != nil && !errors.Is(err, ErrRemoved):
		return nil, false, readErrorf("segment %d could not be read", segment)
	}
	return messages, complete, nil
}

// writeRefHeading writes the number of the Segment and, when found knows
// them, the times it covers.
func writeRefHeading(b *strings.Builder, segment int, found *segmentRef, loc *time.Location) {
	fmt.Fprintf(b, "Segment %d", segment)
	if found != nil && !found.from.IsZero() {
		fmt.Fprintf(b, ", %s", found.from.In(loc).Format(time.RFC3339))
		if !found.to.IsZero() {
			fmt.Fprintf(b, " to %s", found.to.In(loc).Format(time.RFC3339))
		}
	}
}

// rolePrefix matches the start of a line that looks like the prefix of a
// Message: a bracketed role (or the kind of a call, a tool result, a notice or
// a tag word), after any backslashes an earlier escape put there.
var rolePrefix = regexp.MustCompile(`(?i)^(\\*)\[(user|assistant|system|developer|notice|call|tool|segment|recap|page)\b[^\]\n]*\]`)

// escapeLine makes a line of someone's text safe to put after a prefix: the
// start or end of one of the four tags, and a prefix that imitates a Message's
// own, which gets a backslash in front of it.
func escapeLine(line string) string {
	line = text.EscapeTags(line)
	if rolePrefix.MatchString(line) {
		return `\` + line
	}
	return line
}

// renderOriginals writes Messages as lines with a prefix on the first line of
// each, such as `[user] text`, with the text lines after it escaped. A line
// longer than maxLine bytes is cut into lines of that length.
func renderOriginals(messages []gotato.Message, maxLine int) []string {
	callTool := map[gotato.ToolCallID]string{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			callTool[call.ID] = call.ToolID
		}
	}
	out := originalLines{maxLine: maxLine}
	for _, message := range messages {
		if message.Role == gotato.RoleToolResult {
			out.toolResult(message, callTool)
			continue
		}
		out.message(message)
	}
	return out.lines
}

// originalLines collects the lines renderOriginals writes.
type originalLines struct {
	lines   []string
	maxLine int
}

// emit writes body after label, cut into lines of maxLine bytes, each escaped.
func (o *originalLines) emit(label, body string) {
	parts := text.SplitLines(body, o.maxLine)
	if len(parts) == 0 {
		o.lines = append(o.lines, label)
		return
	}
	o.lines = append(o.lines, label+" "+escapeLine(parts[0]))
	for _, part := range parts[1:] {
		o.lines = append(o.lines, escapeLine(part))
	}
}

// toolResult writes a tool result Message under the name of the tool its call named.
func (o *originalLines) toolResult(message gotato.Message, callTool map[gotato.ToolCallID]string) {
	if message.ToolResult == nil {
		return
	}
	result := message.ToolResult
	label := "[tool " + toolLabel(callTool[result.CallID], string(result.CallID))
	if result.Status != gotato.ToolResultStatusOK {
		label += " " + oneLine(string(result.Status))
	}
	label += "]"
	o.emit(label, toolResultText(message))
}

// message writes each Part with text under its role, then each tool call.
func (o *originalLines) message(message gotato.Message) {
	for _, part := range message.Parts {
		body := textOfPart(part)
		if body == "" {
			continue
		}
		label := "[" + string(message.Role) + "]"
		if part.Metadata[gotato.MetaTag] == gotato.TagNotice {
			label = "[notice]"
		}
		o.emit(label, body)
	}
	for _, call := range message.ToolCalls {
		o.emit("[call "+toolLabel(call.ToolID, string(call.ID))+"]", cutArgsFull(call.Arguments))
	}
}

// toolResultText is the text of a Tool Result Message: its content, or its
// Parts when it has no content, and the error text a failed call left.
func toolResultText(message gotato.Message) string {
	result := message.ToolResult
	if result == nil {
		return ""
	}
	var text []string
	for _, part := range result.Content {
		if t := textOfPart(part); t != "" {
			text = append(text, t)
		}
	}
	if len(result.Content) == 0 {
		for _, part := range message.Parts {
			if t := textOfPart(part); t != "" {
				text = append(text, t)
			}
		}
	}
	if result.SafeError != "" {
		text = append(text, result.SafeError)
	}
	return strings.Join(text, "\n")
}

// toolLabel is how a tool is named after a prefix: its ID with the text kept
// to one line, so the label cannot hold a line of its own.
func toolLabel(toolID, callID string) string {
	if toolID == "" {
		toolID = "call " + callID
	}
	return text.EscapeTags(strings.NewReplacer("]", ")", "\n", " ").Replace(oneLine(toolID)))
}

// textOfPart is the text of a Part, with a placeholder for an image or a file;
// reasoning has none.
func textOfPart(part gotato.ContentPart) string {
	switch {
	case part.Kind == gotato.ContentKindReasoning:
		return ""
	case isText(part):
		return part.Text
	default:
		return placeholder(part).Text
	}
}

// cutArgsFull is a call's arguments as compact JSON on one line, whole.
func cutArgsFull(arguments []byte) string {
	return cutArgs(arguments, 1<<30)
}
