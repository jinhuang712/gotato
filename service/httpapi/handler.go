// Package httpapi exposes a service.Runner over HTTP. It is a protocol
// adapter: every route maps onto one Runner method, and Agent and Session
// semantics stay in the runtime.
//
//	GET    /healthz                         liveness
//	GET    /readyz                          readiness (503 while draining)
//	GET    /v1/agents                       registered Agent names, and each one's max_effect if set
//	POST   /v1/sessions                     {agent?, metadata?}           → session summary
//	GET    /v1/sessions                     → [summary]
//	GET    /v1/sessions/{id}                → session document
//	DELETE /v1/sessions/{id}
//	POST   /v1/sessions/{id}/fork           → summary of the fork
//	GET    /v1/sessions/{id}/events?kind=   → JSON Lines of runtime Events
//	GET    /v1/sessions/{id}/context        → modelctx report (what the model would see now)
//	GET    /v1/sessions/{id}/stream?after=N&record_epoch=E
//	                                        → SSE: stream, event*, reconnect?   (resumes after N; Last-Event-ID too)
//	POST   /v1/sessions/{id}/compact        {keep?}                        → compaction result
//	POST   /v1/sessions/{id}/runs           {prompt | continue}            → run result
//	POST   /v1/sessions/{id}/runs/stream    {prompt | continue}            → SSE: event*, result, reconnect?
//	POST   /v1/sessions/{id}/messages       {kind?, text}                  → 202 queued for the run in flight, or 200 the run it started
//	POST   /v1/sessions/{id}/notices        {kind, text}                   → 202 {queued, number, message_id}: a notice for the model's next call
//	POST   /v1/sessions/{id}/cancel         stop the run in flight (202)
//	POST   /v1/runs                         {prompt, agent?, metadata?}    → create session + run
//	POST   /v1/runs/stream                  same, streaming
//	POST   /v1/runs/{run_id}/cancel
//
// The metadata of a run request is applied only when the request creates the
// Session; for an existing Session it is ignored. No route changes the
// metadata of a Session that exists.
//
// A message (steer, follow_up or interrupt) with no Run going starts one, and
// its response is held until that Run settles (200 with the Run's result); a
// client that closes the connection first ends the Run. A caller that cannot
// wait reads the reply from GET /v1/sessions/{id}/stream instead.
//
// A message (steer, follow_up or interrupt), a notice and a cancel reach the
// Run in flight on this instance. For a Run another live instance writes, they answer
// 409 not_writer, marked not run, with the writer's advertised address in the
// Gotato-Writer header and the body's "writer". WithForwarding sends such a
// request to that address instead, once.
//
// Authentication, rate limiting, and logging belong to the embedding
// application: wrap the Handler with your own middleware.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// ContractVersion is the HTTP contract version. Field names and status codes
// documented here are stable within a version.
const ContractVersion = "1"

// maxBodyBytes bounds every JSON request body. A larger body is rejected with
// 413 rather than being buffered.
const maxBodyBytes = 1 << 20

// errBodyTooLarge marks a request body that exceeded maxBodyBytes.
var errBodyTooLarge = errors.New("request body too large")

// maxTimeoutMS is the largest timeout_ms whose millisecond-to-Duration
// conversion cannot overflow.
const maxTimeoutMS = int64(math.MaxInt64) / int64(time.Millisecond)

// Handler serves the API.
type Handler struct {
	runner    *service.Runner
	mux       *http.ServeMux
	keepAlive time.Duration
	// forward sends a message that reached an instance that is not the Run's
	// writer to the writer's address. Nil leaves it at not_writer.
	forward *http.Client
}

// Option configures New.
type Option func(*Handler)

// WithKeepAlive sets how long an idle Session stream waits before it sends a
// comment, so that a proxy does not close it. The default is 15 seconds.
func WithKeepAlive(d time.Duration) Option {
	return func(h *Handler) {
		if d > 0 {
			h.keepAlive = d
		}
	}
}

// WithForwarding turns on forwarding. A message or a cancel that reaches this
// instance while another live instance writes the Run is sent, as it came, to
// the address that instance advertises, with the header Gotato-Forwarded, and
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
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contract": ContractVersion})
	})
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
	h.mux.HandleFunc("POST /v1/sessions/{id}/cancel", h.scoped(h.cancelSession))
	h.mux.HandleFunc("POST /v1/runs", h.run)
	h.mux.HandleFunc("POST /v1/runs/stream", h.runStream)
	h.mux.HandleFunc("POST /v1/runs/{run_id}/cancel", h.cancelRun)
	return h
}

