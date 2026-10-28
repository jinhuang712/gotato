package gateway

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

// A gateway that calls the model again says so: the Session stores a
// model_retried Event with the attempt, the wait, and why, without the
// provider's own text.
func TestGatewayRetryIsAnEvent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "secret provider detail", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "gateway-model", MaxRetries: 1, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	recorder := testkit.RecordEvents(session)
	if _, err := session.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	retries := recorder.OfKind(gotato.EventKindModelRetried)
	if len(retries) != 1 {
		t.Fatalf("model_retried Events = %d, want 1: %v", len(retries), recorder.Kinds())
	}
	retried := retries[0].(gotato.ModelRetried)
	if retried.Attempt != 1 || retried.Error != "the provider answered 503" || retried.RunID == "" || retried.Number == 0 {
		t.Fatalf("model_retried = %+v", retried)
	}
	kinds := recorder.Kinds()
	retry, reply := -1, -1
	for i, kind := range kinds {
		switch kind {
		case gotato.EventKindModelRetried:
			retry = i
		case gotato.EventKindReplyEnded:
			reply = i
		}
	}
	if retry < 0 || reply < retry {
		t.Fatalf("the retry came after the reply ended: %v", kinds)
	}
}

// Every wire API reports its retries the same way: one model_retried Event
// per repeated request, with the attempt, the wait and why. The three APIs
// share one table so they cannot drift apart again.
func TestEveryAPIReportsRetriesAsEvents(t *testing.T) {
	apis := []struct {
		name   string
		config Config
		reply  func(t *testing.T, w http.ResponseWriter)
	}{
		{"chat completions", Config{API: APIChatCompletions}, func(_ *testing.T, w http.ResponseWriter) {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
		}},
		{"anthropic messages", Config{API: APIAnthropicMessages}, func(t *testing.T, w http.ResponseWriter) {
			writeSSE(t, w, `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
			writeSSE(t, w, `{"type":"message_stop"}`)
		}},
		{"openai responses", Config{API: APIResponses, APIKey: "key"}, func(t *testing.T, w http.ResponseWriter) {
			writeSSE(t, w, `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		}},
	}
	failures := []struct {
		name  string
		fail  func(t *testing.T, w http.ResponseWriter)
		error string
	}{
		{"status", func(_ *testing.T, w http.ResponseWriter) {
			http.Error(w, "secret provider detail", http.StatusServiceUnavailable)
		}, "the provider answered 503"},
		{"transport", func(t *testing.T, w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		}, "the request could not be sent"},
	}
	for _, api := range apis {
		for _, failure := range failures {
			t.Run(api.name+"/"+failure.name, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if calls.Add(1) == 1 {
						failure.fail(t, w)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					api.reply(t, w)
				}))
				defer server.Close()
				config := api.config
				config.Endpoint, config.Model, config.MaxRetries, config.RetryBackoff = server.URL, "m", 1, time.Millisecond
				client, err := New(config)
				if err != nil {
					t.Fatal(err)
				}
				session, err := gotato.NewSession(context.Background(), gotato.WithModel(client))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = session.Close(context.Background()) }()
				recorder := testkit.RecordEvents(session)
				if _, err := session.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
					t.Fatal(err)
				}
				retries := recorder.OfKind(gotato.EventKindModelRetried)
				if len(retries) != 1 {
					t.Fatalf("model_retried Events = %d, want 1: %v", len(retries), recorder.Kinds())
				}
				retried := retries[0].(gotato.ModelRetried)
				if retried.Attempt != 1 || retried.WaitMS <= 0 || retried.Error != failure.error {
					t.Fatalf("model_retried = %+v, want attempt 1, a wait, %q", retried, failure.error)
				}
			})
		}
	}
}
