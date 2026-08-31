package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/orchestration"
)

type Server struct {
	Orchestration         *orchestration.Orchestrator
	CancelRunOnDisconnect bool
	ready                 atomic.Bool
	draining              atomic.Bool
}

func NewServer(orchestrationLayer *orchestration.Orchestrator) *Server {
	s := &Server{Orchestration: orchestrationLayer}
	s.ready.Store(true)
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /v1/runs", s.run)
	mux.HandleFunc("POST /v1/runs/{run_id}/cancel", s.cancelRun)
	mux.HandleFunc("POST /v1/runs/stream", s.runStream)
	mux.HandleFunc("GET /v1/conversations/", s.conversation)
	mux.HandleFunc("POST /v1/conversations/", s.conversationCommand)
	mux.HandleFunc("POST /v1/agents/", s.agentCommand)
	mux.HandleFunc("POST /admin/drain", s.drain)
	return requestLog(mux)
}

func (s *Server) Drain(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.draining.Swap(true) {
		return nil
	}
	s.ready.Store(false)
	if s.Orchestration != nil {
		s.Orchestration.SetServing(false)
		return s.Orchestration.Drain(ctx)
	}
	return nil
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type runRequest struct {
	AgentName          string  `json:"agent_name"`
	ConversationID     string  `json:"conversation_id,omitempty"`
	ConversationKey    string  `json:"conversation_key,omitempty"`
	ExpectedGeneration *uint64 `json:"expected_generation,omitempty"`
	Prompt             string  `json:"prompt"`
}

type runResponse struct {
	ConversationID  string               `json:"conversation_id"`
	AgentID         string               `json:"agent_id,omitempty"`
	AgentGeneration uint64               `json:"agent_generation"`
	RunID           string               `json:"run_id,omitempty"`
	RunStatus       gotato.RunStatus     `json:"status,omitempty"`
	FinalMessage    string               `json:"final_message,omitempty"`
	Usage           *gotato.Usage        `json:"usage,omitempty"`
	Metrics         *gotato.RunMetrics   `json:"metrics,omitempty"`
	Error           *gotato.RuntimeError `json:"error,omitempty"`
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeError(w, http.StatusServiceUnavailable, "host is draining")
		return
	}
	var input runRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(input.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	request := input.toOrchestrationRequest()
	result, record, err := s.Orchestration.Dispatch(r.Context(), request, gotato.UserMessage(input.Prompt))
	status := http.StatusOK
	if err != nil {
		status = statusForError(err)
	}
	response := responseFor(record, result, err)
	writeJSON(w, status, response)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeError(w, http.StatusServiceUnavailable, "host is draining")
		return
	}
	runID := gotato.RunID(r.PathValue("run_id"))
	if err := s.Orchestration.CancelRun(r.Context(), runID); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "cancel_requested",
		"run_id": string(runID),
	})
}

