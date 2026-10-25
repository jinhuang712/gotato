package modelctx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	gotato "github.com/jinhuang712/gotato"
)

// The defaults of a Segment built by code (FEATURES.md → G-F06, Settings).
const (
	// DefaultMessageTokens is how long a user Message or a final reply may be
	// in a Segment before it is cut, in estimated tokens (bytes ÷ 4).
	DefaultMessageTokens = 2000
	// DefaultToolLines is how many tool call lines a Segment shows before it
	// counts the rest by tool name.
	DefaultToolLines = 100
	// DefaultArgChars is how many characters of a tool call's arguments a
	// line quotes.
	DefaultArgChars = 200
	// DefaultTailTokens is the most the kept tail holds when a compaction
	// names no Keep and no Floor.
	DefaultTailTokens = 20000
)

// metaNoticeSource is the Part metadata key that tells a service's notice from
// Gotato's own, until runtime notices (G-F29) are built: a notice Message
// whose Part carries this key with the value "service" stays in a Segment word
// for word, and any other notice is left out.
const metaNoticeSource = "gotato.notice_source"

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
func buildSegment(number int, closed []gotato.Message, recap string, settings SegmentSettings, active []string, position func(gotato.MessageID) (gotato.MessagePosition, bool)) gotato.Segment {
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
	limitLines(items, orDefault(settings.ToolLines, DefaultToolLines))
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
		out.add(gotato.RoleUser, taggedPart(gotato.TagRecap, numberAttr(number), gotato.EscapeTags(recap)))
	}
	for i := range out.messages {
		out.messages[i].ID = gotato.MessageID(fmt.Sprintf("segment-%d-%d", number, i+1))
	}
	segment.Messages = out.messages
	segment.Recap = recap
	segment.ToolSets = toolSetsCalled(closed, active)
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

// taggedPart is a Part of tagged text the system wrote, marked MetaTag.
// The tag, its attributes in the order given, and its text on lines of their
// own, as FEATURES.md shows a Segment.
func taggedPart(tag string, attrs [][2]string, text string) gotato.ContentPart {
	var b strings.Builder
	b.WriteString("<" + tag)
	for _, attr := range attrs {
		b.WriteString(" " + attr[0] + `="` + strings.ReplaceAll(attr[1], `"`, "&quot;") + `"`)
	}
	b.WriteString(">\n" + text + "\n</" + tag + ">")
	return gotato.ContentPart{
		Kind:     gotato.ContentText,
		Text:     b.String(),
		Metadata: map[string]string{gotato.MetaTag: tag},
	}
}

func numberAttr(number int) [][2]string { return [][2]string{{"n", fmt.Sprint(number)}} }

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

