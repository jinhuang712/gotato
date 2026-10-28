package text

import (
	"strings"
	"unicode/utf8"
)

// Cut returns the largest n <= limit such that s[:n] ends on a rune boundary.
// It is 0 when the first rune of s is longer than limit bytes. A limit of s's
// length or more returns the length.
func Cut(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	if limit <= 0 {
		return 0
	}
	n := limit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// SplitLines splits text at its line breaks, and cuts a line longer than limit
// bytes into pieces of at most that many, on a rune boundary.
func SplitLines(text string, limit int) []string {
	if text == "" {
		return nil
	}
	limit = max(limit, 1)
	var out []string
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		whole := line == "" // an empty line is kept as one
		for len(line) > limit {
			cut := Cut(line, limit)
			if cut == 0 {
				// The limit is smaller than the first rune: take that rune
				// whole, so each piece makes progress.
				_, cut = utf8.DecodeRuneInString(line)
			}
			out = append(out, line[:cut])
			line = line[cut:]
		}
		if line != "" || whole {
			out = append(out, line)
		}
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
