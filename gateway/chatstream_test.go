package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

func chatStreamOf(t *testing.T, chunks ...string) gotato.ModelStream {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			writeSSE(t, w, chunk)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("q")}})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

// A provider that puts cumulative usage in every chunk yields a Usage event
// per chunk, each replacing the one before; Done carries the last, so a
// consumer that takes the latest (as the loop does) counts it once.
func TestChatUsageInEveryChunkIsCumulativeAndDoneHoldsTheLast(t *testing.T) {
	stream := chatStreamOf(t,
		`{"choices":[{"delta":{"content":"a"}}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`,
		`{"choices":[{"delta":{"content":"b"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`,
		`[DONE]`)
	events, err := collectEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	var last gotato.Usage
	for _, event := range events {
		if event.Kind == gotato.ModelEventKindUsage {
			if event.Usage.TotalTokens < last.TotalTokens {
				t.Errorf("usage went down: %+v after %+v", event.Usage, last)
			}
			last = event.Usage
		}
	}
	done := events[len(events)-1]
	if done.Kind != gotato.ModelEventKindDone || done.Usage != last || last.TotalTokens != 13 {
		t.Errorf("done = %+v, last usage = %+v", done, last)
	}
}

// After a stream error, Recv keeps returning it: a second call must not read
// as a clean end of the stream.
func TestChatStreamErrorIsSticky(t *testing.T) {
	stream := chatStreamOf(t, `{"error":{"message":"boom","type":"server_error"}}`)
	_, first := stream.Recv(context.Background())
	if first == nil || errors.Is(first, io.EOF) {
		t.Fatalf("first Recv err = %v", first)
	}
	_, second := stream.Recv(context.Background())
	if second == nil || errors.Is(second, io.EOF) {
		t.Fatalf("second Recv err = %v; want the error again, not io.EOF", second)
	}
}
