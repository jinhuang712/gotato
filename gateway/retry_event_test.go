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
	session, err := gotato.NewSession(gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	recorder := testkit.RecordEvents(session)
	if _, err := session.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	retries := recorder.OfKind(gotato.KindModelRetried)
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
		case gotato.KindModelRetried:
			retry = i
		case gotato.KindReplyEnded:
			reply = i
		}
	}
	if retry < 0 || reply < retry {
		t.Fatalf("the retry came after the reply ended: %v", kinds)
	}
}
