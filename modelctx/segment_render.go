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

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
)

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
		outcome = "error " + text.EscapeTags(oneLine(line.Code))
	}
	out := clock + " " + text.EscapeTags(oneLine(line.ToolID))
	if line.Args != "" {
		out += " " + text.EscapeTags(line.Args)
	}
	out += " → " + outcome
	if line.Count > 1 {
		out += fmt.Sprintf(" ×%d", line.Count)
	}
	return out
}

// toolsCalled lists the loaded Tools (qualified IDs) the Messages call, in the
// order they were loaded.
func toolsCalled(messages []gotato.Message, loaded []string) []string {
	var out []string
	for _, id := range loaded {
		for _, message := range messages {
			if slices.ContainsFunc(message.ToolCalls, func(call gotato.ToolCall) bool { return call.ToolID == id }) {
				out = append(out, id)
				break
			}
		}
	}
	return out
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
		counts = append(counts, fmt.Sprintf("%s ×%d", text.EscapeTags(oneLine(name)), over[name]))
		total += over[name]
	}
	items[last].overflow = fmt.Sprintf("… %d more calls: %s", total, strings.Join(counts, ", "))
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

// placeholder stands for an image or a file in a Segment: its kind, its name
// when it has one, and its size.
func placeholder(part gotato.ContentPart) gotato.ContentPart {
	kind := "file"
	if part.Kind == gotato.ContentKindImage && (part.MIMEType == "" || strings.HasPrefix(part.MIMEType, "image/")) {
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
	return gotato.ContentPart{Kind: gotato.ContentKindText, Text: text}
}

func isText(part gotato.ContentPart) bool {
	return part.Kind == gotato.ContentKindText || part.Kind == gotato.ContentKindJSON
}

// taggedPart is a Part of tagged text the system wrote, marked MetaTag.
// The tag, its attributes in the order given, and its text on lines of their
// own, as FEATURES.md shows a Segment.
func taggedPart(tag string, attrs [][2]string, text string) gotato.ContentPart {
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(tag)
	for _, attr := range attrs {
		fmt.Fprintf(&b, " %s=\"%s\"", attr[0], strings.ReplaceAll(attr[1], `"`, "&quot;"))
	}
	fmt.Fprintf(&b, ">\n%s\n</%s>", text, tag)
	return gotato.ContentPart{
		Kind:     gotato.ContentKindText,
		Text:     b.String(),
		Metadata: map[string]string{gotato.MetaTag: tag},
	}
}

func numberAttr(number int) [][2]string { return [][2]string{{"n", fmt.Sprint(number)}} }
