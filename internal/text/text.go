// Package text holds the string helpers that the core and its sibling
// packages share: paging, spill refs and the escape of Gotato's own tags. It
// imports only the standard library, so the core may import it.
package text

import (
	"strings"
	"unicode/utf8"
)

// SpillRefPrefix starts the ref of a spilled result, `result:<call_id>`.
const SpillRefPrefix = "result:"

// SpillRef returns the ref of the spilled result of a Tool call.
func SpillRef(call string) string { return SpillRefPrefix + call }

// CallOfRef returns the Tool call a ref names, and false for a string that is
// not a ref.
func CallOfRef(ref string) (string, bool) {
	call, ok := strings.CutPrefix(strings.TrimSpace(ref), SpillRefPrefix)
	return call, ok && call != ""
}

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

// The four tags Gotato writes into the conversation; the core's Tag…
// constants are these.
const (
	TagNotice    = "notice"
	TagSegment   = "segment"
	TagToolCalls = "tool_calls"
	TagRecap     = "recap"
)

var tagNames = [...]string{TagNotice, TagSegment, TagToolCalls, TagRecap}

// EscapeTags returns s with the "<" that starts or ends one of the four tags
// written as "&lt;", matched case-insensitively: "<recap n=\"1\">" and
// "</NOTICE" are escaped, "<recapture>" is not. Text without such a "<" is
// returned as it is, and escaping twice changes nothing more.
func EscapeTags(s string) string {
	return EscapeTagsAs(s, "&lt;")
}

// EscapeTagsAs is EscapeTags with the replacement for "<" given.
func EscapeTagsAs(s, lt string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '<' || !startsTag(s[i+1:]) {
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(lt)
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// startsTag reports whether rest, the text after a "<", starts a tag name
// (after an optional "/") that ends there or at a character no name uses.
func startsTag(rest string) bool {
	rest = strings.TrimPrefix(rest, "/")
	for _, name := range tagNames {
		if len(rest) < len(name) || !strings.EqualFold(rest[:len(name)], name) {
			continue
		}
		if len(rest) == len(name) {
			return true
		}
		c := rest[len(name)]
		isName := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !isName {
			return true
		}
	}
	return false
}
