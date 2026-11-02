package text

import (
	"strings"
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

// MessageRefPrefix starts the ref of a Message in the Record, `msg:<message_id>`.
// A Segment writes one after each user Message and each final reply it keeps,
// and read_result resolves it from the Record. It names the Message by its ID,
// which every Instance and every fork of the Record shares, not by an Entry
// number, which means something only in the Record that wrote it.
const MessageRefPrefix = "msg:"

// MessageRef returns the ref of a Message.
func MessageRef(id string) string { return MessageRefPrefix + id }

// IDOfMessageRef returns the Message ID a ref names, and false for a string
// that is not a Message ref.
func IDOfMessageRef(ref string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(ref), MessageRefPrefix)
	return id, ok && id != ""
}

// RefSafe reports whether an ID can stand inside a ref written into a Segment:
// not empty, and with no space, control character, quote, bracket or angle
// bracket, so a ref ends where the line says it does.
func RefSafe(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("\\\"[]<>", r) {
			return false
		}
	}
	return true
}
