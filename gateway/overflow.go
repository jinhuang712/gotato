package gateway

import (
	"net/http"
	"strings"
)

// overflowPhrases are the fragments, lower case, by which a provider's error
// body says the request was longer than the model's context window. They are
// the only place the gateway reads that out of prose: providers publish no
// stable code for it on every wire API. They are matched against an error's
// message, code and type. The list is short on purpose and has not been
// checked against live providers yet (see TODO.md); a provider that words it
// differently is reported as an ordinary failure, unless the team adds its
// wording in Config.OverflowPhrases.
var overflowPhrases = []string{
	"prompt is too long",         // Anthropic Messages
	"exceed context limit",       // Anthropic Messages, input plus max_tokens
	"maximum context length",     // OpenAI Chat Completions and compatible servers
	"context_length_exceeded",    // OpenAI error code
	"exceeds the context window", // OpenAI Responses and others
	"input is too long",          // gateways in front of other providers
}

// overflowMatcher decides whether error text names a context overflow. Its
// zero value knows only overflowPhrases; extra holds a team's own phrases and
// codes (Config.OverflowPhrases), lower case.
type overflowMatcher struct {
	extra []string
}

// newOverflowMatcher returns a matcher for the default phrases plus extra,
// which are trimmed and lower-cased; an empty one is dropped.
func newOverflowMatcher(extra []string) overflowMatcher {
	var m overflowMatcher
	for _, phrase := range extra {
		if phrase = strings.ToLower(strings.TrimSpace(phrase)); phrase != "" {
			m.extra = append(m.extra, phrase)
		}
	}
	return m
}

// isContextOverflow reports whether a refused request was refused for being
// too long: a 400 or 413 whose error message, code or type matches.
func (m overflowMatcher) isContextOverflow(status int, body errorBody) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return false
	}
	return m.mentions(body.Message, body.Code, body.Type)
}

// mentions reports whether any of the texts names a context overflow,
// whatever status it came with. A stream's own error event has no HTTP status.
func (m overflowMatcher) mentions(texts ...string) bool {
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, phrase := range overflowPhrases {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
		for _, phrase := range m.extra {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
	}
	return false
}

// httpError is the Error for a refused request.
func (m overflowMatcher) httpError(status int, retryable bool, body errorBody) *Error {
	return &Error{StatusCode: status, Retryable: retryable, Message: body.Message, Overflow: m.isContextOverflow(status, body)}
}
