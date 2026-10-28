package gateway

import (
	"context"
	"encoding/json"
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

func TestGatewayStreamNormalizesTextAndUsage(t *testing.T) {
	var received chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "gateway-model"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{SystemInstructions: "system", Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var events []gotato.ModelEvent
	for {
		event, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			break
		}
		events = append(events, event)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Kind != gotato.ModelEventKindTextDelta || events[0].Text != "hello" {
		t.Fatalf("text event = %+v", events[0])
	}
	if events[1].Kind != gotato.ModelEventKindUsage || events[1].Usage.TotalTokens != 5 {
		t.Fatalf("usage event = %+v", events[1])
	}
	if events[2].Kind != gotato.ModelEventKindDone || events[2].StopReason != gotato.StopReasonEndTurn {
		t.Fatalf("done event = %+v", events[2])
	}
	if received.Model != "gateway-model" || !received.Stream || len(received.Messages) != 2 {
		t.Fatalf("request = %+v", received)
	}
	if received.Messages[0].Role != "system" || received.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", received.Messages)
	}
}

func TestGatewayStreamReassemblesToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		name := gatewayFunctionName("demo.echo")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"` + name + `","arguments":"{\"value\":"}}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, APIKey: "secret", Model: "gateway-model"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Tools: []gotato.ToolSpec{{ID: "demo.echo", InputSchema: []byte(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	toolEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if toolEvent.Kind != gotato.ModelEventKindToolCall || toolEvent.ToolCall == nil {
		t.Fatalf("tool event = %+v", toolEvent)
	}
	if toolEvent.ToolCall.ToolID != "demo.echo" || string(toolEvent.ToolCall.Arguments) != `{"value":"x"}` {
		t.Fatalf("tool call = %+v", toolEvent.ToolCall)
	}
	doneEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if doneEvent.Kind != gotato.ModelEventKindDone || doneEvent.StopReason != gotato.StopReasonToolCalls {
		t.Fatalf("done event = %+v", doneEvent)
	}
}

// A call to a Tool that takes no arguments may stream with empty arguments.
// It must reach Core as {}, or Core fails it as invalid JSON and the Tool can
// never run.
func TestGatewayStreamDefaultsEmptyToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"now","arguments":""}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, APIKey: "secret", Model: "gateway-model"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Tools: []gotato.ToolSpec{{ID: "now", InputSchema: []byte(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	toolEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if toolEvent.Kind != gotato.ModelEventKindToolCall || toolEvent.ToolCall == nil || string(toolEvent.ToolCall.Arguments) != `{}` {
		t.Fatalf("tool event = %+v", toolEvent)
	}
}
