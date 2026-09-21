// Package httpapi exposes a service.Runner over HTTP. It is a protocol
// adapter: every route maps onto one Runner method and defines no Agent or
// Session semantics of its own.
//
//	GET    /healthz                         liveness
//	GET    /readyz                          readiness (503 while draining)
//	GET    /v1/agents                       registered AgentSpec names
//	POST   /v1/sessions                     {agent?, metadata?}           → session summary
//	GET    /v1/sessions                     → [summary]
//	GET    /v1/sessions/{id}                → session document
//	DELETE /v1/sessions/{id}
//	POST   /v1/sessions/{id}/fork           → summary of the fork
//	GET    /v1/sessions/{id}/events?kind=   → JSON Lines of runtime Events
//	GET    /v1/sessions/{id}/context        → modelctx report (what the model would see now)
//	POST   /v1/sessions/{id}/compact        {keep?}                        → compaction result
//	POST   /v1/sessions/{id}/runs           {prompt | continue}            → run result
//	POST   /v1/sessions/{id}/runs/stream    {prompt | continue}            → SSE: event*, result
//	POST   /v1/sessions/{id}/cancel         cancel the run in flight
//	POST   /v1/runs                         {prompt, agent?, metadata?}    → create session + run
//	POST   /v1/runs/stream                  same, streaming
//	POST   /v1/runs/{run_id}/cancel
//
// Authentication, rate limiting, and logging belong to the embedding
// application: wrap the Handler with your own middleware.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// ContractVersion is the HTTP contract version. Field names and status codes
// documented here are stable within a version.
const ContractVersion = "2"

// maxBodyBytes bounds every JSON request body. A larger body is rejected with
// 413 rather than being buffered.
const maxBodyBytes = 1 << 20

// errBodyTooLarge marks a request body that exceeded maxBodyBytes.
var errBodyTooLarge = errors.New("request body too large")

// Handler serves the API.
type Handler struct {
	runner *service.Runner
	mux    *http.ServeMux
}

// New creates a Handler over runner.
func New(runner *service.Runner) *Handler {
	h := &Handler{runner: runner, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contract": ContractVersion})
	})
	h.mux.HandleFunc("GET /readyz", h.ready)
	h.mux.HandleFunc("GET /v1/agents", h.agents)
	h.mux.HandleFunc("POST /v1/sessions", h.createSession)
	h.mux.HandleFunc("GET /v1/sessions", h.listSessions)
	h.mux.HandleFunc("GET /v1/sessions/{id}", h.getSession)
	h.mux.HandleFunc("DELETE /v1/sessions/{id}", h.deleteSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/fork", h.fork)
	h.mux.HandleFunc("GET /v1/sessions/{id}/events", h.events)
	h.mux.HandleFunc("GET /v1/sessions/{id}/context", h.contextReport)
	h.mux.HandleFunc("POST /v1/sessions/{id}/compact", h.compact)
	h.mux.HandleFunc("POST /v1/sessions/{id}/runs", h.run)
	h.mux.HandleFunc("POST /v1/sessions/{id}/runs/stream", h.runStream)
	h.mux.HandleFunc("POST /v1/sessions/{id}/cancel", h.cancelSession)
	h.mux.HandleFunc("POST /v1/runs", h.run)
	h.mux.HandleFunc("POST /v1/runs/stream", h.runStream)
	h.mux.HandleFunc("POST /v1/runs/{run_id}/cancel", h.cancelRun)
	return h
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
	Keep int `json:"keep,omitempty"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// ---- handlers ---------------------------------------------------------------

func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	if h.runner.Draining() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "draining": true, "active_runs": h.runner.ActiveRuns()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active_runs": h.runner.ActiveRuns()})
}

func (h *Handler) agents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"agents": h.runner.Agents()})
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	var in createSessionRequest
	if err := decode(w, r, &in); err != nil {
		writeDecodeError(w, err)
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
		writeJSON(w, http.StatusCreated, session.SummaryOf(s))
		return
	}
	s, err := h.runner.CreateSession(r.Context(), in.Agent, in.Metadata)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, session.SummaryOf(s))
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := h.runner.Store().List(r.Context())
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	s, err := h.runner.Store().Get(r.Context(), r.PathValue("id"))
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
	writeJSON(w, http.StatusCreated, session.SummaryOf(child))
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	s, err := h.runner.Store().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for _, event := range s.Events() {
		if kind != "" && string(event.Kind) != kind {
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
	if in.Keep <= 0 {
		in.Keep = 4
	}
	result, err := h.runner.Compact(r.Context(), r.PathValue("id"), modelctx.CompactOptions{Keep: in.Keep})
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
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// runStream streams Events as Server-Sent Events (event: <kind>, data: JSON)
// and ends with a "result" event carrying the RunResult.
func (h *Handler) runStream(w http.ResponseWriter, r *http.Request) {
	request, err := h.runRequest(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	sink := func(event gotato.Event) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Kind, data); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	result, runErr := h.runner.StreamRun(r.Context(), request, sink)
	if runErr != nil && result.SessionID == "" {
		data, _ := json.Marshal(failureOf(runErr))
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		return
	}
	if errors.Is(runErr, service.ErrNotPersisted) {
		data, _ := json.Marshal(errorResponse{Error: http.StatusText(http.StatusInternalServerError), Message: runErr.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		return
	}
	data, _ := json.Marshal(result)
	fmt.Fprintf(w, "event: result\ndata: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}

func (h *Handler) cancelSession(w http.ResponseWriter, r *http.Request) {
	if err := h.runner.CancelSession(r.Context(), r.PathValue("id")); err != nil {
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

// StatusFor maps a runtime error to an HTTP status.
func StatusFor(err error) int {
	var runtimeErr *gotato.RuntimeError
	switch {
	case errors.Is(err, session.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case errors.As(err, &runtimeErr):
		switch runtimeErr.Code {
		case gotato.ErrInvalidArgument, gotato.ErrNotSupported:
			return http.StatusBadRequest
		case gotato.ErrBusy:
			return http.StatusConflict
		case gotato.ErrLimitExceeded:
			return http.StatusTooManyRequests
		case gotato.ErrInvalidState:
			return http.StatusConflict
		case gotato.ErrDeadlineExceeded:
			return http.StatusGatewayTimeout
		case gotato.ErrCancelled:
			return http.StatusRequestTimeout
		}
	}
	return http.StatusInternalServerError
}

func failureOf(err error) errorResponse {
	out := errorResponse{Error: http.StatusText(StatusFor(err)), Message: err.Error()}
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		out.Code = string(runtimeErr.Code)
	}
	return out
}

func writeFailure(w http.ResponseWriter, err error) {
	writeJSON(w, StatusFor(err), failureOf(err))
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: http.StatusText(status), Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
}
