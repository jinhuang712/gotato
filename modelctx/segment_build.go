package modelctx

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
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

// SegmentSettings are the settings of a Segment built by code. A zero value
// takes the default.
type SegmentSettings struct {
	// Location is the time zone of the times a Segment shows. Nil means UTC.
	Location *time.Location
	// MessageTokens is the most a user Message or a final reply keeps, in
	// estimated tokens (bytes ÷ 4); the rest is cut, and a line says so.
	MessageTokens int
	// ToolLines is how many tool call lines a Segment shows; the calls past
	// it are counted by tool name.
	ToolLines int
	// ArgChars is how many characters of a call's arguments a line quotes.
	ArgChars int
	// TailTokens is the most the kept tail holds by default, before the
	// quarter of the window (or of the ceiling) caps it.
	TailTokens int
}

func (s SegmentSettings) location() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

func orDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// buildSegment renders the Messages a compaction closes as Segment number.
// recap is the text that ends it, or empty. position says when a Message was
// committed. The Record freezes the rendered Messages when it closes the
// Segment, so every instance sends the same bytes.
func buildSegment(number int, closed []gotato.Message, recap string, settings SegmentSettings, loaded []string, position func(gotato.MessageID) (gotato.MessagePosition, bool)) gotato.Segment {
	loc := settings.location()
	segment := gotato.Segment{Number: number}
	var first, last gotato.MessagePosition
	for _, message := range closed {
		p, ok := position(message.ID)
		if !ok {
			continue
		}
		if !p.Time.IsZero() {
			if segment.From.IsZero() || p.Time.Before(segment.From) {
				segment.From = p.Time.UTC()
			}
			if p.Time.After(segment.To) {
				segment.To = p.Time.UTC()
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
	segment.Source = gotato.SegmentSource{FromNumber: first.Number, ToNumber: last.Number}

	items := collect(closed, settings, number, position)
	limitLines(items, orDefault(settings.ToolLines, defaultToolLines))
	for _, it := range items {
		segment.ToolCalls = append(segment.ToolCalls, it.lines...)
	}

	var out messageList
	out.add(gotato.RoleUser, taggedPart(gotato.TagSegment, headerAttrs(number, segment, loc), headerText(number, recap != "")))
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
	if recap != "" {
		out.add(gotato.RoleUser, taggedPart(gotato.TagRecap, numberAttr(number), text.EscapeTags(recap)))
	}
	for i := range out.messages {
		out.messages[i].ID = gotato.MessageID(fmt.Sprintf("segment-%d-%d", number, i+1))
	}
	segment.Messages = out.messages
	segment.Recap = recap
	segment.LoadedTools = toolsCalled(closed, loaded)
	return segment
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
	return fmt.Sprintf("Segment %d. %s; reasoning,\nintermediate steps and tool results were left out. read_segment(%d) returns\nthe segment in full.", number, upTo, number)
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
	var pending []gotato.ToolCallLine
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
				items = append(items, item{role: gotato.RoleUser, parts: parts})
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
				if n := len(pending); n > 0 && sameCall(pending[n-1], line) {
					pending[n-1].Count = max(pending[n-1].Count, 1) + 1
					continue
				}
				pending = append(pending, line)
			}
			if final[i] {
				flush()
				if parts := keptParts(message.Parts, limit, number); len(parts) > 0 {
					items = append(items, item{role: gotato.RoleAssistant, parts: parts})
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
	return taggedPart(gotato.TagNotice, [][2]string{{"kind", kind}}, text.EscapeTags(body.String()))
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