// item is one piece of a Segment, in order: a user Message, a final reply, or
// a block of tool call lines.
type item struct {
	isCalls  bool
	role     gotato.Role
	parts    []gotato.ContentPart
	lines    []gotato.ToolCallLine
	overflow string
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
	argChars := orDefault(settings.ArgChars, DefaultArgChars)
	limit := orDefault(settings.MessageTokens, DefaultMessageTokens)

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
			if skipUser(message) {
				continue
			}
			flush()
			if parts := keptParts(message.Parts, limit, number); len(parts) > 0 {
				items = append(items, item{role: gotato.RoleUser, parts: parts})
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
					if r.Status != gotato.ToolResultOK {
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

// statusUnanswered is the status of a call the closed Messages hold no result
// for.
const statusUnanswered gotato.ToolResultStatus = "unanswered"

func sameCall(a, b gotato.ToolCallLine) bool {
	return a.ToolID == b.ToolID && a.Args == b.Args && a.Status == b.Status && a.Code == b.Code
}

// skipUser reports whether a user Message is left out of a Segment: Gotato's
// own notices, and the continuation after a reply the output limit cut off.
func skipUser(message gotato.Message) bool {
	if gotato.TextOf(message) == gotato.TruncatedContinuationText {
		return true
	}
	for _, part := range message.Parts {
		if part.Metadata[gotato.MetaTag] == gotato.TagNotice {
			return part.Metadata[metaNoticeSource] != "service"
		}
	}
	return false
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
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != gotato.RoleAssistant || len(message.ToolCalls) > 0 || message.StopReason == gotato.StopCutOff || strings.TrimSpace(gotato.TextOf(message)) == "" {
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
		case part.Kind == gotato.ContentReasoning:
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

func isText(part gotato.ContentPart) bool {
	return part.Kind == gotato.ContentText || part.Kind == gotato.ContentJSON
}

// placeholder stands for an image or a file in a Segment: its kind, its name
// when it has one, and its size.
func placeholder(part gotato.ContentPart) gotato.ContentPart {
	kind := "file"
	if part.Kind == gotato.ContentImage && (part.MIMEType == "" || strings.HasPrefix(part.MIMEType, "image/")) {
		kind = "image"
	}
	name := part.Metadata["name"]
	if name == "" {
		name = part.Metadata["filename"]
	}
	text := fmt.Sprintf("[%s: %d bytes]", kind, len(part.Data))
	if name != "" {
		text = fmt.Sprintf("[%s: %s, %d bytes]", kind, oneLine(name), len(part.Data))
	}
	return gotato.ContentPart{Kind: gotato.ContentText, Text: text}
}

// cutRunes returns the first n bytes of s, less a rune cut in half.
func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	n = max(n, 0)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cutArgs returns a call's arguments as compact JSON on one line, cut at
// limit characters.
func cutArgs(arguments []byte, limit int) string {
	text := string(arguments)
	var buf bytes.Buffer
	if json.Compact(&buf, arguments) == nil {
		text = buf.String()
	}
	text = oneLine(text)
	if runes := []rune(text); len(runes) > limit {
		text = string(runes[:limit]) + "…"
	}
	return text
}

// renderLine writes a tool call as one line. The arguments are quoted as text
// inside <tool_calls>, so they pass through EscapeTags: an argument holding
// the end of the tag could end the block early.
func renderLine(line gotato.ToolCallLine, loc *time.Location) string {
	clock := "--:--"
	if !line.At.IsZero() {
		clock = line.At.In(loc).Format("15:04")
	}
	outcome := "ok"
	switch {
	case line.Status == statusUnanswered:
		outcome = string(statusUnanswered)
	case line.Code != "":
		outcome = "error " + gotato.EscapeTags(oneLine(line.Code))
	}
	text := clock + " " + gotato.EscapeTags(oneLine(line.ToolID))
	if line.Args != "" {
		text += " " + gotato.EscapeTags(line.Args)
	}
	text += " → " + outcome
	if line.Count > 1 {
		text += fmt.Sprintf(" ×%d", line.Count)
	}
	return text
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// limitLines keeps the first limit lines of the Segment's tool call blocks
// and counts the calls past them by tool name, on one line at the end of the
// last block that shows a call.
func limitLines(items []item, limit int) {
	shown := 0
	last := -1
	over := map[string]int{}
	for i := range items {
		if !items[i].isCalls {
			continue
		}
		keep := items[i].lines
		if shown+len(keep) > limit {
			keep = keep[:max(limit-shown, 0)]
			for _, line := range items[i].lines[len(keep):] {
				over[line.ToolID] += max(line.Count, 1)
			}
		}
		shown += len(keep)
		items[i].lines = keep
		if len(keep) > 0 {
			last = i
		}
	}
	if len(over) == 0 || last < 0 {
		return
	}
	names := make([]string, 0, len(over))
	for name := range over {
		names = append(names, name)
	}
	sort.Strings(names)
	counts := make([]string, 0, len(names))
	total := 0
	for _, name := range names {
		counts = append(counts, fmt.Sprintf("%s ×%d", gotato.EscapeTags(oneLine(name)), over[name]))
		total += over[name]
	}
	items[last].overflow = fmt.Sprintf("… %d more calls: %s", total, strings.Join(counts, ", "))
}

// toolSetsCalled lists the active ToolSets whose Tools the Messages call. A
// Tool of a ToolSet has the ID "<set>.<tool>".
func toolSetsCalled(messages []gotato.Message, active []string) []string {
	var out []string
	for _, name := range active {
		for _, message := range messages {
			if slices.ContainsFunc(message.ToolCalls, func(call gotato.ToolCall) bool { return strings.HasPrefix(call.ToolID, name+".") }) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
