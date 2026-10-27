// Package fakegateway is a test-only server that speaks the three wire APIs a
// Gotato gateway adapts to, and plays each provider's quirks on demand.
//
//	POST /v1/messages          Anthropic Messages, streamed as SSE
//	POST /v1/responses         OpenAI Responses, streamed as SSE
//	POST /v1/chat/completions  OpenAI Chat Completions, streamed as SSE
//
// It imports nothing from Gotato and is built from the providers' public wire
// formats, so a test that points a Gotato gateway at it checks the gateway
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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
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

var directive = regexp.MustCompile(`\[quirk:([A-Za-z0-9-]*)(?:=([^\]]*))?\]`)

var knownQuirks = map[string]bool{
	"overflow": true, "overflow-midstream": true, "cut-after-text": true, "cut-in-args": true,
	"cached": true, "reasoning": true, "delay": true, "load": true,
}

// quirks are the quirks of one request.
type quirks struct {
	byName map[string]string
	// key identifies the conversation for the quirks that play a limited
	// number of times.
	key string
}

func (q *quirks) has(name string) bool { _, ok := q.byName[name]; return ok }

func hasFlag(value, flag string) bool {
	return slices.Contains(strings.Split(value, "+"), flag)
}

// systemTags are the tags Gotato writes into a conversation; a text that
// starts with one is the runtime speaking, not the user.
var systemTags = []string{"<notice", "<segment", "<recap", "<tool_calls"}

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
	for i := len(p.events) - 1; i >= 0; i-- {
		if !p.events[i].toolResult && !isRuntimeText(p.events[i].text) && strings.TrimSpace(p.events[i].text) != "" {
			return i
		}
	}
	return -1
}

// extractQuirks reads the directives of the last user text and the header.
func extractQuirks(p *parsed, h http.Header, api string) (*quirks, string, error) {
	q := &quirks{byName: map[string]string{}}
	add := func(name, value string) error {
		if !knownQuirks[name] {
			return fmt.Errorf("fakegateway: unknown quirk %q", name)
		}
		switch name {
		case "delay":
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("fakegateway: quirk delay=%q is not a duration", value)
			}
		case "cached", "cut-after-text", "cut-in-args":
			if value != "" {
				if n, err := strconv.Atoi(value); err != nil || n < 0 {
					return fmt.Errorf("fakegateway: quirk %s=%q is not a count", name, value)
				}
			} else if name == "cached" {
				return fmt.Errorf("fakegateway: quirk cached needs a count, as cached=1200")
			}
		}
		q.byName[name] = value
		return nil
	}
	text := ""
	if i := p.lastUser(); i >= 0 {
		raw := p.events[i].text
		for _, m := range directive.FindAllStringSubmatch(raw, -1) {
			if err := add(m[1], m[2]); err != nil {
				return nil, "", err
			}
		}
		text = strings.TrimSpace(directive.ReplaceAllString(raw, ""))
		q.key = api + "|" + raw
	}
	for _, field := range strings.Split(h.Get(QuirkHeader), ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		name, value, _ := strings.Cut(field, "=")
		if err := add(strings.TrimSpace(name), strings.TrimSpace(value)); err != nil {
			return nil, "", err
		}
	}
	return q, text, nil
}

