package text

import (
	"strings"
	"unicode/utf8"
)

// SplitLines splits text at its line breaks, and cuts a line longer than limit
// bytes into pieces of at most that many, on a rune boundary.
func SplitLines(text string, limit int) []string {
	if text == "" {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
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

// Paginate groups lines into pages of at most limit bytes, each line whole.
// read_segment and read_result page with it, and the preview of a spilled
// result counts its pages with it.
func Paginate(lines []string, limit int) [][]string {
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
