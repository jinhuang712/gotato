package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

// replayArguments returns a past Tool Call's arguments in a form every wire
// API accepts back: the arguments themselves when they are a JSON object, and
// {} otherwise. A call whose arguments are not an object (most often one the
// output limit cut off) never ran; the failed Tool Result after it tells the
// model why. Sending the broken text back would make the provider refuse the
// whole request, and so end the Run the Loop meant to continue.
func replayArguments(arguments []byte) []byte {
	if isJSONObject(arguments) {
		return arguments
	}
	return []byte(`{}`)
}

// isJSONObject reports whether data is one JSON object.
func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// toolResultText renders a Tool Result Message as text for a provider. Every
// text-bearing Part kind counts, because a typed Tool returns its output as a
// ContentKindJSON Part; gotato.TextOf would drop it.
func toolResultText(message gotato.Message) string {
	var b strings.Builder
	for _, part := range message.Parts {
		switch part.Kind {
		case gotato.ContentKindText, gotato.ContentKindReasoning, gotato.ContentKindJSON:
			b.WriteString(part.Text)
		}
	}
	return b.String()
}
