package fakemodel

import (
	"encoding/json"
	"net/http"
	"time"
)

// exchange is one request the Server handles, and its record.
type exchange struct {
	g   *Server
	wr  wire
	w   http.ResponseWriter
	r   *http.Request
	rec *Request
	// p, q and text are the parsed request, its quirks and its last user
	// text, once accept has read them.
	p    *parsed
	q    *quirks
	text string
}

// begin records the request, with its body decoded.
func (g *Server) begin(wr wire, w http.ResponseWriter, r *http.Request, body []byte) *exchange {
	rec := &Request{API: wr.api(), Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body, Quirks: map[string]string{}}
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) == nil {
		rec.JSON = decoded
	}
	g.mu.Lock()
	g.requests = append(g.requests, rec)
	g.mu.Unlock()
	return &exchange{g: g, wr: wr, w: w, r: r, rec: rec}
}

// finish records the status and the outcome.
func (x *exchange) finish(status int, outcome string) {
	x.g.mu.Lock()
	x.rec.Status, x.rec.Outcome = status, outcome
	x.g.mu.Unlock()
}

// refuse answers with the provider's error.
func (x *exchange) refuse(e *apiError) {
	x.finish(e.status, "refused")
	writeJSON(x.w, e.status, e.body)
}

// settle records the outcome "error" when none was set.
func (x *exchange) settle() {
	// A panic is a cut connection (http.ErrAbortHandler); the outcome
	// was set before it.
	x.g.mu.Lock()
	if x.rec.Outcome == "" {
		x.rec.Outcome = "error"
	}
	x.g.mu.Unlock()
}

// accept reads the request and its quirks, and returns the refusal of what
// the provider refuses before it counts the request's size.
func (x *exchange) accept(body []byte) *apiError {
	wr, r := x.wr, x.r
	if r.Method != http.MethodPost {
		return &apiError{status: http.StatusMethodNotAllowed, body: wr.invalid("fakemodel: POST only").body}
	}
	if e := wr.authorize(r.Header); e != nil {
		return e
	}
	p, perr := wr.parse(x.g, body, r.Header)
	if perr != nil {
		return perr
	}
	x.g.mu.Lock()
	x.rec.MidSystem = p.midSystem
	x.g.mu.Unlock()
	q, text, err := extractQuirks(p, wr.api())
	if err != nil {
		return wr.invalid(err.Error())
	}
	x.g.mu.Lock()
	x.rec.Quirks, x.rec.UserText = q.byName, text
	x.g.mu.Unlock()
	x.p, x.q, x.text = p, q, text
	return x.allowed()
}

// allowed returns the refusal of a request that does not stream, that drops
// the reasoning the reasoning quirk asks to be replayed, or that asks for
// too many output tokens.
func (x *exchange) allowed() *apiError {
	if !x.p.stream {
		return x.wr.invalid("fakemodel: only streamed requests are served; set stream to true")
	}
	if e := x.wr.validateReasoning(x.p); e != nil && x.q.has("reasoning") {
		return e
	}
	if x.p.maxOutput > x.g.maxOutput {
		return x.wr.tooManyOutputTokens(x.p, x.g.maxOutput)
	}
	return nil
}

// tokens is the request's estimated size, past the window when the
// overflow quirk plays.
func (x *exchange) tokens(body []byte) int {
	g := x.g
	tokens := len(body)/bytesPerToken + 1
	if value, ok := x.q.byName["overflow"]; ok && (!hasFlag(value, "once") || g.times(x.q, "overflow", 1)) {
		tokens = max(tokens, g.window+g.window/20+1)
	}
	return tokens
}

// refuseTooLong refuses the request as too long, after the delay quirk.
func (x *exchange) refuseTooLong(tokens int) {
	d, _ := time.ParseDuration(x.q.byName["delay"])
	if !sleep(x.r, d) {
		x.finish(0, "error")
		return
	}
	x.refuse(x.wr.tooLong(tokens, x.g.window))
}

// reply streams the model's answer, after the delay quirk.
func (x *exchange) reply(tokens int) {
	pl := x.g.answer(x.p, x.text, x.q, tokens)
	sw := newSSE(x.w)
	sw.start()
	if d, ok := x.q.byName["delay"]; ok {
		dur, _ := time.ParseDuration(d)
		if !sleep(x.r, dur) {
			x.finish(0, "error")
			return
		}
	}
	// The stream writes its own outcome through pl.
	pl.outcome = func(outcome string) { x.finish(http.StatusOK, outcome) }
	pl.g, pl.q = x.g, x.q
	x.finish(http.StatusOK, "ok")
	x.wr.stream(sw, x.p, pl, x.q)
}
