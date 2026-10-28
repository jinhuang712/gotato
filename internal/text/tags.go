package text

import (
	"strings"
)

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
