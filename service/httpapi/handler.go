// Package httpapi exposes a service.Runner over HTTP. It is a protocol
// adapter: every route maps onto one Runner method, and Agent and Session
// semantics stay in the runtime. A Handler is an http.Handler: pass it to
// http.ListenAndServe, or mount it with mux.Handle.
//
// # Routes
//
// Bodies are JSON and unknown fields are refused (400); a body over 1 MiB is
// refused (413). Errors are {"code", "message", "not_run"?, "retryable"?}.
//
//	GET    /healthz                         liveness: 200 {ok, contract}
//	GET    /readyz                          readiness: 200, or 503 while draining {ok, draining, active_runs, active_sessions}
//	GET    /v1/agents                       200 {agents: [name], max_effect: {name: effect}}
//	POST   /v1/sessions                     {agent?, id?, metadata?}      → 201 session summary; 409 if id exists
//	GET    /v1/sessions                     → 200 [summary]
//	GET    /v1/sessions/{id}                → 200 session document
//	DELETE /v1/sessions/{id}                → 200 {id, deleted}
//	POST   /v1/sessions/{id}/fork           → 201 summary of the fork
//	GET    /v1/sessions/{id}/events?kind=   → 200 JSON Lines of the Session's stored Events
//	GET    /v1/sessions/{id}/context        → 200 modelctx report (what the model would see now)
//	GET    /v1/sessions/{id}/stream?after=N&record_epoch=E
//	                                        → SSE: stream, event*, reconnect?   (resumes after N; Last-Event-ID too)
//	POST   /v1/sessions/{id}/compact        {keep_messages?, min_tokens?} → 200 compaction result
//	POST   /v1/sessions/{id}/runs           {prompt | continue, timeout_ms?} → 200 run result
//	POST   /v1/sessions/{id}/runs/stream    {prompt | continue, timeout_ms?} → SSE: event*, result | error, reconnect?
//	POST   /v1/sessions/{id}/messages       {kind?, text}                  → 202 queued for the Run in flight, or 200 the Run it started
//	POST   /v1/sessions/{id}/notices        {kind, text}                   → 202 {queued, number, message_id}: a notice for the model's next call
//	POST   /v1/sessions/{id}/abort          → 202 {session_id, status}: stop the Run in flight
//
// # A Run lives as long as its request
//
// A client that drops POST /v1/sessions/{id}/messages (when it started a
// Run), POST .../runs or POST .../runs/stream ends the Run: it settles as
// canceled. A client cannot start a Run and walk away. A message (steer,
// follow_up or interrupt) with no Run going starts one, and its response is
// held until that Run settles (200 with the Run's result).
//
// GET /v1/sessions/{id}/stream is the other kind: it only watches. It may drop
// at any time and resume with Last-Event-ID (or after and record_epoch)
// without touching a Run. So a front end holds one POST open for the whole
// Run, and watches through a separate GET stream. A delta (text_delta and the
// like) is sent live and is not replayed on resume; the stored reply_ended
// Event holds the whole text, and a client replaces its partial text with it.
//
// An Event's JSON "number" is its position in the Session's Record, and the
// SSE "id" is the same number. A delta is not stored, so it has no id, and
// its "number" repeats that of the stored Event it follows: by design, it
// says where in the Record the delta falls.
//
// A message (steer, follow_up or interrupt), a notice and an abort reach the
// Run in flight on this Instance. For a Run another live Instance writes, they
// answer 409 not_writer, marked not run, with the writer's advertised address
// in the Gotato-Writer header and the body's "writer". WithForwarding sends
// such a request to that address instead, once.
//
// Authentication, rate limiting, and logging belong to the embedding
// application: wrap the Handler with your own middleware. To stop a process,
// call Runner.Drain first, then http.Server.Shutdown, then Runner.Close (see
// service.Config).
package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
)

// ContractVersion is the HTTP contract version. Field names and status codes
// documented here are stable within a version.
const ContractVersion = "1"

// Handler serves the API.
type Handler struct {
	runner    *service.Runner
	mux       *http.ServeMux
	keepAlive time.Duration
	// forward sends a message that reached an Instance that is not the Run's
	// writer to the writer's address. Nil leaves it at not_writer.
	forward *http.Client
}

// Option configures New.
type Option func(*Handler)

// WithForwarding turns on forwarding. A message or a cancel that reaches this
// Instance while another live Instance writes the Run is sent, as it came, to
// the address that Instance advertises, with the header Gotato-Forwarded, and
// that answer is returned. A request that carries the header is never forwarded
// again: it is answered not_writer. A nil client uses one with a 30 second
// timeout. The forwarded request carries the caller's headers, so your own
// authentication applies at the writer. Gotato connects to nothing else.
func WithForwarding(client *http.Client) Option {
	return func(h *Handler) {
		if client == nil {
			client = &http.Client{Timeout: 30 * time.Second}
		}
		h.forward = client
	}
}

