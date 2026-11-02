package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
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
	// A key under "gotato." in the metadata is the Runner's: CreateSession
	// refuses it, and writeFailure answers it 400 invalid_argument.
	info, err := h.runner.CreateSession(r.Context(), service.CreateSessionRequest{Agent: in.Agent, ID: in.ID, Metadata: in.Metadata})
	if err != nil {
		writeFailure(w, err)
		return
	}
	w.Header().Set(SessionIDHeader, info.ID)
	writeJSON(w, http.StatusCreated, h.summaryOf(r.Context(), info))
}

// sessionSummary is the answer to a create or a fork: the Session's listing
// columns and its Record epoch, which a client keeps to resume a stream.
type sessionSummary struct {
	record.Info
	RecordEpoch string `json:"record_epoch"`
}

// summaryOf adds the Record's epoch to a Session's listing columns. The create
// calls return the columns alone, so the epoch is read from the Record just made;
// a read that fails leaves it empty, and the client reads it with the Session.
func (h *Handler) summaryOf(ctx context.Context, info record.Info) sessionSummary {
	summary := sessionSummary{Info: info}
	if doc, err := h.runner.Record(ctx, info.ID); err == nil {
		summary.RecordEpoch = doc.RecordEpoch
	}
	return summary
}

// listSessions answers one page of Sessions: {"sessions": [...], "next": "..."},
// with next left out after the last page. limit and cursor are the query
// parameters of session.ListQuery; the cursor is the previous page's next.
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	var q session.ListQuery
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			writeFailure(w, gotato.ErrorOf(gotato.CodeInvalidArgument, "limit must be a non-negative integer"))
			return
		}
		q.Limit = limit
	}
	q.Cursor = r.URL.Query().Get("cursor")
	page, err := h.runner.Sessions(r.Context(), q)
	if err != nil {
		writeFailure(w, err)
		return
	}
	if page.Infos == nil {
		page.Infos = []record.Info{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	doc, err := h.runner.Record(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
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
	w.Header().Set(SessionIDHeader, child.ID)
	writeJSON(w, http.StatusCreated, h.summaryOf(r.Context(), child))
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	events, err := h.runner.Events(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, event := range events {
		if kind != "" && string(event.Kind()) != kind {
			continue
		}
		data, err := gotato.MarshalEvent(event)
		if err != nil {
			return
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
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
	// [CONTRACT] a negative bound is refused, not read as compact everything.
	if in.KeepMessages < 0 || in.MinTokens < 0 {
		writeFailure(w, gotato.ErrorOf(gotato.CodeInvalidArgument, "keep_messages and min_tokens cannot be negative"))
		return
	}
	result, err := h.runner.Compact(r.Context(), r.PathValue("id"), record.CompactConfig{KeepMessages: in.KeepMessages, MinTokens: in.MinTokens})
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
