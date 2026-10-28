package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// toolResultOutput is the text a provider is sent for a Tool Result: its
// output, or the safe error text when it has none, or a placeholder, since no
// wire API accepts an empty one.
func toolResultOutput(message gotato.Message) string {
	text := toolResultText(message)
	if text == "" && message.ToolResult != nil {
		text = message.ToolResult.SafeError
	}
	if text == "" {
		text = "(no tool output)"
	}
	return text
}

// toolSchema returns a Tool's input schema for the wire: the spec's own, or
// fallback when it has none (each wire API names its own empty schema). A
// schema that is not valid JSON is refused.
func toolSchema(spec gotato.ToolSpec, fallback string) (json.RawMessage, error) {
	schema := spec.InputSchema
	if len(schema) == 0 {
		schema = []byte(fallback)
	}
	if !json.Valid(schema) {
		return nil, fmt.Errorf("gateway: Tool %q has invalid InputSchema", spec.ID)
	}
	return json.RawMessage(schema), nil
}

// refuseReasoningConfig refuses a request that sets ModelConfig.ReasoningEffort
// or ReasoningSummary on a wire API that does not send them. Only the
// Responses API does; the setting would otherwise be accepted and do nothing.
func refuseReasoningConfig(api API, config gotato.ModelConfig) error {
	if config.ReasoningEffort == "" && config.ReasoningSummary == "" {
		return nil
	}
	return gotato.ErrorOf(gotato.CodeNotSupported, fmt.Sprintf("gateway: ModelConfig.ReasoningEffort and ReasoningSummary are not sent by %s; use %s or leave them empty", api, APIResponses))
}
