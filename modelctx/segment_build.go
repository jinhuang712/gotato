package modelctx

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/ref"
)

// The defaults of a Segment built by code (FEATURES.md → G-F06, the Segment settings table).
const (
	// defaultMessageTokens is how long a user Message or a final reply may be
	// in a Segment before it is cut, in estimated tokens (bytes ÷ 4).
	defaultMessageTokens = 2000
	// defaultToolLines is how many tool call lines a Segment shows before it
	// counts the rest by tool name.
	defaultToolLines = 100
	// defaultArgChars is how many characters of a tool call's arguments a
	// line quotes.
	defaultArgChars = 200
	// defaultTailTokens is the most the kept tail holds when a compaction
	// names no KeepMessages and no TailTokens.
	defaultTailTokens = 20000
)

// SegmentSettings are the settings of a Segment built by code (CodeSegments).
// A zero value takes the default.
type SegmentSettings struct {
	// MessageTokens is the most a user Message or a final reply keeps, in
	// estimated tokens (bytes ÷ 4); the rest is cut, and a line says so.
	MessageTokens int
	// ToolLines is how many tool call lines a Segment shows; the calls past
	// it are counted by tool name.
	ToolLines int
	// ArgChars is how many characters of a call's arguments a line quotes.
	ArgChars int
}

func orDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// CodeSegments is the default Builder (decision 033, with the refs of 043): the
// Segment shows the user's Messages and each Run's final reply word for word,
// cut at SegmentSettings.MessageTokens; one line per tool call, counted by tool
// past SegmentSettings.ToolLines; and the Notices a service wrote. It leaves out
// tool results, a Run's other text and reasoning, and names what it leaves out
// by a ref (package ref), so the original stays readable. The model writes only
// the Recap, which the frame appends.
type CodeSegments struct {
	SegmentSettings
}

// Build implements Builder. It renders the closed Messages as Segment
// info.Number. The Record freezes the rendered Messages when it closes the
// Segment, so every instance sends the same bytes.
func (c CodeSegments) Build(closed []gotato.Message, info SegmentInfo) (gotato.Segment, error) {
	loc := info.Location
	if loc == nil {
		loc = time.UTC
	}
	number := info.Number
	segment := gotato.Segment{Number: number, From: info.From, To: info.To}

	position := info.Position
	if position == nil {
		position = func(gotato.MessageID) (gotato.MessagePosition, bool) { return gotato.MessagePosition{}, false }
	}
	items := collect(closed, c.SegmentSettings, number, position)
	limitLines(items, orDefault(c.ToolLines, defaultToolLines))
	for _, it := range items {
		for _, line := range it.lines {
			segment.ToolCalls = append(segment.ToolCalls, line.ToolCallLine)
		}
	}

	var out messageList
	out.add(gotato.RoleUser, taggedPart(gotato.TagSegment, headerAttrs(number, segment, loc), headerText(number, info.Recap != "")))
	for _, it := range items {
		if !it.isCalls {
			out.add(it.role, it.parts...)
			continue
		}
		if len(it.lines) == 0 && it.overflow == "" {
			continue
		}
		text := make([]string, 0, len(it.lines)+1)
		for _, line := range it.lines {
			text = append(text, renderLine(line, loc))
		}
		if it.overflow != "" {
			text = append(text, it.overflow)
		}
		out.add(gotato.RoleAssistant, taggedPart(gotato.TagToolCalls, nil, strings.Join(text, "\n")))
	}
	segment.Messages = out.messages
	return segment, nil
}

