package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/jinhuang712/gotato/service"
)

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

// message delivers a message to the Run in flight on this Instance (202), or,
// when no Run is going, starts a new Run with it and answers when that Run
// settles (200, as the run routes do). A Run another live Instance writes is
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
	case result.Run == nil:
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
// Run. For a Run another Instance writes it answers 409 not_writer, or is
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

// forwarded sends the request to the writer of the Run when err says another
// Instance writes it and forwarding is on, and relays the answer. It reports
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
	maps.Copy(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
	return true
}

func (h *Handler) abortSession(w http.ResponseWriter, r *http.Request) {
	if err := h.runner.AbortSession(r.Context(), r.PathValue("id")); err != nil {
		if h.forwarded(w, r, err, nil) {
			return
		}
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"session_id": r.PathValue("id"), "status": "abort_requested"})
}
