package model

import (
	"slices"

	"github.com/jinhuang712/gotato/gotato"
)

// roundStart returns the index of the last user Message: where the current
// Round starts, or -1 when there is none. A Round is a user Message and every
// reply and Tool Result after it. A Steer is a user Message, so it starts a
// new Round; a Continue without a new prompt stays in the Round it resumes;
// Tool Results have a role of their own and never start one, and neither does
// a runtime notice (F4.4): it is not the user speaking.
//
// With ModelFacts.RoundReasoning, an assistant reply before this index is sent
// without its reasoning. A provider that takes only the current Round's
// reasoning needs it back only inside the tool loop in progress. What is left
// out is always every reasoning artifact from the start of the Transcript up
// to the Round, and it is never sent again, so a provider's check of the prefix
// a thinking block was written under still holds for every block that is sent.
func roundStart(messages []gotato.Message) int {
	for i, message := range slices.Backward(messages) {
		if message.Role == gotato.RoleUser {
			return i
		}
	}
	return -1
}

// keepsReasoning reports whether the reply at index keeps its reasoning: always,
// unless the model takes only the current Round's, and then from the Round's
// start on.
func keepsReasoning(roundReasoning bool, index, start int) bool {
	return !roundReasoning || index > start
}