// SessionIDHeader is the request header that names the Session a request is
// about. A load balancer hashes on it to send every request for a Session to
// one instance, its writer. The handler reads it as a check: it must agree
// with the Session ID in the path or the body, or the request is refused with
// 400, since the load balancer would have routed it for another Session. A
// request that creates a Session answers with the new ID in a header of the
// same name, for the client to send next time.
const SessionIDHeader = "session-id"

// NotRunHeader marks a refusal whose request did not run, whatever its
// status (503 for a draining or overloaded instance, 409 for a busy Session,
// 404 for a Session that does not exist), so a load balancer may send it again
// to another instance without running it twice. The runtime decides it
// (gotato.NotRun). Its value is "true", and the body says the same in
// "not_run".
const NotRunHeader = "Gotato-Not-Run"

// WriterHeader carries, on a not_writer answer, the address the writer of the
// Run advertises. The body says the same in "writer". It is absent when the
// writer advertises none.
const WriterHeader = "Gotato-Writer"

// ForwardedHeader marks a request that another instance forwarded here (see
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

// ---- request / response shapes ---------------------------------------------

type createSessionRequest struct {
	Agent    string            `json:"agent,omitempty"`
	ID       string            `json:"id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type runRequest struct {
	Prompt   string            `json:"prompt,omitempty"`
	Continue bool              `json:"continue,omitempty"`
	Agent    string            `json:"agent,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// TimeoutMS bounds the run; it settles as deadline_exceeded.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

type compactRequest struct {
	// Keep is how many recent Messages stay as they are; without it the
	// latest Run's Turns do.
	Keep int `json:"keep,omitempty"`
	// MinTokens skips a stretch since the last Checkpoint estimated below it.
	MinTokens int `json:"min_tokens,omitempty"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	// NotRun says the request did not run, so it can be sent again, also to
	// another instance.
	NotRun bool `json:"not_run,omitempty"`
	// Retryable says the same request, sent again later, may succeed.
	Retryable bool `json:"retryable,omitempty"`
	// RecordEpoch is the Record's current epoch, on a refusal to resume a
	// stream, for the client to start over with.
	RecordEpoch string `json:"record_epoch,omitempty"`
	// Writer is, on not_writer, the address the Run's writer advertises.
	Writer string `json:"writer,omitempty"`
}

// messageRequest is the body of POST /v1/sessions/{id}/messages.
type messageRequest struct {
	// Kind is steer (the default), follow_up or interrupt.
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
}

// noticeRequest is the body of POST /v1/sessions/{id}/notices.
type noticeRequest struct {
	// Kind is your own short label; Gotato's own kinds are reserved.
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// ---- handlers ---------------------------------------------------------------

func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	if h.runner.Draining() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "draining": true, "active_runs": h.runner.ActiveRuns(), "active_sessions": h.runner.ActiveSessions()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active_runs": h.runner.ActiveRuns(), "active_sessions": h.runner.ActiveSessions()})
}

func (h *Handler) agents(w http.ResponseWriter, _ *http.Request) {
	names := h.runner.Agents()
	caps := map[string]gotato.ToolEffect{}
	for _, name := range names {
		if spec, ok := h.runner.Agent(name); ok && spec.MaxEffect != gotato.EffectUnspecified {
			caps[name] = spec.MaxEffect
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": names, "max_effect": caps})
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	var in createSessionRequest
	if err := decode(w, r, &in); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := checkSessionHeader(r, in.ID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if in.ID != "" {
		s, err := h.runner.CreateSessionExclusive(r.Context(), in.Agent, in.ID, in.Metadata)
		if errors.Is(err, service.ErrSessionExists) {
			writeError(w, http.StatusConflict, fmt.Errorf("session %s already exists", in.ID))
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set(SessionIDHeader, s.ID())
		writeJSON(w, http.StatusCreated, session.SummaryOf(s))
		return
	}
	s, err := h.runner.CreateSession(r.Context(), in.Agent, in.Metadata)
	if err != nil {
		writeFailure(w, err)
		return
	}
	w.Header().Set(SessionIDHeader, s.ID())
	writeJSON(w, http.StatusCreated, session.SummaryOf(s))
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := h.runner.Sessions(r.Context())
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	s, err := h.runner.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Snapshot())
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := h.runner.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "deleted": true})
}

func (h *Handler) fork(w http.ResponseWriter, r *http.Request) {
	child, err := h.runner.Fork(r.Context(), r.PathValue("id"), "")
	if err != nil {
		writeFailure(w, err)
		return
	}
	w.Header().Set(SessionIDHeader, child.ID())
	writeJSON(w, http.StatusCreated, session.SummaryOf(child))
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.runner.Events(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for _, event := range events {
		if kind != "" && string(event.Kind()) != kind {
			continue
		}
		if err := enc.Encode(event); err != nil {
			return
		}
	}
}

func (h *Handler) contextReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.runner.Inspect(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) compact(w http.ResponseWriter, r *http.Request) {
	var in compactRequest
	if err := decode(w, r, &in); err != nil {
		writeDecodeError(w, err)
		return
	}
	if in.Keep < 0 || in.MinTokens < 0 {
		writeFailure(w, gotato.ErrorOf(gotato.ErrInvalidArgument, "keep and min_tokens cannot be negative"))
		return
	}
	result, err := h.runner.Compact(r.Context(), r.PathValue("id"), modelctx.CompactOptions{Keep: in.Keep, MinTokens: in.MinTokens})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) runRequest(w http.ResponseWriter, r *http.Request) (service.RunRequest, error) {
	var in runRequest
	if err := decode(w, r, &in); err != nil {
		return service.RunRequest{}, err
	}
	if !in.Continue && strings.TrimSpace(in.Prompt) == "" {
		return service.RunRequest{}, errors.New("prompt is required (or continue: true)")
	}
	if in.TimeoutMS < 0 || in.TimeoutMS > maxTimeoutMS {
		return service.RunRequest{}, fmt.Errorf("timeout_ms must be between 0 and %d", maxTimeoutMS)
	}
	return service.RunRequest{
		SessionID: r.PathValue("id"),
		Agent:     in.Agent,
		Prompt:    in.Prompt,
		Continue:  in.Continue,
		Metadata:  in.Metadata,
		Timeout:   time.Duration(in.TimeoutMS) * time.Millisecond,
	}, nil
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	request, err := h.runRequest(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	result, err := h.runner.Run(r.Context(), request)
	if err != nil && result.SessionID == "" {
		writeFailure(w, err)
		return
	}
	if errors.Is(err, service.ErrNotPersisted) {
		// Another writer took the Session during the Run: its result was
		// not kept, and the caller can tell that from a store failure.
		writeJSON(w, notSavedStatus(err), notSaved(err))
		return
	}
	if request.SessionID == "" {
		w.Header().Set(SessionIDHeader, result.SessionID)
	}
	writeJSON(w, http.StatusOK, result)
}

// runStream streams Events as Server-Sent Events (event: <kind>, data: JSON)
// and ends with a "result" event carrying the RunResult. When the drain
// stopped the Run, a "reconnect" event follows, with the SSE retry field set
// to the same delay.
func (h *Handler) runStream(w http.ResponseWriter, r *http.Request) {
	request, err := h.runRequest(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	flusher, _ := w.(http.Flusher)
	started := false
	start := func() {
		if !started {
			started = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
		}
	}
	sink := func(event gotato.Event) error { return writeEvent(w, flusher, event) }
	// The stream answers 200 once the Run is admitted. A request refused at
	// admission (draining, at capacity, past the activation limit) never gets
	// there: it answers the status /v1/runs answers, before the stream starts,
	// and a refusal marked not run is marked here too.
	result, runErr := h.runner.StreamRunAdmitted(r.Context(), request, start, sink)
	if !started && runErr != nil && result.SessionID == "" {
		writeFailure(w, runErr)
		return
	}
	start()
	switch {
	case runErr != nil && result.SessionID == "":
		data, _ := json.Marshal(failureOf(runErr))
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	case errors.Is(runErr, service.ErrNotPersisted):
		data, _ := json.Marshal(notSaved(runErr))
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	default:
		data, _ := json.Marshal(result)
		fmt.Fprintf(w, "event: result\ndata: %s\n\n", data)
	}
	if result.Reconnect != nil {
		writeReconnect(w, result.Reconnect)
	}
	if flusher != nil {
		flusher.Flush()
	}
}

// writeEvent sends one Event as a server-sent event. A stored Event carries its
// number as the id, which a browser's EventSource sends back as Last-Event-ID
// when it reconnects. A delta has none: it is not a position, and what it
// carried ends up whole in a Message.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, event gotato.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var id string
	if number := event.Head().Number; number > 0 && event.Kind().Class() == gotato.ClassStored {
		id = "id: " + strconv.FormatUint(number, 10) + "\n"
	}
	if _, err := fmt.Fprintf(w, "%sevent: %s\ndata: %s\n\n", id, event.Kind(), data); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// writeReconnect sends the advice to reconnect: the SSE retry field sets the
// wait a browser's EventSource uses, and the data says the same, with why.
func writeReconnect(w http.ResponseWriter, hint *service.Reconnect) {
	data, _ := json.Marshal(hint)
	fmt.Fprintf(w, "event: reconnect\nretry: %d\ndata: %s\n\n", hint.RetryAfterMS, data)
}

// streamStart is the first frame of a Session's stream: the Record's epoch for
// the client to keep, and where the stream starts and what the Record held.
type streamStart struct {
	SessionID   string `json:"session_id"`
	RecordEpoch string `json:"record_epoch"`
	After       uint64 `json:"after"`
	Head        uint64 `json:"head"`
}

// fromOf reads where a client's stream starts. The after parameter wins over
// the Last-Event-ID header that a browser's EventSource sends by itself; the
// epoch is the record_epoch parameter, or the Record-Epoch header.
func fromOf(r *http.Request) (service.StreamFrom, error) {
	var from service.StreamFrom
	raw := strings.TrimSpace(r.URL.Query().Get("after"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw != "" {
		after, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return from, fmt.Errorf("the position %q is not an Event number", raw)
		}
		from.After = after
	}
	from.RecordEpoch = strings.TrimSpace(r.URL.Query().Get("record_epoch"))
	if from.RecordEpoch == "" {
		from.RecordEpoch = strings.TrimSpace(r.Header.Get("Record-Epoch"))
	}
	return from, nil
}

// sessionStream serves a Session's stream as server-sent events: every stored
// Event after a number, from the Record, and then the live ones across Runs.
// It answers with a status before it streams: 404 for a Session that does not
// exist, and 409 (cannot_resume, with the current record_epoch) when the
// client's epoch or number does not fit the Record. The first frame is
// "stream", with the epoch; each Event follows with its number as the id. A
// stream that ends for a reason a reconnect cures, such as a drain, ends with a
// "reconnect" frame; any other failure ends with an "error" frame.
func (h *Handler) sessionStream(w http.ResponseWriter, r *http.Request) {
	from, err := fromOf(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := r.PathValue("id")
	stream, err := h.runner.StreamSession(r.Context(), sessionID, from)
	if err != nil {
		writeFailure(w, err)
		return
	}
	defer func() { _ = stream.Close() }()
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Record-Epoch", stream.RecordEpoch)
	w.WriteHeader(http.StatusOK)
	// The first frame also sets the wait a browser uses after a lost
	// connection, spread at random for each client.
	start, _ := json.Marshal(streamStart{SessionID: sessionID, RecordEpoch: stream.RecordEpoch, After: from.After, Head: stream.Head})
	fmt.Fprintf(w, "retry: %d\nevent: stream\ndata: %s\n\n", service.NewReconnect("").RetryAfterMS, start)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		next, cancel := context.WithTimeout(r.Context(), h.keepAlive)
		event, err := stream.Next(next)
		cancel()
		switch {
		case err == nil:
			if writeEvent(w, flusher, event) != nil {
				return
			}
		case r.Context().Err() != nil:
			return
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Fprint(w, ": keepalive\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case errors.Is(err, io.EOF):
			if hint := stream.Reconnect(); hint != nil {
				writeReconnect(w, hint)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		default:
			data, _ := json.Marshal(failureOf(err))
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
	}
}

// message delivers a message to the Run in flight on this instance (202), or,
// when no Run is going, starts a new Run with it and answers when that Run
// settles (200, as the run routes do). A Run another live instance writes is
// answered 409 not_writer, or forwarded when forwarding is on.
func (h *Handler) message(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	var in messageRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	result, err := h.runner.Send(r.Context(), service.MessageRequest{
		SessionID: r.PathValue("id"),
		Kind:      service.MessageKind(in.Kind),
		Text:      in.Text,
	})
	switch {
	case err == nil && result.Queued:
		writeJSON(w, http.StatusAccepted, result)
	case err == nil:
		writeJSON(w, http.StatusOK, result)
	case result.Run == nil || result.Run.SessionID == "":
		if h.forwarded(w, r, err, body) {
			return
		}
		writeFailure(w, err)
	case errors.Is(err, service.ErrNotPersisted):
		writeJSON(w, notSavedStatus(err), notSaved(err))
	default:
		writeJSON(w, http.StatusOK, result)
	}
}

// notice queues a notice for the Session's next model call (202). It starts no
// Run. For a Run another instance writes it answers 409 not_writer, or is
// forwarded, as a message is.
func (h *Handler) notice(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	var in noticeRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeDecodeError(w, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	result, err := h.runner.Notice(r.Context(), service.NoticeRequest{SessionID: r.PathValue("id"), Kind: in.Kind, Text: in.Text})
	if err != nil {
		if h.forwarded(w, r, err, body) {
			return
		}
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// readBody reads a request body, at most maxBodyBytes of it.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	if r.ContentLength > maxBodyBytes {
		return nil, errBodyTooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return body, nil
}

// forwarded sends the request to the writer of the Run when err says another
// instance writes it and forwarding is on, and relays the answer. It reports
// whether it answered. A request that was forwarded already, a writer that
// advertises no address, and an address that is not an http(s) URL are left to
// the caller, which answers not_writer.
func (h *Handler) forwarded(w http.ResponseWriter, r *http.Request, err error, body []byte) bool {
	address, ok := service.WriterOf(err)
	if !ok || h.forward == nil || address == "" || r.Header.Get(ForwardedHeader) != "" {
		return false
	}
	base, parseErr := url.Parse(address)
	if parseErr != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return false
	}
	target := strings.TrimRight(address, "/") + r.URL.RequestURI()
	request, reqErr := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if reqErr != nil {
		return false
	}
	request.Header = r.Header.Clone()
	request.Header.Del("Content-Length")
	request.Header.Set(ForwardedHeader, "true")
	response, doErr := h.forward.Do(request)
	if doErr != nil {
		// The request may have reached the writer, so it is not marked not run.
		writeError(w, http.StatusBadGateway, fmt.Errorf("forwarding to the writer: %w", doErr))
		return true
	}
	defer func() { _ = response.Body.Close() }()
	for key, values := range response.Header {
		w.Header()[key] = values
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
	return true
}

func (h *Handler) cancelSession(w http.ResponseWriter, r *http.Request) {
	if err := h.runner.CancelSession(r.Context(), r.PathValue("id")); err != nil {
		if h.forwarded(w, r, err, nil) {
			return
		}
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"session_id": r.PathValue("id"), "status": "cancel_requested"})
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	runID := gotato.RunID(r.PathValue("run_id"))
	if err := h.runner.CancelRun(r.Context(), runID); err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "cancel_requested"})
}

// ---- helpers ----------------------------------------------------------------

func decode(w http.ResponseWriter, r *http.Request, into any) error {
	if r.Body == nil {
		return nil
	}
	if r.ContentLength > maxBodyBytes {
		return errBodyTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		if errors.Is(err, io.EOF) {
			// An absent body, including a chunked request with no data, is not
			// malformed JSON.
			return nil
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errBodyTooLarge
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// writeDecodeError reports a request-parsing failure: 413 for an oversized
// body, 400 for everything else.
func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

// statusOfCode is the HTTP status of each ErrorCode. Every code is listed, so
// a new one cannot be left to a default by accident; the test over
// gotato.ErrorCodes fails when one is missing.
var statusOfCode = map[gotato.ErrorCode]int{
	gotato.ErrInvalidArgument:       http.StatusBadRequest,
	gotato.ErrNotSupported:          http.StatusBadRequest,
	gotato.ErrNotFound:              http.StatusNotFound,
	gotato.ErrSessionClosed:         http.StatusConflict,
	gotato.ErrSessionClosing:        http.StatusConflict,
	gotato.ErrInvalidState:          http.StatusConflict,
	gotato.ErrBusy:                  http.StatusConflict,
	gotato.ErrConflict:              http.StatusConflict,
	gotato.ErrCannotResume:          http.StatusConflict,
	gotato.ErrNotWriter:             http.StatusConflict,
	gotato.ErrStoreFailure:          http.StatusServiceUnavailable,
	gotato.ErrOverloaded:            http.StatusServiceUnavailable,
	gotato.ErrDraining:              http.StatusServiceUnavailable,
	gotato.ErrLimitExceeded:         http.StatusTooManyRequests,
	gotato.ErrContextOverflow:       http.StatusUnprocessableEntity,
	gotato.ErrDeadlineExceeded:      http.StatusGatewayTimeout,
	gotato.ErrCancelled:             http.StatusRequestTimeout,
	gotato.ErrModelFailure:          http.StatusInternalServerError,
	gotato.ErrModelProtocolFailure:  http.StatusInternalServerError,
	gotato.ErrToolResolutionFailure: http.StatusInternalServerError,
	gotato.ErrToolArgumentFailure:   http.StatusInternalServerError,
	gotato.ErrToolExecutionFailure:  http.StatusInternalServerError,
	gotato.ErrExtensionFailure:      http.StatusInternalServerError,
	gotato.ErrInternalInvariant:     http.StatusInternalServerError,
}

// StatusFor maps a runtime error to an HTTP status. It translates the error's
// code; it decides nothing about whether the request ran or may be retried,
// which the error says itself.
func StatusFor(err error) int {
	var runtimeErr *gotato.RuntimeError
	switch {
	case errors.As(err, &runtimeErr):
		if status, ok := statusOfCode[runtimeErr.Code]; ok {
			return status
		}
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	}
	return http.StatusInternalServerError
}

func failureOf(err error) errorResponse {
	status := StatusFor(err)
	out := errorResponse{Error: http.StatusText(status), NotRun: gotato.NotRun(err), Retryable: gotato.Retryable(err)}
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		out.Code = string(runtimeErr.Code)
		out.RecordEpoch = runtimeErr.Details["record_epoch"]
		out.Writer = runtimeErr.Details["writer"]
	}
	if status >= http.StatusInternalServerError && !out.NotRun {
		// Keep store and filesystem internals in the server log; the client
		// gets a stable body.
		log.Printf("httpapi: %d %s: %v", status, out.Error, err)
		out.Message = http.StatusText(status)
		return out
	}
	out.Message = err.Error()
	return out
}

// notSavedStatus is the status of a Run whose result was not kept: 409 when
// another writer took the Session during the Run, 500 otherwise.
func notSavedStatus(err error) int {
	if errors.Is(err, session.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// notSaved is the body of a Run whose result was not kept. It names the code
// and whether a retry may succeed, and keeps the store's own message in the
// log.
func notSaved(err error) errorResponse {
	out := internalError(err)
	out.Retryable = gotato.Retryable(err)
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		out.Code = string(runtimeErr.Code)
	}
	return out
}

func writeFailure(w http.ResponseWriter, err error) {
	if gotato.NotRun(err) {
		w.Header().Set(NotRunHeader, "true")
	}
	// A Session busy on another instance says when to retry.
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		if writer := runtimeErr.Details["writer"]; writer != "" && runtimeErr.Code == gotato.ErrNotWriter {
			w.Header().Set(WriterHeader, writer)
		}
		if ms, convErr := strconv.ParseInt(runtimeErr.Details["retry_after_ms"], 10, 64); convErr == nil {
			w.Header().Set("Retry-After", strconv.FormatInt((ms+999)/1000, 10))
		}
	}
	writeJSON(w, StatusFor(err), failureOf(err))
}

func writeError(w http.ResponseWriter, status int, err error) {
	resp := errorResponse{Error: http.StatusText(status)}
	if status >= http.StatusInternalServerError {
		resp = internalError(err)
	} else {
		resp.Message = err.Error()
	}
	writeJSON(w, status, resp)
}

// internalError logs the detailed cause and returns a stable body for a 5xx
// response, so store or filesystem internals never reach the client.
func internalError(err error) errorResponse {
	log.Printf("httpapi: internal error: %v", err)
	message := http.StatusText(http.StatusInternalServerError)
	return errorResponse{Error: message, Message: message}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
}