func (s *Server) runStream(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeError(w, http.StatusServiceUnavailable, "host is draining")
		return
	}
	var input runRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(input.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	request := input.toOrchestrationRequest()
	agent, record, err := s.Orchestration.Resolve(r.Context(), request)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	source, ok := agent.(gotato.EventSource)
	if !ok {
		writeError(w, http.StatusNotImplemented, "Agent does not expose Events")
		return
	}
	stream, err := source.Subscribe(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer stream.Close()

	runCtx := r.Context()
	if !s.CancelRunOnDisconnect {
		runCtx = context.Background()
	}
	resultCh := make(chan dispatchResponse, 1)
	go func() {
		result, finalRecord, dispatchErr := s.Orchestration.Dispatch(runCtx, request, gotato.UserMessage(input.Prompt))
		resultCh <- dispatchResponse{result: result, record: finalRecord, err: dispatchErr}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	readCh := make(chan eventRead, 1)
	readNext := func() {
		go func() {
			event, nextErr := stream.Next(r.Context())
			readCh <- eventRead{event: event, err: nextErr}
		}()
	}
	readNext()
	var pending *dispatchResponse
	writeResult := func(response dispatchResponse) {
		fmt.Fprintf(w, "event: result\ndata: %s\n\n", mustJSON(responseFor(response.record, response.result, response.err)))
		flusher.Flush()
	}
	for {
		select {
		case response := <-resultCh:
			pending = &response
			// No RunID means dispatch was rejected before Core accepted a Run.
			if response.result.RunID == "" {
				writeResult(response)
				return
			}
		case received := <-readCh:
			if received.err != nil {
				if pending != nil {
					writeResult(*pending)
				}
				if errors.Is(received.err, io.EOF) || r.Context().Err() != nil {
					return
				}
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": received.err.Error()}))
				flusher.Flush()
				return
			}
			payload := map[string]any{
				"conversation_id":  string(record.ID),
				"agent_generation": record.Generation,
				"event":            received.event,
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", received.event.Kind, mustJSON(payload))
			flusher.Flush()
			if received.event.Kind == gotato.EventAgentEnd {
				if pending == nil {
					response := <-resultCh
					pending = &response
				}
				writeResult(*pending)
				return
			}
			readNext()
		}
	}
}

type dispatchResponse struct {
	result gotato.RunResult
	record orchestration.Record
	err    error
}

type eventRead struct {
	event gotato.Event
	err   error
}

func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/conversations/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "conversation ID is required")
		return
	}
	record, ok := s.Orchestration.Get(gotato.ConversationID(id))
	if !ok {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) conversationCommand(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/conversations/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "retire" {
		writeError(w, http.StatusNotFound, "unknown conversation command")
		return
	}
	var body struct {
		Policy orchestration.RetirementPolicy `json:"policy"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if body.Policy == "" {
		body.Policy = orchestration.Retain
	}
	err := s.Orchestration.Retire(r.Context(), gotato.ConversationID(parts[0]), body.Policy)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	record, _ := s.Orchestration.Get(gotato.ConversationID(parts[0]))
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) agentCommand(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/agents/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "close" {
		writeError(w, http.StatusNotFound, "unknown Agent command")
		return
	}
	if err := s.Orchestration.CloseAgent(r.Context(), gotato.AgentID(parts[0])); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed", "agent_id": parts[0]})
}

func (s *Server) drain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.Drain(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "drained"})
}

func (in runRequest) toOrchestrationRequest() orchestration.Request {
	request := orchestration.Request{AgentName: gotato.AgentName(in.AgentName), ConversationID: gotato.ConversationID(in.ConversationID), ConversationKey: gotato.ConversationKey(in.ConversationKey)}
	if in.ExpectedGeneration != nil {
		generation := gotato.AgentGeneration(*in.ExpectedGeneration)
		request.ExpectedGeneration = &generation
	}
	return request
}

func responseFor(record orchestration.Record, result gotato.RunResult, err error) runResponse {
	response := runResponse{ConversationID: string(record.ID), AgentID: string(record.LiveAgentID), AgentGeneration: uint64(record.Generation), RunID: string(result.RunID), RunStatus: result.Status, Error: result.Error}
	if result.RunID != "" {
		metrics := result.Metrics
		response.Metrics = &metrics
		if result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0 || result.Usage.TotalTokens != 0 {
			usage := result.Usage
			response.Usage = &usage
		}
	}
	if result.FinalMessage != nil {
		response.FinalMessage = gotato.TextOf(*result.FinalMessage)
	}
	if err != nil && response.Error == nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(err, &runtimeErr) {
			response.Error = runtimeErr
		} else {
			response.Error = &gotato.RuntimeError{Code: gotato.ErrInternalInvariant, Message: err.Error()}
		}
	}
	return response
}

func statusForError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var runtimeErr *gotato.RuntimeError
	if errors.As(err, &runtimeErr) {
		switch runtimeErr.Code {
		case gotato.ErrInvalidArgument:
			return http.StatusBadRequest
		case gotato.ErrAgentClosed:
			return http.StatusNotFound
		case gotato.ErrBusy, gotato.ErrInvalidState, gotato.ErrAgentClosing:
			return http.StatusConflict
		case gotato.ErrCancelled, gotato.ErrDeadlineExceeded:
			return http.StatusRequestTimeout
		case gotato.ErrRetirementFailed:
			return http.StatusInternalServerError
		}
	}
	return http.StatusInternalServerError
}

func decodeJSON(r *http.Request, value any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func mustJSON(value any) string { bytes, _ := json.Marshal(value); return string(bytes) }
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r) })
}
