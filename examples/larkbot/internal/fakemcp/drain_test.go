package fakemcp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Drain waits for a call in flight, turns /healthz to 503 at once, and still answers the call.
func TestDrainLetsACallInFlightFinish(t *testing.T) {
	s := New(slog.New(slog.DiscardHandler), true)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	client := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	answered := make(chan string, 1)
	go func() {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "get_order", Arguments: map[string]any{"id": "o1", "delay": "400ms"}})
		if err != nil {
			answered <- "error: " + err.Error()
			return
		}
		text, ok := res.Content[0].(*sdk.TextContent)
		if !ok {
			answered <- "not text"
			return
		}
		answered <- text.Text
	}()
	for s.Stats().InFlight == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	drained := make(chan Drained, 1)
	go func() { drained <- s.Drain(ctx, 100*time.Millisecond) }()
	for !s.draining.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/healthz while draining = %d, want 503", resp.StatusCode)
	}
	if !resp.Close {
		t.Fatal("an answer while draining should close the connection")
	}
	if got := <-answered; got != "order o1: status paid, total 42.50" {
		t.Fatalf("the call in flight got %q", got)
	}
	d := <-drained
	if d.InFlightAtStart != 1 || d.FinishedDuring != 1 || d.Left != 0 || d.Took < 400*time.Millisecond {
		t.Fatalf("drained = %+v", d)
	}
}

// A drain whose context ends first reports the calls it left.
func TestDrainGivesUp(t *testing.T) {
	s := New(slog.New(slog.DiscardHandler), true)
	s.started.Add(1) // a call that never ends
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if d := s.Drain(ctx, time.Millisecond); d.Left != 1 || d.InFlightAtStart != 1 {
		t.Fatalf("drained = %+v", d)
	}
}
