package gotato

import (
	"strings"
)

// validatePrompt checks that a Prompt message is a user message with content.
func validatePrompt(message Message) error {
	if message.Role != RoleUser {
		return runtimeError(CodeInvalidArgument, "Prompt", "Prompt message must have role user", nil)
	}
	if !hasContent(message) {
		return runtimeError(CodeInvalidArgument, "Prompt", "Prompt message is empty", nil)
	}
	return nil
}

// validateControlMessage checks that a Steer, Interrupt or FollowUp message is
// a user message with content.
func validateControlMessage(message Message, operation string) error {
	if message.Role != RoleUser {
		return runtimeError(CodeInvalidArgument, operation, operation+" message must have role user", nil)
	}
	if !hasContent(message) {
		return runtimeError(CodeInvalidArgument, operation, operation+" message is empty", nil)
	}
	return nil
}

// hasContent reports whether at least one Part carries substance: non-blank
// text, or binary/JSON data. A Message made only of blank text Parts is empty.
func hasContent(message Message) bool {
	for _, part := range message.Parts {
		switch part.Kind {
		case ContentKindText, ContentKindReasoning:
			if strings.TrimSpace(part.Text) != "" {
				return true
			}
		default:
			if len(part.Data) > 0 || strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	}
	return false
}

// validateContinuable enforces that Continue never synthesizes user input: the
// transcript must already end where a Model can pick up.
func validateContinuable(messages []Message) error {
	if len(messages) == 0 {
		return runtimeError(CodeInvalidState, "Continue", "transcript is empty", nil)
	}
	// A reply cut off mid-stream is never sent again, so Continue asks the
	// Model again from the Message before it.
	last := len(messages) - 1
	for last >= 0 && messages[last].StopReason == StopReasonCutOff {
		last--
	}
	if last < 0 {
		return runtimeError(CodeInvalidState, "Continue", "transcript does not end in a Model-continuable state", nil)
	}
	switch messages[last].Role {
	case RoleUser, RoleToolResult, RoleNotice:
		return nil
	default:
		return runtimeError(CodeInvalidState, "Continue", "transcript does not end in a Model-continuable state", nil)
	}
}
