package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/orchestration"
)

type asyncRunState struct {
	mu        sync.RWMutex
	record    orchestration.Record
	runID     gotato.RunID
	status    gotato.RunStatus
	metrics   gotato.RunMetrics
	heartbeat *runHeartbeat
	startedAt time.Time
	result    *gotato.RunResult
	err       error
	finished  bool
}

type progressFrame struct {
	Type string       `json:"type"`
	Run  *runResponse `json:"run,omitempty"`
}

func (s *Server) runProgress(w http.ResponseWriter, r *http.Request) {
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

	state, err := s.startAsyncRun(input.toOrchestrationRequest(), gotato.UserMessage(input.Prompt))
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "progress streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	writeFrame := func(frame progressFrame) error {
		if _, err := fmt.Fprintf(w, "%s\n", mustJSON(frame)); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := writeFrame(progressFrame{Type: "accepted", Run: ptrRunResponse(state.response())}); err != nil {
		return
	}

	var lastTurn gotato.TurnNumber
	var lastUpdated time.Time
	for {
		response := state.response()
		if heartbeat := response.Heartbeat; heartbeat != nil && (heartbeat.Turn != lastTurn || heartbeat.UpdatedAt.After(lastUpdated)) {
			loop := response
			loop.RunStatus = gotato.RunRunning
			loop.FinalMessage = ""
			loop.Usage = nil
			loop.Error = nil
			if err := writeFrame(progressFrame{Type: "loop", Run: ptrRunResponse(loop)}); err != nil {
				if s.CancelRunOnDisconnect {
					_ = s.Orchestration.CancelRun(context.Background(), state.runID)
				}
				return
			}
			lastTurn = heartbeat.Turn
			lastUpdated = heartbeat.UpdatedAt
		}
		if response.RunStatus != gotato.RunRunning {
			_ = writeFrame(progressFrame{Type: "result", Run: ptrRunResponse(response)})
			return
		}

		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			if s.CancelRunOnDisconnect {
				_ = s.Orchestration.CancelRun(context.Background(), state.runID)
			}
			return
		}
	}
}

func ptrRunResponse(response runResponse) *runResponse {
	return &response
}

func (s *Server) runAsync(w http.ResponseWriter, r *http.Request) {
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

	state, err := s.startAsyncRun(input.toOrchestrationRequest(), gotato.UserMessage(input.Prompt))
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	w.Header().Set("Location", "/v1/runs/"+string(state.runID))
	w.Header().Set("Retry-After", "2")
	writeJSON(w, http.StatusAccepted, state.response())
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	runID := gotato.RunID(r.PathValue("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run ID is required")
		return
	}
	s.runsMu.Lock()
	state, ok := s.runs[runID]
	s.runsMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	response := state.response()
	if response.RunStatus == gotato.RunRunning {
		w.Header().Set("Retry-After", "2")
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) startAsyncRun(request orchestration.Request, prompt gotato.Message) (*asyncRunState, error) {
	agent, record, err := s.Orchestration.Resolve(context.Background(), request)
	if err != nil {
		return nil, err
	}
	source, ok := agent.(gotato.EventSource)
	if !ok {
		return nil, fmt.Errorf("Agent does not expose Events")
	}
	stream, err := source.Subscribe(context.Background())
	if err != nil {
		return nil, err
	}

	dispatchCtx, cancelDispatch := context.WithCancel(context.Background())
	resultCh := make(chan dispatchResponse, 1)
	admittedCh := make(chan struct{}, 1)
	go func() {
		defer cancelDispatch()
		result, finalRecord, dispatchErr := s.Orchestration.DispatchWithAdmission(dispatchCtx, request, prompt, admittedCh)
		resultCh <- dispatchResponse{result: result, record: finalRecord, err: dispatchErr}
	}()

	eventCh := make(chan eventRead, 1)
	stopReader := make(chan struct{})
	var stopReaderOnce sync.Once
	stop := func() {
		stopReaderOnce.Do(func() {
			close(stopReader)
			_ = stream.Close()
		})
	}
	go readAsyncEvents(stream, eventCh, stopReader, admittedCh)

	startCtx, cancelStart := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStart()
	for {
		select {
		case received := <-eventCh:
			if received.err != nil {
				cancelDispatch()
				stop()
				if errors.Is(received.err, io.EOF) {
					return nil, fmt.Errorf("run event stream ended before run start")
				}
				return nil, received.err
			}
			if received.event.RunID == "" {
				continue
			}
			state := newAsyncRunState(record, received.event)
			s.storeRun(state)
			go s.monitorAsyncRun(state, eventCh, resultCh, stop)
			return state, nil
		case response := <-resultCh:
			if response.result.RunID == "" {
				cancelDispatch()
				stop()
				if response.err != nil {
					return nil, response.err
				}
				return nil, fmt.Errorf("run was rejected before start")
			}
			// Core normally publishes agent_start before Dispatch returns. Keep
			// this fallback so a fast implementation can still be polled.
			state := newAsyncRunState(record, gotato.Event{RunID: response.result.RunID, Timestamp: time.Now()})
			s.storeRun(state)
			resultCh <- response
			go s.monitorAsyncRun(state, eventCh, resultCh, stop)
			return state, nil
		case <-startCtx.Done():
			cancelDispatch()
			stop()
			return nil, fmt.Errorf("timed out waiting for run start")
		}
	}
}

func readAsyncEvents(stream gotato.EventStream, eventCh chan<- eventRead, stop <-chan struct{}, admitted <-chan struct{}) {
	select {
	case <-admitted:
	case <-stop:
		return
	}
	for {
		event, err := stream.Next(context.Background())
		select {
		case eventCh <- eventRead{event: event, err: err}:
		case <-stop:
			return
		}
		if err != nil {
			return
		}
	}
}

func newAsyncRunState(record orchestration.Record, event gotato.Event) *asyncRunState {
	startedAt := event.Timestamp
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &asyncRunState{
		record:    record,
		runID:     event.RunID,
		status:    gotato.RunRunning,
		startedAt: startedAt,
		metrics:   gotato.RunMetrics{ElapsedMS: time.Since(startedAt).Milliseconds()},
	}
}

func (s *Server) storeRun(state *asyncRunState) {
	s.runsMu.Lock()
	if s.runs == nil {
		s.runs = make(map[gotato.RunID]*asyncRunState)
	}
	s.runs[state.runID] = state
	s.runsMu.Unlock()
}

func (s *Server) monitorAsyncRun(state *asyncRunState, eventCh <-chan eventRead, resultCh <-chan dispatchResponse, stop func()) {
	defer stop()
	var resultReceived bool
	var terminalEvent bool
	for {
		select {
		case received := <-eventCh:
			if received.err != nil {
				if resultReceived {
					return
				}
				go func() {
					response := <-resultCh
					s.finishAsyncRun(state, response)
				}()
				return
			}
			applyRunEvent(state, received.event)
			if received.event.Kind == gotato.EventAgentEnd {
				terminalEvent = true
				if resultReceived {
					return
				}
			}
		case response := <-resultCh:
			s.finishAsyncRun(state, response)
			resultReceived = true
			if terminalEvent {
				return
			}
		}
	}
}

func (s *Server) finishAsyncRun(state *asyncRunState, response dispatchResponse) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.record = response.record
	state.status = response.result.Status
	if state.status == "" {
		state.status = gotato.RunFailed
	}
	state.metrics = response.result.Metrics
	state.err = response.err
	result := response.result.Clone()
	state.result = &result
	state.finished = true
}

