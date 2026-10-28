package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
)

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
	if number := event.Head().Number; number > 0 && event.Kind().Class() == gotato.EventClassStored {
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
		// The keep-alive is the per-call timer running out, not any error that
		// wraps DeadlineExceeded, such as a Store's own.
		timer := next.Err() != nil && errors.Is(err, context.DeadlineExceeded)
		cancel()
		switch {
		case err == nil:
			if writeEvent(w, flusher, event) != nil {
				return
			}
		case r.Context().Err() != nil:
			return
		case timer:
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
