package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// drainGate answers once its gate opens, and reports each call as it starts.
type drainGate struct {
	gate    chan struct{}
	started chan struct{}
}

func (m *drainGate) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	m.started <- struct{}{}
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.NewFakeModel(testkit.Text("finished")).Stream(ctx, gotato.ModelRequest{})
}

// startedModel reports each call of a hanging model as it starts.
type startedModel struct {
	hangingModel
	started chan struct{}
}

func (m startedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.started <- struct{}{}
	return m.hangingModel.Stream(ctx, request)
}

// sseFrame is one server-sent event.
type sseFrame struct {
	event, retry, data string
}

// readFrames reads a whole SSE body into its frames.
func readFrames(body io.Reader) ([]sseFrame, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	var frames []sseFrame
	for _, block := range strings.Split(strings.TrimSpace(string(raw)), "\n\n") {
		var frame sseFrame
		for _, line := range strings.Split(block, "\n") {
			field, value, _ := strings.Cut(line, ": ")
			switch field {
			case "event":
				frame.event = value
			case "retry":
				frame.retry = value
			case "data":
				frame.data = value
			}
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// openStream starts a streaming Run and reads its frames in the background.
func openStream(t *testing.T, server *httptest.Server, path, body string) <-chan []sseFrame {
	t.Helper()
	response, err := http.Post(server.URL+path, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan []sseFrame, 1)
	go func() {
		defer func() { _ = response.Body.Close() }()
		frames, _ := readFrames(response.Body)
		out <- frames
	}()
	return out
}

// TestDrainEndsStreamsInOrder: while an instance drains, /readyz answers 503
// and a new Run is refused; a Run that finishes in time ends its stream with
// its result, as always; and a Run the drain stops at its deadline ends its
// stream with its result and then a reconnect frame.
func TestDrainEndsStreamsInOrder(t *testing.T) {
	gated := &drainGate{gate: make(chan struct{}), started: make(chan struct{}, 4)}
	openGate := sync.OnceFunc(func() { close(gated.gate) })
	t.Cleanup(openGate)
	hanging := startedModel{started: make(chan struct{}, 4)}
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "gated", Model: gated}, {Name: "hanging", Model: hanging}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	ctx := context.Background()
	finishing, err := runner.CreateSession(ctx, "gated", nil)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := runner.CreateSession(ctx, "hanging", nil)
	if err != nil {
		t.Fatal(err)
	}
	finished := openStream(t, server, "/v1/sessions/"+finishing.ID()+"/runs/stream", `{"prompt":"finish"}`)
	cut := openStream(t, server, "/v1/sessions/"+stopped.ID()+"/runs/stream", `{"prompt":"wait"}`)
	<-gated.started
	<-hanging.started
	if status, body := call(t, server, http.MethodGet, "/readyz", nil); status != http.StatusOK {
		t.Fatalf("readyz before the drain = %d %s", status, body)
	}

	drainCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- runner.Drain(drainCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !runner.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the Runner did not start draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status, body := call(t, server, http.MethodGet, "/readyz", nil); status != http.StatusServiceUnavailable || !strings.Contains(string(body), `"draining":true`) {
		t.Fatalf("readyz while draining = %d %s", status, body)
	}
	// A new Run while draining is refused before the stream starts, with the
	// status /v1/runs answers: 503 marked not run, so a load balancer sends it
	// to another instance.
	for _, path := range []string{"/v1/runs", "/v1/runs/stream"} {
		resp, body := send(t, server, http.MethodPost, path, map[string]any{"prompt": "new", "agent": "gated"}, nil)
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get(httpapi.NotRunHeader) != "true" ||
			!strings.Contains(string(body), `"code":"draining"`) || !strings.Contains(string(body), `"not_run":true`) || !strings.Contains(string(body), `"retryable":true`) {
			t.Fatalf("a new Run on %s while draining = %d %s (not run %q), want 503, draining, not run, retryable", path, resp.StatusCode, body, resp.Header.Get(httpapi.NotRunHeader))
		}
	}

	openGate()
	frames := <-finished
	if last := frames[len(frames)-1]; last.event != "result" || !strings.Contains(last.data, `"status":"completed"`) || strings.Contains(last.data, "reconnect") {
		t.Fatalf("a Run that finished in the drain ends with %+v, want its completed result", last)
	}

	if err := <-drained; err == nil {
		t.Fatal("drain = nil, want its deadline")
	}
	frames = <-cut
	if len(frames) < 2 {
		t.Fatalf("a stopped Run's stream = %+v", frames)
	}
	result, reconnect := frames[len(frames)-2], frames[len(frames)-1]
	if result.event != "result" || !strings.Contains(result.data, `"status":"cancelled"`) {
		t.Fatalf("a stopped Run's result = %+v, want cancelled", result)
	}
	var hint service.Reconnect
	if reconnect.event != "reconnect" || json.Unmarshal([]byte(reconnect.data), &hint) != nil || hint.RetryAfterMS <= 0 || reconnect.retry == "" {
		t.Fatalf("a stopped Run's stream ends with %+v, want a reconnect frame with a delay", reconnect)
	}
	if reconnect.retry != strconv.FormatInt(hint.RetryAfterMS, 10) {
		t.Fatalf("retry %q and retry_after_ms in %s differ", reconnect.retry, reconnect.data)
	}
	// By then its Session is released.
	loaded, err := runner.Store().Load(ctx, stopped.ID())
	if err != nil || loaded.Summary.Busy {
		t.Fatalf("the stopped Run's Session = %+v, %v; want released", loaded.Summary, err)
	}
}
