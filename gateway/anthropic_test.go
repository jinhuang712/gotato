package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

func collectEvents(t *testing.T, stream gotato.ModelStream) ([]gotato.ModelEvent, error) {
	t.Helper()
	var events []gotato.ModelEvent
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

func TestAnthropicStreamNormalizesReasoningTextToolsAndUsage(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "secret" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("x-session-affinity"); got != "agent-1" {
			t.Errorf("configured header = %q", got)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, data := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"ping"}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"` + gatewayFunctionName("fs.read") + `","input":{}}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		} {
			writeSSE(t, w, data)
		}
	}))
	defer server.Close()

	client, err := New(Config{API: APIAnthropicMessages, BaseURL: server.URL, APIKey: "secret", Model: "model-m",
		Headers: map[string]string{"x-session-affinity": "agent-1"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{
		SystemInstructions: "system",
		Messages:           []gotato.Message{gotato.UserMessage("read a.go")},
		Tools:              []gotato.ToolSpec{{ID: "fs.read", Description: "Read a file", InputSchema: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if received["model"] != "model-m" || received["stream"] != true || received["max_tokens"] != float64(defaultAnthropicMaxTokens) {
		t.Fatalf("request = %+v", received)
	}
	kinds := make([]gotato.ModelEventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind
	}
	want := []gotato.ModelEventKind{gotato.ModelReasoningDelta, gotato.ModelReasoningDone, gotato.ModelTextDelta, gotato.ModelToolCall, gotato.ModelUsage, gotato.ModelDone}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event kinds = %v, want %v", kinds, want)
		}
	}
	var artifact map[string]string
	if err := json.Unmarshal(events[1].ReasoningArtifact, &artifact); err != nil || artifact["type"] != "thinking" || artifact["thinking"] != "plan" || artifact["signature"] != "sig-1" {
		t.Fatalf("reasoning artifact = %s (%v)", events[1].ReasoningArtifact, err)
	}
	call := events[3].ToolCall
	if call == nil || call.ID != "toolu_1" || call.ToolID != "fs.read" || string(call.Arguments) != `{"path":"a.go"}` {
		t.Fatalf("tool call = %+v", call)
	}
	usage := events[4].Usage
	if usage.InputTokens != 125 || usage.CacheReadTokens != 100 || usage.CacheWriteTokens != 20 || usage.OutputTokens != 7 || usage.TotalTokens != 132 {
		t.Fatalf("usage = %+v", usage)
	}
	if events[5].StopReason != gotato.StopToolCalls || events[5].Usage != usage {
		t.Fatalf("done = %+v", events[5])
	}
}

func TestAnthropicRequestReplaysReasoningMergesToolResultsAndPlacesCacheMarkers(t *testing.T) {
	thinking := []byte(`{"type":"thinking","thinking":"plan","signature":"sig-1"}`)
	request := gotato.ModelRequest{
		SystemInstructions: "system",
		Tools:              []gotato.ToolSpec{{ID: "fs.read"}, {ID: "fs.write"}},
		Messages: []gotato.Message{
			gotato.UserMessage("go"),
			{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{
				{Kind: gotato.ContentReasoning, Text: "plan", Signature: thinking},
				{Kind: gotato.ContentReasoning, Text: "unsigned"},
				{Kind: gotato.ContentText, Text: "reading"},
			}, ToolCalls: []gotato.ToolCall{
				{ID: "c1", ToolID: "fs.read", Arguments: []byte(`{"path":"a"}`)},
				{ID: "c2", ToolID: "fs.read"},
			}},
			{Role: gotato.RoleToolResult, Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "A"}},
				ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultOK}},
			{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c2", Status: gotato.ToolResultFailed, SafeError: "missing"}},
			gotato.UserMessage("continue"),
		},
		CacheBreakpoints: []gotato.CacheBreakpoint{
			{After: gotato.CacheAfterSystem},
			{After: gotato.CacheAfterTools},
			{After: gotato.CacheAfterMessage, Index: 3},
		},
		Options: gotato.ModelOptions{MaxTokens: 512},
	}
	body, _, err := encodeAnthropicRequest("m", request)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MaxTokens int                `json:"max_tokens"`
		System    []map[string]any   `json:"system"`
		Tools     []map[string]any   `json:"tools"`
		Messages  []anthropicMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != 512 {
		t.Fatalf("max_tokens = %d", got.MaxTokens)
	}
	if got.System[0]["cache_control"] == nil {
		t.Fatalf("system = %+v", got.System)
	}
	if got.Tools[0]["cache_control"] != nil || got.Tools[1]["cache_control"] == nil {
		t.Fatalf("tools = %+v", got.Tools)
	}
	if got.Tools[0]["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("default input_schema = %+v", got.Tools[0])
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	assistant := got.Messages[1]
	if assistant.Role != "assistant" || len(assistant.Content) != 4 {
		t.Fatalf("assistant = %+v", assistant)
	}
	if assistant.Content[0]["type"] != "thinking" || assistant.Content[0]["signature"] != "sig-1" {
		t.Fatalf("replayed thinking = %+v", assistant.Content[0])
	}
	if assistant.Content[3]["type"] != "tool_use" || assistant.Content[3]["input"].(map[string]any) == nil {
		t.Fatalf("tool_use with empty arguments = %+v", assistant.Content[3])
	}
	results := got.Messages[2]
	if results.Role != "user" || len(results.Content) != 3 {
		t.Fatalf("merged user turn = %+v", results)
	}
	if results.Content[0]["type"] != "tool_result" || results.Content[0]["is_error"] != nil || results.Content[0]["cache_control"] != nil {
		t.Fatalf("first tool_result = %+v", results.Content[0])
	}
	if results.Content[1]["is_error"] != true || results.Content[1]["content"] != "missing" || results.Content[1]["cache_control"] == nil {
		t.Fatalf("second tool_result = %+v", results.Content[1])
	}
	if results.Content[2]["type"] != "text" || results.Content[2]["text"] != "continue" {
		t.Fatalf("trailing user text = %+v", results.Content[2])
	}
}

func TestAnthropicCacheBreakpointsKeepTheLastFour(t *testing.T) {
	request := gotato.ModelRequest{SystemInstructions: "s"}
	for i := range 6 {
		request.Messages = append(request.Messages, gotato.UserMessage("u"), gotato.AssistantMessage("a"))
		request.CacheBreakpoints = append(request.CacheBreakpoints, gotato.CacheBreakpoint{After: gotato.CacheAfterMessage, Index: 2*i + 1})
	}
	request.CacheBreakpoints = append([]gotato.CacheBreakpoint{{After: gotato.CacheAfterSystem}}, request.CacheBreakpoints...)
	body, _, err := encodeAnthropicRequest("m", request)
	if err != nil {
		t.Fatal(err)
	}
	var got anthropicRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	marked := 0
	for i, message := range got.Messages {
		for _, block := range message.Content {
			if block["cache_control"] != nil {
				marked++
				if i < len(got.Messages)-8 {
					t.Fatalf("an early message kept a marker: message %d", i)
				}
			}
		}
	}
	if got.System[0]["cache_control"] != nil || marked != maxAnthropicCacheBreakpoints {
		t.Fatalf("system marker = %v, message markers = %d", got.System[0]["cache_control"], marked)
	}
}

func TestAnthropicRetriesOverloadedStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(529)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		writeSSE(t, w, `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
		writeSSE(t, w, `{"type":"message_stop"}`)
	}))
	defer server.Close()

	client, err := New(Config{API: "anthropic", BaseURL: server.URL, Model: "m", RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectEvents(t, stream)
	if err != nil || attempts.Load() != 2 || events[len(events)-1].StopReason != gotato.StopEndTurn {
		t.Fatalf("attempts = %d events = %+v err = %v", attempts.Load(), events, err)
	}
}

func TestAnthropicStreamErrorEventAndTruncation(t *testing.T) {
	for name, frames := range map[string][]string{
		"error event": {`{"type":"message_start","message":{"usage":{}}}`, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		"truncated":   {`{"type":"message_start","message":{"usage":{}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, frame := range frames {
					writeSSE(t, w, frame)
				}
			}))
			defer server.Close()
			client, err := New(Config{API: APIAnthropicMessages, BaseURL: server.URL, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			_, err = collectEvents(t, stream)
			var gatewayErr *Error
			switch name {
			case "error event":
				if !errors.As(err, &gatewayErr) || !gatewayErr.Retryable {
					t.Fatalf("err = %v", err)
				}
			case "truncated":
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("err = %v", err)
				}
			}
		})
	}
}

func TestAnthropicEndpointDefaults(t *testing.T) {
	for base, want := range map[string]string{
		"":                       "https://api.anthropic.com/v1/messages",
		"http://127.0.0.1:3456":  "http://127.0.0.1:3456/v1/messages",
		"http://127.0.0.1:3456/": "http://127.0.0.1:3456/v1/messages",
		"https://example.com/v1": "https://example.com/v1/messages",
	} {
		client, err := New(Config{API: APIAnthropicMessages, BaseURL: base, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if client.endpoint != want {
			t.Fatalf("base %q: endpoint = %q, want %q", base, client.endpoint, want)
		}
	}
}
