package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

func recvAll(t *testing.T, stream gotato.ModelStream) ([]gotato.ModelEvent, error) {
	t.Helper()
	var events []gotato.ModelEvent
	for {
		event, err := stream.Recv(context.Background())
		if err != nil {
			if err.Error() == "EOF" {
				return events, nil
			}
			return events, err
		}
		events = append(events, event)
	}
}

func TestStreamRestartsWhenInterruptedBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		drop func(t *testing.T, w http.ResponseWriter)
	}{
		{"connection reset", func(t *testing.T, w http.ResponseWriter) { dropConnection(t, w, roleChunk) }},
		{"clean close without [DONE]", func(t *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(roleChunk))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					tc.drop(t, w)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()
			client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 2, RetryBackoff: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			events, err := recvAll(t, stream)
			if err != nil {
				t.Fatalf("stream failed instead of restarting: %v", err)
			}
			if textOf(events) != "ok" || events[len(events)-1].Kind != gotato.ModelEventKindDone {
				t.Fatalf("events = %+v", events)
			}
			if calls.Load() != 2 {
				t.Fatalf("HTTP attempts = %d, want 2", calls.Load())
			}
		})
	}
}

func TestStreamIsNotReplayedAfterOutputBegan(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		dropConnection(t, w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 3, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := recvAll(t, stream)
	if textOf(events) != "partial" {
		t.Fatalf("delivered text = %q; output must not be duplicated or dropped", textOf(events))
	}
	var gerr *Error
	if !errors.As(err, &gerr) || !gerr.Retryable() || !strings.Contains(gerr.Message, "interrupted") {
		t.Fatalf("error = %v, want a retryable interruption", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP attempts = %d; a stream that delivered output must not be re-requested", calls.Load())
	}
}

func TestStreamRestartsAreBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		dropConnection(t, w, "")
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 2, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	_, err = recvAll(t, stream)
	var gerr *Error
	if !errors.As(err, &gerr) || !gerr.Retryable() {
		t.Fatalf("error = %v, want a retryable interruption", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("HTTP attempts = %d, want 1 + MaxRetries", calls.Load())
	}
}

func TestStreamRestartStopsOnCancellation(t *testing.T) {
	var calls atomic.Int32
	dropped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dropConnection(t, w, "")
		if calls.Add(1) == 1 {
			close(dropped)
		}
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 5, RetryBackoff: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Stream(ctx, gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	done := make(chan error, 1)
	go func() { _, err := stream.Recv(ctx); done <- err }()
	<-dropped // the stream is now waiting out the hour-long restart backoff
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled context did not stop the restart backoff")
	}
}

func TestStreamToleratesMissingDoneAfterFinishReason(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 2, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := recvAll(t, stream)
	if err != nil || textOf(events) != "done" || events[len(events)-1].Kind != gotato.ModelEventKindDone {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP attempts = %d; a finished stream must not be re-requested", calls.Load())
	}
}

func TestResponsesStreamRestartsWhenInterruptedBeforeOutput(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			dropConnection(t, w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		bw := bufio.NewWriter(w)
		for _, data := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		} {
			fmt.Fprintf(bw, "data: %s\n\n", data)
		}
		_ = bw.Flush()
	}))
	defer server.Close()
	client, err := New(Config{API: APIResponses, Endpoint: server.URL, APIKey: "k", Model: "m", MaxRetries: 1, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := recvAll(t, stream)
	if err != nil || textOf(events) != "ok" {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP attempts = %d, want 2", calls.Load())
	}
}

func TestResponsesStreamIsNotReplayedAfterOutputBegan(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		dropConnection(t, w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"partial\"}\n\n")
	}))
	defer server.Close()
	client, err := New(Config{API: APIResponses, Endpoint: server.URL, APIKey: "k", Model: "m", MaxRetries: 3, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := recvAll(t, stream)
	var gerr *Error
	if textOf(events) != "partial" || !errors.As(err, &gerr) || !gerr.Retryable() {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP attempts = %d, want 1", calls.Load())
	}
}

const anthropicStart = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"

func TestAnthropicStreamRestartsWhenInterruptedBeforeOutput(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			dropConnection(t, w, anthropicStart)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		bw := bufio.NewWriter(w)
		for _, data := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(bw, "data: %s\n\n", data)
		}
		_ = bw.Flush()
	}))
	defer server.Close()
	client, err := New(Config{API: APIAnthropicMessages, BaseURL: server.URL, Model: "m", MaxRetries: 1, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := recvAll(t, stream)
	if err != nil || textOf(events) != "ok" || events[len(events)-1].Kind != gotato.ModelEventKindDone {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP attempts = %d, want 2", calls.Load())
	}
}

func TestAnthropicStreamIsNotReplayedAfterOutputBegan(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		dropConnection(t, w, anthropicStart+
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
	}))
	defer server.Close()
	client, err := New(Config{API: APIAnthropicMessages, BaseURL: server.URL, Model: "m", MaxRetries: 3, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := recvAll(t, stream)
	var gerr *Error
	if textOf(events) != "partial" || !errors.As(err, &gerr) || !gerr.Retryable() {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP attempts = %d, want 1", calls.Load())
	}
}
