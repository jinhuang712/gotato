package modelctx

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// ReadSegmentID is the ID of the built-in tool that reads a Segment back.
const ReadSegmentID = "read_segment"

// DefaultReadSegmentPageBytes is the most one page of a read_segment result
// holds, in bytes of text: about 8K tokens, far below MaxToolResultBytes, so a
// long Segment is read page by page and one call never fills the Context.
const DefaultReadSegmentPageBytes = 32 << 10

// ReadSegmentOptions controls ReadSegmentTool.
type ReadSegmentOptions struct {
	// PageBytes is the most one page holds, in bytes. 0 means
	// DefaultReadSegmentPageBytes. A Runner sets it lower when the Agent's
	// MaxToolResultBytes leaves less room.
	PageBytes int
	// Location is the time zone of the times in the result; nil means UTC.
	Location *time.Location
}

func (o ReadSegmentOptions) pageBytes() int {
	if o.PageBytes <= 0 {
		return DefaultReadSegmentPageBytes
	}
	return max(o.PageBytes, 256)
}

// SegmentTools is the gotato.ToolSource that gives a Session the built-in
// read_segment tool once it has a Segment, and no tool before: the tool list
// comes from the Record's last Checkpoint, so every instance that loads the
// Session offers the same one. log is where the Messages a Segment covers are
// read back from, the Store of the Record.
func SegmentTools(record *session.Record, log session.EntryLog, opts ReadSegmentOptions) gotato.ToolSource {
	return segmentTools{record: record, tool: ReadSegmentTool(record, log, opts)}
}

type segmentTools struct {
	record *session.Record
	tool   gotato.Tool
}

// Tools implements gotato.ToolSource.
func (s segmentTools) Tools() []gotato.Tool {
	if s.record == nil || len(s.record.Segments()) == 0 {
		return nil
	}
	return []gotato.Tool{s.tool}
}

// readSegmentInput is the input of read_segment.
type readSegmentInput struct {
	Segment int `json:"segment" description:"the number of the segment, as in <segment n=\"3\">; for a merged segment such as n=\"1-3\", the number of one of the segments it merged"`
	Page    int `json:"page,omitempty" description:"the page to read, from 1; a long segment is read page by page"`
}

// ReadSegmentTool returns the read_segment tool for record. It reads a
// Segment's Messages back from the log by the range of Entries the Segment
// records, tool calls and tool results included, so what a compaction left out
// of the Context is still there to read, and a Segment that was merged stays
// readable by its own number. Its Effect is read, so every Effect cap keeps it.
//
// The result is text that carries none of the four tags: a prefix says whose
// each Message is, such as `[user]`, `[assistant]`, `[call payments.get_batch]`
// and `[tool payments.get_batch]`; a line of the text that itself starts with
// such a prefix is escaped with a backslash, and the tags in it are escaped as
// in any Tool Result, so neither a user nor a tool can speak as the assistant
// or the system in it. It is cut into pages of opts.PageBytes. When the log no
// longer holds the Entries (the team pruned it), the result says so.
func ReadSegmentTool(record *session.Record, log session.EntryLog, opts ReadSegmentOptions) gotato.Tool {
	tool, err := gotato.NewFuncTool(ReadSegmentID,
		"Returns the messages of a segment of this conversation word for word, tool calls and tool results included, as they were before the segment was closed or merged. "+
			"Use it when a recap leaves out a detail you need, such as an ID or a number a tool returned.",
		func(ctx context.Context, in readSegmentInput) (string, error) {
			return readSegment(ctx, record, log, opts, in)
		})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.EffectRead)
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