// times reports whether a quirk that plays a limited number of times plays
// now, and counts it.
func (g *Gateway) times(q *quirks, name string, limit int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := q.key + "|" + name
	if g.counts[key] >= limit {
		return false
	}
	g.counts[key]++
	return true
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

// plan is the reply the model gives.
type plan struct {
	text      string
	call      *toolCall
	reasoning string
	input     int
	cached    int
	output    int
	id        string
	outcome   func(string)
	g         *Gateway
	q         *quirks
}

type toolCall struct {
	name string
	args string
	// search says the call is the model's search, a tool_search_call item on
	// Responses.
	search bool
}

func (g *Gateway) answer(p *parsed, text string, q *quirks, tokens int) *plan {
	pl := &plan{input: tokens, id: shortHash(p.body)}
	if v, ok := q.byName["cached"]; ok {
		pl.cached, _ = strconv.Atoi(v)
		pl.cached = min(pl.cached, tokens)
	}
	last := p.lastUser()
	result, hasResult, searched := "", false, false
	for i := last + 1; i < len(p.events); i++ {
		if p.events[i].toolResult && p.events[i].search {
			searched = true
		} else if p.events[i].toolResult {
			result, hasResult = p.events[i].text, true
		}
	}
	visible := p.visibleTools()
	switch {
	case hasResult:
		pl.text = "tool said: " + result
	case q.has("load"):
		pl.load(p, visible, q.byName["load"], searched)
	case text == "use-tool" && len(visible) > 0:
		pl.call = &toolCall{name: visible[0].name, args: argsFor(visible[0])}
	default:
		pl.text = "fake: " + text
	}
	if q.has("reasoning") {
		pl.reasoning = "thinking about: " + text
	}
	pl.output = len(pl.text)/4 + len(pl.reasoning)/4 + 1
	if pl.call != nil {
		pl.output += len(pl.call.args)/4 + 1
	}
	return pl
}

// load plays the load directive: call the tool when it is visible, search for
// it when it is not, and say so when a search did not load it.
func (pl *plan) load(p *parsed, visible []toolDef, id string, searched bool) {
	name := wireName(id)
	for _, t := range visible {
		if t.name == name {
			pl.call = &toolCall{name: name, args: argsFor(t)}
			return
		}
	}
	switch {
	case searched:
		pl.text = "fake: " + name + " is not loaded"
	case p.searchName == "":
		pl.text = "fake: no search tool to load " + name
	default:
		args, _ := json.Marshal(map[string][]string{"names": {id}})
		pl.call = &toolCall{name: p.searchName, args: string(args), search: p.nativeSearch}
	}
}

func argsFor(t toolDef) string {
	if props, ok := t.schema["properties"].(map[string]any); ok {
		var names []string
		for name, v := range props {
			if m, ok := v.(map[string]any); ok && m["type"] == "string" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			first := names[0]
			for _, n := range names {
				if n < first {
					first = n
				}
			}
			b, _ := json.Marshal(map[string]string{first: "from-tool"})
			return string(b)
		}
	}
	return "{}"
}

// chunks splits s into pieces of about size bytes, at least two when s has
// two or more bytes, so a stream can be cut between them.
func chunks(s string, size int) []string {
	if s == "" {
		return nil
	}
	size = max(1, min(size, (len(s)+1)/2))
	var out []string
	for len(s) > size {
		cut := size
		for cut < len(s) && s[cut]&0xC0 == 0x80 { // keep a UTF-8 rune whole
			cut++
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(marshal(body))
}

// marshal encodes v as the providers do, with < > and & as they are.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// sseWriter writes server-sent events and flushes each one.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) start() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	s.w.WriteHeader(http.StatusOK)
	s.flush()
}

func (s *sseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// event writes one event. An empty name writes a data-only event.
func (s *sseWriter) event(name string, data any) {
	var buf bytes.Buffer
	if name != "" {
		buf.WriteString("event: " + name + "\n")
	}
	buf.WriteString("data: ")
	switch d := data.(type) {
	case string:
		buf.WriteString(d)
	default:
		buf.Write(marshal(d))
	}
	buf.WriteString("\n\n")
	_, _ = s.w.Write(buf.Bytes())
	s.flush()
}

// cut closes the connection without ending the stream.
func (s *sseWriter) cut(pl *plan) {
	pl.outcome("cut")
	s.flush()
	panic(http.ErrAbortHandler)
}

// cutNow reports whether the cut quirk plays now.
func (pl *plan) cutNow(name string) bool {
	v, ok := pl.q.byName[name]
	if !ok {
		return false
	}
	n := 1
	if v != "" {
		n, _ = strconv.Atoi(v)
	}
	return pl.g.times(pl.q, name, n)
}

// midOverflow reports whether the mid-stream overflow plays now.
func (pl *plan) midOverflow() (flags string, ok bool) {
	v, ok := pl.q.byName["overflow-midstream"]
	if !ok {
		return "", false
	}
	if hasFlag(v, "once") && !pl.g.times(pl.q, "overflow-midstream", 1) {
		return "", false
	}
	return v, true
}
