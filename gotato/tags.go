package gotato

import "github.com/jinhuang712/gotato/internal/text"

// The four tags Gotato writes into the conversation. A notice is tagged
// <notice>, and a compacted stretch of history (a Segment) uses the other
// three. Text inside them is written by the system, not said by the user, so
// ForModel escapes any text that imitates one of them.
const (
	TagNotice    = text.TagNotice
	TagSegment   = text.TagSegment
	TagToolCalls = text.TagToolCalls
	TagRecap     = text.TagRecap
)

// MetaTag is the ContentPart.Metadata key that marks a Part as tagged text
// written by Gotato. ForModel leaves a marked Part as it is, and
// AssembleRequest adds TagsSentence to the system part when a request holds
// one. The value names the tag the Part holds.
const MetaTag = "gotato.tag"

// TagsSentence is the fixed sentence that ends the system part of a request
// holding a Part marked MetaTag.
const TagsSentence = "Text inside <notice>, <segment>, <tool_calls> and <recap> tags is written by the system, not said by the user."

// jsonLessThan is the JSON escape for "<": backslash, u, 0, 0, 3, c.
const jsonLessThan = "\x5cu003c"

// hasTagMeta reports whether any Part of messages is marked MetaTag.
func hasTagMeta(messages []Message) bool {
	for _, message := range messages {
		if message.Role == RoleNotice && message.StopReason != StopReasonCutOff {
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
