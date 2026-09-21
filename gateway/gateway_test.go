package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

func TestGatewayStreamNormalizesTextAndUsage(t *testing.T) {
	var received wireRequest
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
	defer stream.Close()
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
	if events[0].Kind != gotato.ModelTextDelta || events[0].Text != "hello" {
		t.Fatalf("text event = %+v", events[0])
	}
	if events[1].Kind != gotato.ModelUsage || events[1].Usage.TotalTokens != 5 {
		t.Fatalf("usage event = %+v", events[1])
	}
	if events[2].Kind != gotato.ModelDone || events[2].StopReason != gotato.StopEndTurn {
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
	defer stream.Close()
	toolEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if toolEvent.Kind != gotato.ModelToolCall || toolEvent.ToolCall == nil {
		t.Fatalf("tool event = %+v", toolEvent)
	}
	if toolEvent.ToolCall.ToolID != "demo.echo" || string(toolEvent.ToolCall.Arguments) != `{"value":"x"}` {
		t.Fatalf("tool call = %+v", toolEvent.ToolCall)
	}
	doneEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if doneEvent.Kind != gotato.ModelDone || doneEvent.StopReason != gotato.StopToolCalls {
		t.Fatalf("done event = %+v", doneEvent)
	}
}

func TestGatewayHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("retry-after-ms", "50")
			http.Error(w, "slow down", http.StatusTooManyRequests)
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
	start := time.Now()
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP attempts = %d", calls.Load())
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("retry did not honor retry-after-ms: %s", elapsed)
	}
}

func TestGatewayDoesNotRetryTerminalQuota(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"You exceeded your current quota: insufficient_quota"}}`))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "gateway-model", MaxRetries: 3, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Stream(context.Background(), gotato.ModelRequest{})
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("err = %v", err)
	}
	if gatewayErr.Retryable {
		t.Fatalf("terminal quota 429 marked retryable: %+v", gatewayErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP attempts = %d, want 1", calls.Load())
	}
}

func TestGatewayRetriesBeforeStreamStarts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "gateway-model", MaxRetries: 1, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP attempts = %d", calls.Load())
	}
}

func TestConfigHeadersCannotOverrideProtocolHeaders(t *testing.T) {
	var auth, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		accept = r.Header.Get("Accept")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{
		Endpoint: server.URL, Model: "m", APIKey: "secret",
		Headers: map[string]string{"Authorization": "Bearer attacker", "Accept": "application/json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" || accept != "text/event-stream" {
		t.Fatalf("protocol headers were overridden: auth=%q accept=%q", auth, accept)
	}
}

func TestStreamRejectsOversizedSSELine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: " + strings.Repeat("x", maxSSELineBytes+10)))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized SSE line err = %v", err)
	}
}

func TestConvertMessageToolResultKeepsJSONContent(t *testing.T) {
	message := gotato.Message{
		Role:       gotato.RoleToolResult,
		ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultOK},
		Parts:      []gotato.ContentPart{{Kind: gotato.ContentJSON, Text: `{"summary":"sunny"}`}},
	}
	wire, err := convertMessage(message, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Role != "tool" || wire.Content != `{"summary":"sunny"}` || wire.ToolCallID != "c1" {
		t.Fatalf("wire = %+v", wire)
	}
}

func TestConvertMessageToolResultFallsBackToSafeError(t *testing.T) {
	message := gotato.Message{
		Role:       gotato.RoleToolResult,
		ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultFailed, SafeError: "upstream is down"},
	}
	wire, err := convertMessage(message, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if wire.Content != "upstream is down" {
		t.Fatalf("content = %q", wire.Content)
	}
}
