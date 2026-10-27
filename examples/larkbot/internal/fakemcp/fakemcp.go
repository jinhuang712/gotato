// Package fakemcp is a tiny MCP server for the cluster scenarios and the bot's tests: streamable HTTP on the
// official Go SDK, fixed data, and a tool whose answer waits as long as the caller asks.
//
//	get_order(id, delay)   one order of the shop; delay (a Go duration, "" for none) holds the answer
//	get_customer(id)       a customer
//	get_invoice(id)        an invoice
//	list_shipments(id)     the shipments of an order
//	get_inventory(id)      stock of one item
//
// The tools only read, and say so (ReadOnlyHint).
package fakemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type args struct {
	ID    string `json:"id,omitempty" jsonschema:"the id to look up"`
	Delay string `json:"delay,omitempty" jsonschema:"hold the answer this long, a Go duration such as 5s"`
}

// Server counts the calls it has seen; the counts start again from 0 when the process restarts.
type Server struct {
	started, finished, canceled atomic.Int64
	draining                    atomic.Bool
	handler                     http.Handler
	log                         *slog.Logger
	trace                       bool
}

// Stats are the calls a Server has seen.
type Stats struct {
	Started  int64 `json:"started"`
	Finished int64 `json:"finished"`
	Canceled int64 `json:"canceled"`  // the caller went away before the answer
	InFlight int64 `json:"in_flight"` // started and not yet finished or canceled
}

// New returns the server. Stateless makes it answer with no Mcp-Session-Id, so any replica can answer any call.
func New(log *slog.Logger, stateless bool) *Server {
	s := &Server{log: log}
	server := sdk.NewServer(&sdk.Implementation{Name: "fakemcp", Version: "1"}, nil)
	add := func(name, description, answer string) {
		sdk.AddTool(server, &sdk.Tool{Name: name, Description: description, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
			func(ctx context.Context, _ *sdk.CallToolRequest, in args) (*sdk.CallToolResult, any, error) {
				s.started.Add(1)
				if d, err := time.ParseDuration(in.Delay); err == nil && d > 0 {
					select {
					case <-time.After(d):
					case <-ctx.Done():
						s.canceled.Add(1)
						return nil, nil, ctx.Err()
					}
				}
				s.finished.Add(1)
				log.Info("call", "tool", name, "id", in.ID)
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf(answer, in.ID)}}}, nil, nil
			})
	}
	add("get_order", "Look up one order of the shop by its id: status and total.", "order %s: status paid, total 42.50")
	add("get_customer", "Look up a customer by id: name and city.", "customer %s: Ada Lovelace, London")
	add("get_invoice", "Look up an invoice by id: the amount due and the due date.", "invoice %s: due 2026-12-01, amount 120.00")
	add("list_shipments", "List the shipments of an order by the order's id: carrier and tracking number.", "order %s: 1 shipment, carrier DHL, tracking 7731")
	add("get_inventory", "Stock of one item in the warehouse by its id.", "item %s: 12 in stock")

	mux := http.NewServeMux()
	mux.Handle("/mcp", sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: stateless}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if s.draining.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable) // not ready: a readiness probe takes the pod out of its Service
			return
		}
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(s.Stats()) })
	s.handler = mux
	return s
}

// ServeHTTP serves /mcp (streamable HTTP), /healthz and /stats. While the server drains, every answer carries
// Connection: close, so a client with a kept-alive connection opens its next one through the Service, which no
// longer lists this pod.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		w.Header().Set("Connection", "close")
	}
	if !s.trace || r.URL.Path != "/mcp" {
		s.handler.ServeHTTP(w, r)
		return
	}
	start := time.Now()
	rec := &statusWriter{ResponseWriter: w}
	s.handler.ServeHTTP(rec, r)
	s.log.Info("request", "method", r.Method, "session", short(r.Header.Get("Mcp-Session-Id")), "status", rec.status, "took", time.Since(start).Round(time.Millisecond), "draining", s.draining.Load())
}

// Trace logs one line per /mcp request: method, session, status and time taken.
func (s *Server) Trace() { s.trace = true }

func short(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}

// statusWriter records the status a handler wrote; it passes Flush through, which the SSE streams need.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Stats returns the calls seen so far.
func (s *Server) Stats() Stats {
	started, finished, canceled := s.started.Load(), s.finished.Load(), s.canceled.Load()
	return Stats{Started: started, Finished: finished, Canceled: canceled, InFlight: started - finished - canceled}
}

// Drained says what Drain saw.
type Drained struct {
	InFlightAtStart int64         // calls in flight when the drain began
	StartedDuring   int64         // calls that arrived while it went on (a client with an open connection still sends them)
	FinishedDuring  int64         // calls that ended with their answer
	Left            int64         // calls still in flight when it stopped: cut off when the process exits
	Took            time.Duration // how long it went on
}

// Drain is the graceful part of a shutdown. /healthz answers 503 from now on, so a readiness probe takes the pod
// out of its Service, while the server keeps answering the calls it has and any that still arrive. It returns
// when no call has been in flight for linger (so a call that was already on its way is not cut), or when ctx ends.
func (s *Server) Drain(ctx context.Context, linger time.Duration) Drained {
	begin := time.Now()
	s.draining.Store(true)
	before := s.Stats()
	idleSince := time.Now()
	for ctx.Err() == nil {
		if s.Stats().InFlight > 0 {
			idleSince = time.Now()
		} else if time.Since(idleSince) >= linger {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	after := s.Stats()
	return Drained{
		InFlightAtStart: before.InFlight,
		StartedDuring:   after.Started - before.Started,
		FinishedDuring:  after.Finished - before.Finished,
		Left:            after.InFlight,
		Took:            time.Since(begin),
	}
}
