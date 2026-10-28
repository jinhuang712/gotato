package gateway

import (
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

// thinkingPrefixModels check the prefix each thinking block was written
// under: the system prompt, the tools and every Message before it. Only they
// take block_binding, and only with thinking.type "adaptive", which is their
// default. Sending "adaptive" to another Claude model would turn its thinking
// on (Opus 4.6, Sonnet 4.6) or be refused (Opus 4.5 and before).
var thinkingPrefixModels = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}

func checksThinkingPrefix(model string) bool {
	for _, name := range thinkingPrefixModels {
		if model == name || strings.HasPrefix(model, name+"-") || strings.HasPrefix(model, name+"@") {
			return true
		}
	}
	return false
}

// roundStart returns the index of the last user Message: where the current
// Round starts, or -1 when there is none. A Round is a user Message and every
// reply and Tool Result after it. A Steer is a user Message, so it starts a
// new Round; a Continue without a new prompt stays in the Round it resumes;
// Tool Results have a role of their own and never start one, and neither does
// a runtime notice (G-F29): it is not the user speaking.
//
// With roundReasoning, an assistant reply before this index is sent without
// its reasoning. Claude and OpenAI need reasoning back only inside the tool
// loop in progress. What is left out is always every reasoning artifact from
// the start of the Transcript up to the Round, and it is never sent again, so
// Claude's check of the prefix a thinking block was written under still holds
// for every block that is sent.
func roundStart(messages []gotato.Message) int {
	for i, message := range slices.Backward(messages) {
		if message.Role == gotato.RoleUser {
			return i
		}
	}
	return -1
}

// keepsReasoning reports whether the reply at index keeps its reasoning.
func (r providerRules) keepsReasoning(index, start int) bool {
	return !r.roundReasoning || index > start
}

// RoundReasoningOnly reports whether the provider is sent only the reasoning
// of the current Round, so a Checkpoint can leave the reasoning out of the
// Messages it keeps (modelctx.RoundReasoner). It is true for a recognized
// Claude or OpenAI provider.
func (c *Client) RoundReasoningOnly() bool { return c.rules.roundReasoning }
