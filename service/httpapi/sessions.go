package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
)

type createSessionRequest struct {
	Agent    string            `json:"agent,omitempty"`
	ID       string            `json:"id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type compactRequest struct {
	// KeepMessages is how many recent Messages stay as they are; without it the
	// latest Run's Turns do.
	KeepMessages int `json:"keep_messages,omitempty"`
	// MinTokens skips a stretch since the last Checkpoint estimated below it.
	MinTokens int `json:"min_tokens,omitempty"`
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	var in createSessionRequest
	if err := h.decode(w, r, &in); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := checkSessionHeader(r, in.ID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if in.ID != "" {
		s, err := h.runner.CreateSessionExclusive(r.Context(), in.Agent, in.ID, in.Metadata)
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set(SessionIDHeader, s.ID())
		writeJSON(w, http.StatusCreated, s.Info())
		return
	}
	s, err := h.runner.CreateSession(r.Context(), in.Agent, in.Metadata)
	if err != nil {
		writeFailure(w, err)
		return
	}
	w.Header().Set(SessionIDHeader, s.ID())
	writeJSON(w, http.StatusCreated, s.Info())
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
	s, err := h.runner.Record(r.Context(), r.PathValue("id"))
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
	writeJSON(w, http.StatusCreated, child.Info())
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
	report, err := h.runner.Inspect(r.Context(), r.PathValue("id"), service.InspectConfig{})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) compact(w http.ResponseWriter, r *http.Request) {
	var in compactRequest
	if err := h.decode(w, r, &in); err != nil {
		writeDecodeError(w, err)
		return
	}
	if in.KeepMessages < 0 || in.MinTokens < 0 {
		writeFailure(w, gotato.ErrorOf(gotato.CodeInvalidArgument, "keep_messages and min_tokens cannot be negative"))
		return
	}
	result, err := h.runner.Compact(r.Context(), r.PathValue("id"), modelctx.CompactConfig{KeepMessages: in.KeepMessages, MinTokens: in.MinTokens})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