// New creates a Handler over runner.
func New(runner *service.Runner, options ...Option) *Handler {
	h := &Handler{runner: runner, mux: http.NewServeMux(), keepAlive: 15 * time.Second}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	h.mux.HandleFunc("GET /healthz", h.healthy)
	h.mux.HandleFunc("GET /readyz", h.ready)
	h.mux.HandleFunc("GET /v1/agents", h.agents)
	h.mux.HandleFunc("POST /v1/sessions", h.createSession)
	h.mux.HandleFunc("GET /v1/sessions", h.listSessions)
	h.mux.HandleFunc("GET /v1/sessions/{id}", h.scoped(h.getSession))
	h.mux.HandleFunc("DELETE /v1/sessions/{id}", h.scoped(h.deleteSession))
	h.mux.HandleFunc("POST /v1/sessions/{id}/fork", h.scoped(h.fork))
	h.mux.HandleFunc("GET /v1/sessions/{id}/events", h.scoped(h.events))
	h.mux.HandleFunc("GET /v1/sessions/{id}/context", h.scoped(h.contextReport))
	h.mux.HandleFunc("GET /v1/sessions/{id}/stream", h.scoped(h.sessionStream))
	h.mux.HandleFunc("POST /v1/sessions/{id}/compact", h.scoped(h.compact))
	h.mux.HandleFunc("POST /v1/sessions/{id}/runs", h.scoped(h.run))
	h.mux.HandleFunc("POST /v1/sessions/{id}/runs/stream", h.scoped(h.runStream))
	h.mux.HandleFunc("POST /v1/sessions/{id}/messages", h.scoped(h.message))
	h.mux.HandleFunc("POST /v1/sessions/{id}/notices", h.scoped(h.notice))
	h.mux.HandleFunc("POST /v1/sessions/{id}/abort", h.scoped(h.abortSession))
	return h
}

// SessionIDHeader is the request header that names the Session a request is
// about. A load balancer hashes on it to send every request for a Session to
// one Instance, its writer. The handler reads it as a check: it must agree
// with the Session ID in the path or the body, or the request is refused with
// 400, since the load balancer would have routed it for another Session. A
// request that creates a Session answers with the new ID in a header of the
// same name, for the client to send next time.
const SessionIDHeader = "session-id"

// NotRunHeader marks a refusal whose request did not run, whatever its
// status (503 for a draining or overloaded Instance, 409 for a busy Session,
// 404 for a Session that does not exist), so a load balancer may send it again
// to another Instance without running it twice. The runtime decides it
// (gotato.NotRun). Its value is "true", and the body says the same in
// "not_run".
const NotRunHeader = "Gotato-Not-Run"

// WriterHeader carries, on a not_writer answer, the address the writer of the
// Run advertises. The body says the same in "writer". It is absent when the
// writer advertises none.
const WriterHeader = "Gotato-Writer"

// ForwardedHeader marks a request that another Instance forwarded here (see
// WithForwarding). Its value is "true". Such a request is never forwarded
// again.
const ForwardedHeader = "Gotato-Forwarded"

// scoped wraps a handler of a route with a {id}: the session-id header, when
// the request carries one, must name that Session.
func (h *Handler) scoped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := checkSessionHeader(r, r.PathValue("id")); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		next(w, r)
	}
}

// checkSessionHeader reports an error when the request's session-id header
// names another Session than id. An absent or blank header agrees with all.
func checkSessionHeader(r *http.Request, id string) error {
	header := strings.TrimSpace(r.Header.Get(SessionIDHeader))
	if header == "" || id == "" || header == id {
		return nil
	}
	return fmt.Errorf("the %s header names session %q, the request is about session %q", SessionIDHeader, header, id)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// healthy answers the liveness probe: the process is up. It stays 200 while
// the Runner drains, so an orchestrator does not restart an Instance that is
// finishing its Runs.
func (h *Handler) healthy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contract": ContractVersion})
}

// ready answers the readiness probe. While the Runner drains it answers 503, so
// the load balancer stops sending new work to this Instance.
func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK
	body := map[string]any{"ok": true, "active_runs": h.runner.ActiveRuns(), "active_sessions": h.runner.ActiveSessions()}
	if h.runner.Draining() {
		status = http.StatusServiceUnavailable
		body["ok"] = false
		body["draining"] = true
	}
	writeJSON(w, status, body)
}

func (h *Handler) agents(w http.ResponseWriter, _ *http.Request) {
	names := h.runner.Agents()
	caps := map[string]gotato.ToolEffect{}
	for _, name := range names {
		if spec, ok := h.runner.Agent(name); ok && spec.MaxEffect != gotato.ToolEffectUnspecified {
			caps[name] = spec.MaxEffect
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": names, "max_effect": caps})
}
