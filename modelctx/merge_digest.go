package modelctx

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The sizes of a digest built by code (mergeDigest), in bytes.
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
// Recap. The whole is cut at 4,000 bytes by dropping the oldest lines.
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
