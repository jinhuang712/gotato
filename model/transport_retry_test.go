package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// a transport-error retry must wait no longer than Retry.MaxWait.
func TestTransportRetryWaitIsCappedByMaxWait(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(ChatCompletions{}, Connection{Endpoint: server.URL, APIKey: "k"}, modelOf(gotato.ModelFacts{}),
		WithRetry(Retry{Max: 1, Backoff: 2 * time.Second, MaxWait: 20 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	recorder := testkit.RecordEvents(session)
	start := time.Now()
	if _, err := session.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	retries := recorder.OfKind(gotato.EventKindModelRetried)
	if len(retries) != 1 {
		t.Fatalf("model_retried Events = %d, want 1", len(retries))
	}
	retried := retries[0].(gotato.ModelRetried)
	if retried.WaitMS > 20 || elapsed > time.Second {
		t.Fatalf("transport retry waited %v ms (elapsed %s), want at most MaxWait 20ms", retried.WaitMS, elapsed)
	}
}
