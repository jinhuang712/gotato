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
