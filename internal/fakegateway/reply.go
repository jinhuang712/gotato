package fakegateway

import (
	"bytes"
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(marshal(body))
}

// marshal encodes v as the providers do, with < > and & as they are.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// sseWriter writes server-sent events and flushes each one.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) start() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	s.w.WriteHeader(http.StatusOK)
	s.flush()
}

func (s *sseWriter) flush() {
	if s.f != nil {
		s.f.Flush()
	}
}

// event writes one event. An empty name writes a data-only event.
func (s *sseWriter) event(name string, data any) {
	var buf bytes.Buffer
	if name != "" {
		buf.WriteString("event: ")
		buf.WriteString(name)
		buf.WriteString("\n")
	}
	buf.WriteString("data: ")
	switch d := data.(type) {
	case string:
		buf.WriteString(d)
	default:
		buf.Write(marshal(d))
	}
	buf.WriteString("\n\n")
	_, _ = s.w.Write(buf.Bytes())
	s.flush()
}

// cut closes the connection without ending the stream.
func (s *sseWriter) cut(pl *plan) {
	pl.outcome("cut")
	s.flush()
	panic(http.ErrAbortHandler)
}