func applyRunEvent(state *asyncRunState, event gotato.Event) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	state.metrics.ElapsedMS = time.Since(state.startedAt).Milliseconds()
	switch event.Kind {
	case gotato.EventTurnStart:
		if uint32(event.Turn) > state.metrics.Turns {
			state.metrics.Turns = uint32(event.Turn)
		}
	case gotato.EventToolExecutionStart:
		state.metrics.ToolCalls++
	case gotato.EventTurnEnd:
		if summary, ok := event.Payload["summary"].(map[string]any); ok {
			state.metrics.TextBytes += uint64Value(summary["text_bytes"])
			state.metrics.ReasoningBytes += uint64Value(summary["reasoning_bytes"])
		}
		state.heartbeat = &runHeartbeat{
			Turn:      event.Turn,
			ElapsedMS: state.metrics.ElapsedMS,
			Summary:   cloneAnyMap(event.Payload["summary"]),
			UpdatedAt: event.Timestamp,
		}
	}
}

func uint64Value(value any) uint64 {
	switch value := value.(type) {
	case uint64:
		return value
	case uint32:
		return uint64(value)
	case int:
		return uint64(value)
	case int64:
		return uint64(value)
	case float64:
		return uint64(value)
	default:
		return 0
	}
}

func cloneAnyMap(value any) map[string]any {
	input, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, item := range input {
		output[key] = item
	}
	return output
}

func (state *asyncRunState) response() runResponse {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.result != nil {
		response := responseFor(state.record, *state.result, state.err)
		response.Heartbeat = cloneHeartbeat(state.heartbeat)
		return response
	}
	metrics := state.metrics
	if !state.startedAt.IsZero() {
		metrics.ElapsedMS = time.Since(state.startedAt).Milliseconds()
	}
	response := runResponse{
		ConversationID:  string(state.record.ID),
		AgentID:         string(state.record.LiveAgentID),
		AgentGeneration: uint64(state.record.Generation),
		RunID:           string(state.runID),
		RunStatus:       state.status,
		Metrics:         &metrics,
		Heartbeat:       cloneHeartbeat(state.heartbeat),
	}
	return response
}

func cloneHeartbeat(input *runHeartbeat) *runHeartbeat {
	if input == nil {
		return nil
	}
	output := *input
	output.Summary = cloneAnyMap(input.Summary)
	return &output
}
