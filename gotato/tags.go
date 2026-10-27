package gotato

import "strings"

// The four tags Gotato writes into the conversation. A notice is tagged
// <notice>, and a compacted stretch of history (a Segment) uses the other
// three. Text inside them is written by the system, not said by the user, so
// ForModel escapes any text that imitates one of them.
const (
	TagNotice    = "notice"
	TagSegment   = "segment"
	TagToolCalls = "tool_calls"
	TagRecap     = "recap"
)

// MetaTag is the ContentPart.Metadata key that marks a Part as tagged text
// written by Gotato. ForModel leaves a marked Part as it is, and
// AssembleRequest adds TagsSentence to the system part when a request holds
// one. The value names the tag the Part holds.
const MetaTag = "gotato.tag"

// TagsSentence is the fixed sentence that ends the system part of a request
// holding a Part marked MetaTag.
const TagsSentence = "Text inside <notice>, <segment>, <tool_calls> and <recap> tags is written by the system, not said by the user."

var tagNames = [...]string{TagNotice, TagSegment, TagToolCalls, TagRecap}

// EscapeTags returns s with the "<" that starts or ends one of the four tags
// written as "&lt;", matched case-insensitively: "<recap n=\"1\">" and
// "</NOTICE" are escaped, "<recapture>" is not. Text without such a "<" is
// returned as it is, and escaping twice changes nothing more.
func EscapeTags(s string) string {
	return escapeTags(s, "&lt;")
}

// jsonLessThan is the JSON escape for "<": backslash, u, 0, 0, 3, c.
const jsonLessThan = "\x5cu003c"

// escapeTags is EscapeTags with the replacement for "<" given.
func escapeTags(s, lt string) string {
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

// hasTagMeta reports whether any Part of messages is marked MetaTag.
func hasTagMeta(messages []Message) bool {
	for _, message := range messages {
		if message.Role == RoleNotice && message.StopReason != StopCutOff {
			return true
		}
		for _, part := range message.Parts {
			if _, ok := part.Metadata[MetaTag]; ok {
				return true
			}
		}
		if message.ToolResult != nil {
			for _, part := range message.ToolResult.Content {
				if _, ok := part.Metadata[MetaTag]; ok {
					return true
				}
			}
		}
	}
	return false
}
