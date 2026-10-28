package fakegateway

import (
	"net/http"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/internal/text"
)

// apiError is a refusal in a provider's own form.
type apiError struct {
	status int
	body   any
}

// wire is one wire API.
type wire interface {
	api() string
	authorize(h http.Header) *apiError
	// parse decodes the body into the neutral form, refusing what the
	// provider refuses by structure: a malformed tool loop, a missing field.
	parse(g *Gateway, body []byte, h http.Header) (*parsed, *apiError)
	// invalid wraps a message of the fake's own in the provider's error form.
	invalid(message string) *apiError
	tooManyOutputTokens(p *parsed, limit int) *apiError
	tooLong(tokens, window int) *apiError
	// validateReasoning refuses a request that does not replay the reasoning
	// the provider requires.
	validateReasoning(p *parsed) *apiError
	stream(w *sseWriter, p *parsed, pl *plan, q *quirks)
}

// parsed is a request in a form the three wire APIs share.
type parsed struct {
	model     string
	stream    bool
	maxOutput int
	tools     []toolDef
	events    []event
	// loaded holds the deferred tools the conversation loaded, by name.
	loaded map[string]bool
	// searchName is the name of the search tool, or "" when the request has
	// none; nativeSearch says it is the Responses tool_search tool, which
	// the model calls with a tool_search_call item.
	searchName   string
	nativeSearch bool
	midSystem    []int
	// store and include are the Responses API's fields of those names.
	store   *bool
	include []string
	header  http.Header
	raw     map[string]any
	// request is the typed body, for the wire's own validation.
	request any
	body    []byte
}

type toolDef struct {
	name   string
	schema map[string]any
	// deferred is a tool the model does not see until it is loaded
	// (defer_loading).
	deferred bool
}

// event is one step of the conversation as the model reads it: a text the
// user wrote, or a Tool Result.
type event struct {
	toolResult bool
	// search says the Tool Result answers the search tool.
	search bool
	text   string
}

// visibleTools are the tools the model can call: those that are not deferred,
// and the deferred ones a tool_reference or a tool_search_output loaded.
func (p *parsed) visibleTools() []toolDef {
	var out []toolDef
	for _, t := range p.tools {
		if !t.deferred || p.loaded[t.name] {
			out = append(out, t)
		}
	}
	return out
}

// wireName is a tool ID as the gateway names it for a provider: every
// character outside [A-Za-z0-9_-] becomes "_".
func wireName(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, id)
}

// systemTags are the tags Gotato writes into a conversation; a text that
// starts with one is the runtime speaking, not the user.
var systemTags = []string{"<" + text.TagNotice, "<" + text.TagSegment, "<" + text.TagRecap, "<" + text.TagToolCalls}

func isRuntimeText(text string) bool {
	text = strings.TrimSpace(text)
	for _, tag := range systemTags {
		if strings.HasPrefix(text, tag) {
			return true
		}
	}
	return false
}

// lastUser returns the index of the last user text the user wrote.
func (p *parsed) lastUser() int {
	for i, v := range slices.Backward(p.events) {
		if !v.toolResult && !isRuntimeText(v.text) && strings.TrimSpace(v.text) != "" {
			return i
		}
	}
	return -1
}