// stamps reads from the positions of the closed Messages when the stretch
// began and ended, and the range of Entries it covers: what the frame stamps
// on a Segment, so every Segment is readable back by its number.
func stamps(closed []gotato.Message, position func(gotato.MessageID) (gotato.MessagePosition, bool)) (from, to time.Time, source gotato.SegmentSource) {
	var first, last gotato.MessagePosition
	for _, message := range closed {
		p, ok := position(message.ID)
		if !ok {
			continue
		}
		if !p.Time.IsZero() {
			if from.IsZero() || p.Time.Before(from) {
				from = p.Time.UTC()
			}
			if p.Time.After(to) {
				to = p.Time.UTC()
			}
		}
		if p.Number > 0 {
			if first.Number == 0 || p.Number < first.Number {
				first = p
			}
			if p.Number > last.Number {
				last = p
			}
		}
	}
	return from, to, gotato.SegmentSource{FromNumber: first.Number, ToNumber: last.Number}
}

// messageList builds Messages, joining a Part to the last Message when it has
// the same role, so user and assistant still take turns.
type messageList struct {
	messages []gotato.Message
}

func (l *messageList) add(role gotato.Role, parts ...gotato.ContentPart) {
	if n := len(l.messages); n > 0 && l.messages[n-1].Role == role {
		l.messages[n-1].Parts = append(l.messages[n-1].Parts, parts...)
		return
	}
	l.messages = append(l.messages, gotato.Message{Role: role, Parts: slices.Clone(parts)})
}

func headerAttrs(number int, segment gotato.Segment, loc *time.Location) [][2]string {
	attrs := numberAttr(number)
	if !segment.From.IsZero() {
		attrs = append(attrs, [2]string{"from", segment.From.In(loc).Format(time.RFC3339)})
	}
	if !segment.To.IsZero() {
		attrs = append(attrs, [2]string{"to", segment.To.In(loc).Format(time.RFC3339)})
	}
	return attrs
}

func headerText(number int, hasRecap bool) string {
	upTo := "The messages are word for word"
	if hasRecap {
		upTo = fmt.Sprintf("The messages up to recap %d are word for word", number)
	}
	return fmt.Sprintf("Segment %d. %s; reasoning,\nintermediate steps and tool results were left out. read_segment(%d) returns\nthe segment in full; read_result(ref) returns one tool result or message by its ref.", number, upTo, number)
}

// collect walks the closed Messages in order and returns what a Segment keeps
// of them.
func collect(closed []gotato.Message, settings SegmentSettings, number int, position func(gotato.MessageID) (gotato.MessagePosition, bool)) []item {
	results := map[gotato.ToolCallID]*gotato.ToolResult{}
	for i := range closed {
		if r := closed[i].ToolResult; r != nil {
			results[r.CallID] = r
		}
	}
	final := finalReplies(closed)
	argChars := orDefault(settings.ArgChars, defaultArgChars)
	limit := orDefault(settings.MessageTokens, defaultMessageTokens)

	var items []item
	var pending []callLine
	flush := func() {
		if len(pending) > 0 {
			items = append(items, item{isCalls: true, lines: pending})
			pending = nil
		}
	}
	for i, message := range closed {
		switch message.Role {
		case gotato.RoleUser:
			flush()
			if parts := keptParts(message.Parts, limit, number); len(parts) > 0 {
				items = append(items, item{role: gotato.RoleUser, parts: append(parts, refPartOf(number, message.ID)...)})
			}
		case gotato.RoleNotice:
			// Gotato's own notices mattered only at the time: the Recap covers
			// them. A service's stays word for word, because Gotato cannot tell
			// whether it still applies.
			if message.Notice == nil || message.Notice.Source != gotato.NoticeSourceService {
				continue
			}
			flush()
			if parts := keptParts(message.Parts, limit, number); len(parts) > 0 {
				items = append(items, item{role: gotato.RoleUser, parts: []gotato.ContentPart{noticePart(message.Notice.Kind, parts)}})
			}
		case gotato.RoleAssistant:
			var at time.Time
			if p, ok := position(message.ID); ok {
				at = p.Time.UTC()
			}
			for _, call := range message.ToolCalls {
				line := gotato.ToolCallLine{At: at, ToolID: call.ToolID, Args: cutArgs(call.Arguments, argChars), Status: statusUnanswered}
				if r := results[call.ID]; r != nil {
					line.Status = r.Status
					if r.Status != gotato.ToolResultStatusOK {
						line.Code = string(r.Status)
					}
				}
				var refs []string
				if ref.Safe(string(call.ID)) {
					refs = []string{ref.Result(string(call.ID)).String()}
				}
				if n := len(pending); n > 0 && sameCall(pending[n-1].ToolCallLine, line) {
					pending[n-1].Count = max(pending[n-1].Count, 1) + 1
					pending[n-1].refs = append(pending[n-1].refs, refs...)
					continue
				}
				pending = append(pending, callLine{ToolCallLine: line, refs: refs})
			}
			if final[i] {
				flush()
				if parts := keptParts(message.Parts, limit, number); len(parts) > 0 {
					items = append(items, item{role: gotato.RoleAssistant, parts: append(parts, refPartOf(number, message.ID)...)})
				}
			}
		}
	}
	flush()
	return items
}

