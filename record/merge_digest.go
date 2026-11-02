package record

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The sizes of a digest built by code (mergeDigest): digestMaxBytes counts
// bytes, the others count characters (runes) of one piece of text.
const (
	digestMaxBytes     = 4000
	digestUserChars    = 160
	digestUsersPerSeg  = 3
	digestRecapChars   = 300
	digestMergedChars  = 1200
	digestToolsPerLine = 5
)

// mergeDigest is the Recap of a merge when no model writes it: a digest built
// by code, deterministic, under `## What happened`, so it passes the same rules
// as a Recap the model writes. For each Segment it has one line with its times,
// the start of its user Messages, its tool calls counted by tool, and its own
// Recap cut short; a merged Segment among them contributes the start of its
// Recap. The whole is held to 4,000 bytes by dropping the oldest lines, and the
// text, with the note of what was left out, is cut at a rune boundary to 120
// bytes more than that, which only a single line that is longer can reach.
func mergeDigest(segments []gotato.Segment, loc *time.Location) string {
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
		shown := clip(withoutHeading(segment.Recap), digestMergedChars)
		return line + " " + shown + digestRefs(segment, shown)
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
	return line + digestRefs(segment, "")
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
			if _, tagged := part.Metadata[gotato.MetaTag]; !tagged && part.Kind == gotato.ContentKindText && strings.TrimSpace(part.Text) != "" {
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

// The refs a digest line keeps: at most digestMsgRefs of the Messages a Segment
// kept and digestResultRefs of its tool results, in the order they come in the
// Segment. A Segment that has none (one written before refs, or whose calls
// were counted past the line cap) is named by its number, which read_segment
// reads.
const (
	digestMsgRefs    = 4
	digestResultRefs = 4
	// digestMergedRefs is how many refs a digest line takes from the Recap of
	// a merged Segment that the cut of the line left out.
	digestMergedRefs = 8
)

var (
	// resultRefsInLine matches the refs that end a tool call line, `[result:c1 result:c2]`.
	resultRefsInLine = regexp.MustCompile(`\[((?:result:[^\s\[\]]+ ?)+)\]$`)
	// msgRefLine matches the line a Segment writes after a kept Message.
	msgRefLine = regexp.MustCompile(`^ref (msg:\S+)$`)
	// anyRef matches a ref wherever a Recap states one.
	anyRef = regexp.MustCompile(`\b(?:result|msg):[^\s\[\]()"',;<>]+`)
)

// segmentRefs lists the refs a Segment's own Messages carry, read only from
// the parts the system wrote (a user's words carry no tag and are never
// scanned): those of the Messages it kept, and those of its tool results.
func segmentRefs(segment gotato.Segment) (msgs, results []string) {
	for _, message := range segment.Messages {
		for _, part := range message.Parts {
			switch part.Metadata[gotato.MetaTag] {
			case gotato.TagSegment:
				for line := range strings.SplitSeq(part.Text, "\n") {
					if m := msgRefLine.FindStringSubmatch(line); m != nil {
						msgs = append(msgs, m[1])
					}
				}
			case gotato.TagToolCalls:
				for line := range strings.SplitSeq(part.Text, "\n") {
					if m := resultRefsInLine.FindStringSubmatch(line); m != nil {
						results = append(results, strings.Fields(m[1])...)
					}
				}
			}
		}
	}
	return msgs, results
}

// digestRefs writes the refs of a Segment for a digest line, or "". shown is
// the Recap text of a merged Segment as the line shows it: a merged Segment
// holds its refs in its Recap, so only those that the cut left out are added.
func digestRefs(segment gotato.Segment, shown string) string {
	var refs []string
	if len(segment.Merged) > 0 {
		for _, ref := range anyRef.FindAllString(withoutHeading(segment.Recap), -1) {
			if !strings.Contains(shown, ref) && !slices.Contains(refs, ref) {
				refs = append(refs, ref)
			}
		}
		if len(refs) > digestMergedRefs {
			refs = append(refs[:digestMergedRefs], "…")
		}
	} else {
		msgs, results := segmentRefs(segment)
		refs = append(refs, msgs[:min(len(msgs), digestMsgRefs)]...)
		refs = append(refs, results[:min(len(results), digestResultRefs)]...)
		if len(refs) > 0 && (len(msgs) > digestMsgRefs || len(results) > digestResultRefs) {
			refs = append(refs, "…")
		}
	}
	if len(refs) == 0 {
		return ""
	}
	return " Refs: " + strings.Join(refs, " ") + "."
}

// clip cuts s at limit characters with an ellipsis.
func clip(s string, limit int) string {
	if runes := []rune(s); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return s
}

// withoutHeading returns a Recap on one line, without the `## ` headings.
func withoutHeading(recap string) string {
	var kept []string
	for line := range strings.SplitSeq(recap, "\n") {
		if strings.HasPrefix(line, "## ") {
			continue
		}
		kept = append(kept, strings.TrimPrefix(strings.TrimSpace(line), "- "))
	}
	return oneLine(strings.Join(kept, " "))
}