func readSegment(ctx context.Context, record *session.Record, log session.EntryLog, opts ReadSegmentOptions, in readSegmentInput) (string, error) {
	refs, merged := refsOf(record.Segments())
	var ref *segmentRef
	for i := range refs {
		if refs[i].number == in.Segment {
			ref = &refs[i]
		}
	}
	if ref == nil {
		numbers := make([]int, len(refs))
		for i, r := range refs {
			numbers[i] = r.number
		}
		if standsFor, ok := merged[in.Segment]; ok {
			return "", fmt.Errorf("segment %d is the merge of segments %s; read one of them by its own number", in.Segment, rangeLabel(standsFor))
		}
		return "", fmt.Errorf("there is no segment %d in this conversation; the segments you can read are %s", in.Segment, rangeLabel(numbers))
	}
	page := max(in.Page, 1)

	id := record.ID()
	if ref.source.SessionID != "" {
		id = string(ref.source.SessionID)
	}
	messages, complete, err := session.CommittedMessages(ctx, log, id, ref.source.FromNumber, ref.source.ToNumber)
	if err != nil {
		return "", fmt.Errorf("segment %d could not be read: %w", ref.number, err)
	}
	if len(messages) == 0 {
		return fmt.Sprintf("The messages of segment %d were removed from the Record, so only what the conversation keeps of it is left: its recap and its tool call lines.", ref.number), nil
	}

	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	limit := opts.pageBytes()
	pages := paginate(renderOriginals(messages, limit/2), limit)
	if page > len(pages) {
		return "", fmt.Errorf("segment %d has %d pages; there is no page %d", ref.number, len(pages), page)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Segment %d", ref.number)
	if !ref.from.IsZero() {
		fmt.Fprintf(&b, ", %s", ref.from.In(loc).Format(time.RFC3339))
		if !ref.to.IsZero() {
			fmt.Fprintf(&b, " to %s", ref.to.In(loc).Format(time.RFC3339))
		}
	}
	fmt.Fprintf(&b, ". Its messages as they were, tool calls and tool results included; reasoning is left out. Page %d of %d.\n", page, len(pages))
	if !complete {
		b.WriteString("Some of this segment's messages were removed from the Record, so the first ones may be missing.\n")
	}
	b.WriteString(strings.Join(pages[page-1], "\n"))
	if page < len(pages) {
		fmt.Fprintf(&b, "\nMore: read_segment(segment=%d, page=%d).", ref.number, page+1)
	} else {
		fmt.Fprintf(&b, "\nEnd of segment %d.", ref.number)
	}
	return b.String(), nil
}

// paginate groups lines into pages of at most limit bytes, each line whole.
func paginate(lines []string, limit int) [][]string {
	var pages [][]string
	var current []string
	size := 0
	for _, line := range lines {
		if len(current) > 0 && size+len(line)+1 > limit {
			pages = append(pages, current)
			current, size = nil, 0
		}
		current = append(current, line)
		size += len(line) + 1
	}
	if len(current) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}
	return pages
}

// rolePrefix matches the start of a line that looks like the prefix of a
// Message: a bracketed role (or the kind of a call, a tool result, a notice or
// a tag word), after any backslashes an earlier escape put there.
var rolePrefix = regexp.MustCompile(`(?i)^(\\*)\[(user|assistant|system|developer|notice|call|tool|segment|recap|page)\b[^\]\n]*\]`)

// escapeLine makes a line of someone's text safe to put after a prefix: the
// start or end of one of the four tags, and a prefix that imitates a Message's
// own, which gets a backslash in front of it.
func escapeLine(line string) string {
	line = gotato.EscapeTags(line)
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
	var lines []string
	emit := func(label, text string) {
		parts := splitLines(text, maxLine)
		if len(parts) == 0 {
			lines = append(lines, label)
			return
		}
		lines = append(lines, label+" "+escapeLine(parts[0]))
		for _, part := range parts[1:] {
			lines = append(lines, escapeLine(part))
		}
	}
	for _, message := range messages {
		switch message.Role {
		case gotato.RoleToolResult:
			if message.ToolResult == nil {
				continue
			}
			result := message.ToolResult
			label := "[tool " + toolLabel(callTool[result.CallID], string(result.CallID))
			if result.Status != gotato.ToolResultOK {
				label += " " + oneLine(string(result.Status))
			}
			label += "]"
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
			emit(label, strings.Join(text, "\n"))
		default:
			for _, part := range message.Parts {
				text := textOfPart(part)
				if text == "" {
					continue
				}
				label := "[" + string(message.Role) + "]"
				if part.Metadata[gotato.MetaTag] == gotato.TagNotice {
					label = "[notice]"
				}
				emit(label, text)
			}
			for _, call := range message.ToolCalls {
				emit("[call "+toolLabel(call.ToolID, string(call.ID))+"]", cutArgsFull(call.Arguments))
			}
		}
	}
	return lines
}

// toolLabel is how a tool is named after a prefix: its ID with the text kept
// to one line, so the label cannot hold a line of its own.
func toolLabel(toolID, callID string) string {
	if toolID == "" {
		toolID = "call " + callID
	}
	return gotato.EscapeTags(strings.NewReplacer("]", ")", "\n", " ").Replace(oneLine(toolID)))
}

// textOfPart is the text of a Part, with a placeholder for an image or a file;
// reasoning has none.
func textOfPart(part gotato.ContentPart) string {
	switch {
	case part.Kind == gotato.ContentReasoning:
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

// splitLines splits text at its line breaks, and cuts a line longer than max
// bytes into pieces of at most that many, on a rune boundary.
func splitLines(text string, limit int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		for len(line) > limit {
			cut := limit
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			out = append(out, line[:cut])
			line = line[cut:]
		}
		out = append(out, line)
	}
	return out
}