func sameCall(a, b gotato.ToolCallLine) bool {
	return a.ToolID == b.ToolID && a.Args == b.Args && a.Status == b.Status && a.Code == b.Code
}

// noticePart is a service's notice as a Segment keeps it: its kind and its
// text, with the text escaped like any other.
func noticePart(kind string, parts []gotato.ContentPart) gotato.ContentPart {
	var body strings.Builder
	for _, part := range parts {
		if isText(part) {
			body.WriteString(part.Text)
		}
	}
	return taggedPart(gotato.TagNotice, [][2]string{{"kind", text.EscapeTags(kind)}}, text.EscapeTags(body.String()))
}

// finalReplies marks each Run's final reply: its last assistant Message
// without tool calls that has text. A Message with no Run ID is grouped with
// the Messages after the user Message before it.
func finalReplies(messages []gotato.Message) map[int]bool {
	groups := make([]string, len(messages))
	anon := 0
	for i, message := range messages {
		if message.Role == gotato.RoleUser && message.RunID == "" {
			anon++
		}
		if message.RunID != "" {
			groups[i] = "run:" + string(message.RunID)
		} else {
			groups[i] = fmt.Sprintf("anon:%d", anon)
		}
	}
	final := map[int]bool{}
	seen := map[string]bool{}
	for i, message := range slices.Backward(messages) {
		if message.Role != gotato.RoleAssistant || len(message.ToolCalls) > 0 || message.StopReason == gotato.StopReasonCutOff || strings.TrimSpace(gotato.TextOf(message)) == "" {
			continue
		}
		if !seen[groups[i]] {
			seen[groups[i]] = true
			final[i] = true
		}
	}
	return final
}

// keptParts keeps the text of a Message word for word, cut at limit tokens
// with a line that says so, and stands an image or a file in with a
// placeholder. Reasoning is left out.
func keptParts(parts []gotato.ContentPart, limit, number int) []gotato.ContentPart {
	budget := limit * 4
	total := 0
	for _, part := range parts {
		if isText(part) {
			total += len(part.Text)
		}
	}
	var out []gotato.ContentPart
	used := 0
	cut := false
	for _, part := range parts {
		switch {
		case part.Kind == gotato.ContentKindReasoning:
		case isText(part):
			if part.Text == "" {
				continue
			}
			if used+len(part.Text) > budget {
				out = append(out, gotato.ContentPart{Kind: part.Kind, Text: cutRunes(part.Text, budget-used)})
				cut = true
			} else {
				used += len(part.Text)
				out = append(out, gotato.ContentPart{Kind: part.Kind, Text: part.Text})
			}
		default:
			out = append(out, placeholder(part))
		}
		if cut {
			break
		}
	}
	if cut {
		out = append(out, taggedPart(gotato.TagSegment, numberAttr(number),
			fmt.Sprintf("This message was cut after %d of %d bytes (%d tokens). read_segment(%d) returns the rest.", budget, total, limit, number)))
	}
	return out
}
