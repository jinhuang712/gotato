// Package fakemodel is a test-only server that speaks the three wire APIs a
// Gotato model bridge (package model) adapts to, and plays each provider's quirks on demand.
//
//	POST /v1/messages          Anthropic Messages, streamed as SSE
//	POST /v1/responses         OpenAI Responses, streamed as SSE
//	POST /v1/chat/completions  OpenAI Chat Completions, streamed as SSE
//
// It imports only Gotato's tag names (internal/text) and is built from the
// providers' public wire formats, so a test that points a Gotato model client at it checks the client
// against the format and not against its own decoder. Use it from a test with
// httptest.NewServer(fakemodel.New()), or serve it on a port with
// internal/fakemodel/cmd/fakemodel.
//
// # The model
//
// The model answers deterministically from the request. It echoes the last
// user text ("fake: <text>"). When that text is "use-tool" and the request
// has a tool, it calls the first tool, passing "from-tool" for the first
// string property of the tool's schema; after the Tool Result it answers
// "tool said: <result>".
//
// # Quirks
//
// A quirk is switched on by a directive in the last user text, such as
// [quirk:overflow]. Directives are removed from the text the model echoes. A
// directive the fake does not know is a 400, so a typo fails loudly.
//
//	overflow[=once]            refuse the request as too long, before any
//	                           output, in the provider's own words and status
//	overflow-midstream[=flags] stream some of the reply, then say the context
//	                           window ran out: Anthropic stops with
//	                           model_context_window_exceeded, OpenAI sends an
//	                           error event (flag "failed": Responses sends
//	                           response.failed instead)
//	cut-after-text             close the connection after some text, the first
//	                           time only, then answer normally
//	cut-in-args                the same, in the middle of a tool call's
//	                           arguments
//	cached=N                   report N cached input tokens, in the provider's
//	                           own usage field
//	reasoning                  stream reasoning with its signature or
//	                           encrypted content, and refuse a request that
//	                           does not replay it as the provider does
//	delay=D                    wait D (a Go duration) before the reply, and
//	                           stop when the client goes away
//
//	load=<tool ID>             play the native form of deferred tools: the
//	                           model calls the search tool for the tool, calls
//	                           the tool once it is loaded, and answers "tool
//	                           said: <result>" (see Deferred tools)
//
// Without a directive the fake also refuses as the provider does a request
// whose estimated size (one token per four bytes of the body) is past its
// window (WithWindow), a request whose max output tokens are past its limit
// (WithMaxOutput, a refusal that is not an overflow), a malformed tool loop,
// and, for Anthropic, a mid-conversation system message that breaks its
// placement rules.
//
// # Deferred tools
//
// The fake plays the native form of deferred tools on the two wire APIs that
// have one, from the documented shapes of each:
//
//   - Anthropic Messages: a tool with defer_loading true is in the tools array
//     but not in the model's context until a tool_result holds a tool_reference
//     block that names it. At least one tool must be non-deferred, and a
//     deferred tool takes no cache_control. A tool_reference to a tool that is
//     not deferred, or not in the list, is a 400.
//   - OpenAI Responses: a tool of type tool_search with execution "client"
//     stands for the client's search. The model's search is a tool_search_call
//     item with the arguments as an object, and the client answers with a
//     tool_search_output item with the same call_id and the functions it loads,
//     each one declared in tools with defer_loading true. A call without its
//     output, or an output that loads a function not declared deferred, is a 400.
//
// A directive [quirk:load=shop.invoice] in the last user text makes the model
// load that tool: it calls the search tool (a tool named search_tools, or
// ending in _search_tools, on Anthropic; the tool_search tool on Responses)
// with {"names":["shop.invoice"]}, and once the tool is loaded it calls it, with
// "from-tool" for the first string property of its schema, in alphabetical
// order. The tool's name on the wire is its ID with every character outside
// [A-Za-z0-9_-] replaced by "_". A tool already loaded is called without a
// search; a search that does not load the tool ends the reply with "fake: <name>
// is not loaded". The fake plays the documented shape. It is not a real
// provider, and it does not decide what a provider would do with a tool list it
// did not expect. Chat Completions has no native form, and "load" is not played
// there.
//
// # Recording
//
// Every request is recorded: method, path, headers, the body as sent and as
// decoded, the quirks it carried, and the positions of any system or
// developer messages in the middle of the conversation.
package fakemodel

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// The three wire APIs, named as Gotato's model bridge names them.
const (
	APIAnthropic = "anthropic-messages"
	APIResponses = "openai-responses"
	APIChat      = "openai-chat-completions"
)

const (
	defaultWindow    = 200_000
	defaultMaxOutput = 64_000
)

// Option configures a Server.
type Option func(*Server)

// WithWindow sets the model's context window in tokens, 200,000 by default.
// A request whose estimated size is past it is refused as too long.
func WithWindow(tokens int) Option { return func(g *Server) { g.window = tokens } }

// WithMaxOutput sets the most output tokens a request may ask for, 64,000 by
// default. A request past it is refused, and the refusal is not an overflow.
func WithMaxOutput(tokens int) Option { return func(g *Server) { g.maxOutput = tokens } }

// Request is one request the Server received.
type Request struct {
	// API is APIAnthropic, APIResponses or APIChat.
	API    string
	Method string
	Path   string
	Header http.Header
	// Body is the request body as it arrived.
	Body []byte
	// JSON is Body decoded, or nil when it was not a JSON object.
	JSON map[string]any
	// Quirks are the quirks the request carried, by name, with their value.
	Quirks map[string]string
	// UserText is the last user text with its directives removed.
	UserText string
	// MidSystem holds the positions, in the messages (or input items) of
	// the request, of each system or developer message that follows a
	// message of another role.
	MidSystem []int
	// Status is the HTTP status the Server answered.
	Status int
	// Outcome is "ok", "refused", "overflow-midstream", "cut" or "error".
	Outcome string
}

// Server is an http.Handler that plays the three wire APIs.
type Server struct {
	window, maxOutput int

	mu       sync.Mutex
	requests []*Request
	counts   map[string]int
}

// New returns a Server.
func New(opts ...Option) *Server {
	g := &Server{window: defaultWindow, maxOutput: defaultMaxOutput, counts: map[string]int{}}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Requests returns a copy of every request received, oldest first.
func (g *Server) Requests() []Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Request, len(g.requests))
	for i, r := range g.requests {
		out[i] = *r
	}
	return out
}

// Reset forgets the recorded requests and the quirks' counts.
func (g *Server) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = nil
	g.counts = map[string]int{}
}

// ServeHTTP implements http.Handler.
func (g *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var wire wire
	switch r.URL.Path {
	case "/v1/messages":
		wire = anthropicWire{}
	case "/v1/responses":
		wire = responsesWire{}
	case "/v1/chat/completions":
		wire = chatWire{}
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "fakemodel: no such endpoint " + r.URL.Path, "type": "invalid_request_error"}})
		return
	}
	g.handle(wire, w, r)
}

func (g *Server) handle(wr wire, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	x := g.begin(wr, w, r, body)
	defer x.settle()
	if e := x.accept(body); e != nil {
		x.refuse(e)
		return
	}
	tokens := x.tokens(body)
	if tokens > g.window {
		x.refuseTooLong(tokens)
		return
	}
	x.reply(tokens)
}

// sleep waits d, and reports false when the client went away first.
func sleep(r *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	}
}
