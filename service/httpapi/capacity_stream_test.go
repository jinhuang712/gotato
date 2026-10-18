package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A Run refused at capacity is known before any byte is written, so the
// streaming routes answer it as /v1/runs does, 429, and not with a 200 and an
// error in the stream. It is not marked not run: that is not decided here.
func TestStreamingRoutesAnswerCapacityBeforeTheStream(t *testing.T) {
	gate := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("done"))
	model.Block = gate
	runner, err := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "gated", Model: model}},
		Admission: service.Admission{MaxActiveRuns: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = runner.Run(context.Background(), service.RunRequest{Prompt: "hold"})
	}()
	t.Cleanup(func() { close(gate); <-held })
	deadline := time.Now().Add(5 * time.Second)
	for model.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for _, path := range []string{"/v1/runs", "/v1/runs/stream"} {
		resp, data := send(t, server, http.MethodPost, path, map[string]any{"prompt": "new"}, nil)
		if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Gotato-Not-Run") != "" {
			t.Fatalf("%s at capacity = %d %s, mark %q; want 429, not marked", path, resp.StatusCode, data, resp.Header.Get("Gotato-Not-Run"))
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s started a stream", path)
		}
	}
}
