// Package fakegateway is a test-only server that speaks the three wire APIs a
// Gotato gateway adapts to, and plays each provider's quirks on demand.
//
//	POST /v1/messages          Anthropic Messages, streamed as SSE
//	POST /v1/responses         OpenAI Responses, streamed as SSE
//	POST /v1/chat/completions  OpenAI Chat Completions, streamed as SSE
//
// It imports only Gotato's tag names (internal/text) and is built from the
// providers' public wire formats, so a test that points a Gotato gateway at it checks the gateway
// against the format and not against its own decoder. Use it from a test with
// httptest.NewServer(fakegateway.New()), or serve it on a port with
// internal/fakegateway/cmd/fakegateway.
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
// [quirk:overflow], or by the X-Fake-Quirk request header, a comma-separated
// list of the same names and values (X-Fake-Quirk: delay=8s,cached=1200).
// Directives are removed from the text the model echoes. A directive the fake
// does not know is a 400, so a typo fails loudly.
//
//	overflow[=once]            refuse the request as too long, before any
//	                           output, in the provider's own words and status
//	overflow-midstream[=flags] stream some of the reply, then say the context
//	                           window ran out: Anthropic stops with
//	                           model_context_window_exceeded, OpenAI sends an
//	                           error event (flag "failed": Responses sends
//	                           response.failed instead); flag "once" plays it
//	                           the first time only
//	cut-after-text[=N]         close the connection after some text, N times
//	                           (default 1), then answer normally
//	cut-in-args[=N]            the same, in the middle of a tool call's
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
package fakegateway

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// The three wire APIs, named as Gotato's gateway names them.
const (
	APIAnthropic = "anthropic-messages"
	APIResponses = "openai-responses"
	APIChat      = "openai-chat-completions"
)

// QuirkHeader is the request header that switches quirks on, as an
// alternative to a directive in the last user text.
const QuirkHeader = "X-Fake-Quirk"

const (
	defaultWindow    = 200_000
	defaultMaxOutput = 64_000
)

// Option configures a Gateway.
type Option func(*Gateway)

// WithWindow sets the model's context window in tokens, 200,000 by default.
// A request whose estimated size is past it is refused as too long.
func WithWindow(tokens int) Option { return func(g *Gateway) { g.window = tokens } }

// WithMaxOutput sets the most output tokens a request may ask for, 64,000 by
// default. A request past it is refused, and the refusal is not an overflow.
func WithMaxOutput(tokens int) Option { return func(g *Gateway) { g.maxOutput = tokens } }

// Request is one request the Gateway received.
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
	// Status is the HTTP status the Gateway answered.
	Status int
	// Outcome is "ok", "refused", "overflow-midstream", "cut" or "error".
	Outcome string
}

// Gateway is an http.Handler that plays the three wire APIs.
type Gateway struct {
	window, maxOutput int

	mu       sync.Mutex
	requests []*Request
	counts   map[string]int
}

// New returns a Gateway.
func New(opts ...Option) *Gateway {
	g := &Gateway{window: defaultWindow, maxOutput: defaultMaxOutput, counts: map[string]int{}}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Requests returns a copy of every request received, oldest first.
func (g *Gateway) Requests() []Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Request, len(g.requests))
	for i, r := range g.requests {
		out[i] = *r
	}
	return out
}

// Reset forgets the recorded requests and the quirks' counts.
func (g *Gateway) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = nil
	g.counts = map[string]int{}
}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var wire wire
	switch r.URL.Path {
	case "/v1/messages":
		wire = anthropicWire{}
	case "/v1/responses":
		wire = responsesWire{}
	case "/v1/chat/completions":
		wire = chatWire{}
	case "/healthz":
		_, _ = io.WriteString(w, "ok")
		return
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "fakegateway: no such endpoint " + r.URL.Path, "type": "invalid_request_error"}})
		return
	}
	g.handle(wire, w, r)
}

func (g *Gateway) handle(wr wire, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := &Request{API: wr.api(), Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body, Quirks: map[string]string{}}
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) == nil {
		rec.JSON = decoded
	}
	g.mu.Lock()
	g.requests = append(g.requests, rec)
	g.mu.Unlock()
	finish := func(status int, outcome string) {
		g.mu.Lock()
		rec.Status, rec.Outcome = status, outcome
		g.mu.Unlock()
	}
	refuse := func(e *apiError) {
		finish(e.status, "refused")
		writeJSON(w, e.status, e.body)
	}
	defer func() {
		// A panic is a cut connection (http.ErrAbortHandler); the outcome
		// was set before it.
		g.mu.Lock()
		if rec.Outcome == "" {
			rec.Outcome = "error"
		}
		g.mu.Unlock()
	}()

	if r.Method != http.MethodPost {
		refuse(&apiError{status: http.StatusMethodNotAllowed, body: wr.invalid("fakegateway: POST only").body})
		return
	}
	if e := wr.authorize(r.Header); e != nil {
		refuse(e)
		return
	}
	p, perr := wr.parse(g, body, r.Header)
	if perr != nil {
		refuse(perr)
		return
	}
	g.mu.Lock()
	rec.MidSystem = p.midSystem
	g.mu.Unlock()
	q, text, err := extractQuirks(p, r.Header, wr.api())
	if err != nil {
		refuse(wr.invalid(err.Error()))
		return
	}
	g.mu.Lock()
	rec.Quirks, rec.UserText = q.byName, text
	g.mu.Unlock()
	if !p.stream {
		refuse(wr.invalid("fakegateway: only streamed requests are served; set stream to true"))
		return
	}
	if e := wr.validateReasoning(p); e != nil && q.has("reasoning") {
		refuse(e)
		return
	}
	if p.maxOutput > g.maxOutput {
		refuse(wr.tooManyOutputTokens(p, g.maxOutput))
		return
	}
	tokens := len(body)/4 + 1
	if value, ok := q.byName["overflow"]; ok && (!hasFlag(value, "once") || g.times(q, "overflow", 1)) {
		tokens = max(tokens, g.window+g.window/20+1)
	}
	if tokens > g.window {
		d, _ := time.ParseDuration(q.byName["delay"])
		if !sleep(r, d) {
			finish(0, "error")
			return
		}
		refuse(wr.tooLong(tokens, g.window))
		return
	}

	pl := g.answer(p, text, q, tokens)
	sw := newSSE(w)
	sw.start()
	if d, ok := q.byName["delay"]; ok {
		dur, _ := time.ParseDuration(d)
		if !sleep(r, dur) {
			finish(0, "error")
			return
		}
	}
	// The stream writes its own outcome through pl.
	pl.outcome = func(outcome string) { finish(http.StatusOK, outcome) }
	pl.g, pl.q = g, q
	finish(http.StatusOK, "ok")
	wr.stream(sw, p, pl, q)
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
