package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
)

// maxTimeoutMS is the largest timeout_ms whose millisecond-to-Duration
// conversion cannot overflow.
const maxTimeoutMS = int64(math.MaxInt64) / int64(time.Millisecond)

type runRequest struct {
	Prompt   string `json:"prompt,omitempty"`
	Continue bool   `json:"continue,omitempty"`
	// TimeoutMS bounds the run; it settles as deadline_exceeded.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// Agent binds the Session to a registered Agent for this Run and after;
	// it repairs a Session whose stored Agent is gone.
	Agent string `json:"agent,omitempty"`
}

func (h *Handler) runRequest(w http.ResponseWriter, r *http.Request) (service.RunRequest, error) {
	var in runRequest
	if err := h.decode(w, r, &in); err != nil {
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
		Prompt:    in.Prompt,
		Continue:  in.Continue,
		Agent:     strings.TrimSpace(in.Agent),
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
	// The stream answers 200 once the Run is starting, holding its Session. A
	// request refused before that (draining, at capacity, busy here or on
	// another Instance, a Session that does not exist, an unknown Agent) never
	// gets there: it answers the status and headers a run answers, before the
	// stream starts.
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
