package gateway

import (
	"net/http"
	"strings"
)

// overflowPhrases are the fragments, lower case, by which a provider's error
// body says the request was longer than the model's context window. They are
// the only place the gateway reads that out of prose: providers publish no
// stable code for it on every wire API. The list is short on purpose and has
// not been checked against live providers yet (see TODO.md); a provider
// that words it differently is reported as an ordinary failure.
var overflowPhrases = []string{
	"prompt is too long",         // Anthropic Messages
	"exceed context limit",       // Anthropic Messages, input plus max_tokens
	"maximum context length",     // OpenAI Chat Completions and compatible servers
	"context_length_exceeded",    // OpenAI error code
	"exceeds the context window", // OpenAI Responses and others
	"input is too long",          // gateways in front of other providers
}

// isContextOverflow reports whether a refused request was refused for being
// too long: a 400 or 413 whose body matches overflowPhrases.
func isContextOverflow(status int, message string) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return false
	}
	return mentionsOverflow(message)
}

// mentionsOverflow reports whether text names a context overflow, whatever
// status it came with. A stream's own error event has no HTTP status.
func mentionsOverflow(text string) bool {
	lower := strings.ToLower(text)
	for _, phrase := range overflowPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// httpError is the Error for a refused request.
func httpError(status int, retryable bool, message string) *Error {
	return &Error{StatusCode: status, Retryable: retryable, Message: message, Overflow: isContextOverflow(status, message)}
}
