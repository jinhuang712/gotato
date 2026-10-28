package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// A finish reason the decoder does not know, and a call it cannot name, fail
// the stream with model_protocol_failure; neither is a clean end of turn.
func TestUnknownFinishAndUnnamedCallFailTheStream(t *testing.T) {
	cases := []struct {
		name   string
		api    API
		frames []string
	}{
		{"chat unknown finish_reason", APIChatCompletions, []string{
			`{"choices":[{"delta":{"content":"par"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"insufficient_system_resource"}]}`, `[DONE]`}},
		{"anthropic pause_turn", APIAnthropicMessages, []string{
			`{"type":"message_start","message":{"usage":{}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`}},
		{"anthropic unknown stop_reason", APIAnthropicMessages, []string{
			`{"type":"message_start","message":{"usage":{}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"brand_new"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`}},
		{"responses unknown status", APIResponses, []string{
			`{"type":"response.completed","response":{"status":"queued"}}`}},
		{"responses call without call_id", APIResponses, []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"echo"}}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{}"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`}},
		{"responses call without name", APIResponses, []string{
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, frame := range tc.frames {
					writeSSE(t, w, frame)
				}
			}))
			defer server.Close()
			client, err := New(Config{API: tc.api, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1, RetryBackoff: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			events, err := collectEvents(t, stream)
			if !gotato.IsCode(err, gotato.CodeModelProtocolFailure) {
				t.Fatalf("err = %v, want model_protocol_failure; events = %v", err, events)
			}
			for _, event := range events {
				if event.Kind == gotato.ModelEventKindDone {
					t.Fatalf("the stream ended cleanly: %+v", event)
				}
			}
		})
	}
}

// A server that sends "finish_reason": "" on a chunk that is not the last has
// not finished: a stream cut after such a chunk is cut, not a complete reply.
func TestEmptyFinishReasonIsNotAFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"The answer is fo"},"finish_reason":""}]}`+"\n\n")
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Model: "m", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(t, stream)
	var coded *Error
	if !errors.As(err, &coded) || !coded.Retryable() {
		t.Fatalf("err = %v after %d events, want a retryable cut-stream Error", err, len(events))
	}
	for _, event := range events {
		if event.Kind == gotato.ModelEventKindDone {
			t.Fatalf("a stream cut after an empty finish_reason ended as Done %q", event.StopReason)
		}
	}
}
