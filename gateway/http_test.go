package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

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
	defer func() { _ = stream.Close() }()
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
	if gatewayErr.Retryable() {
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
	defer func() { _ = stream.Close() }()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("HTTP attempts = %d", calls.Load())
	}
}

// A transport error names the endpoint's URL and host; Error.Message is read
// by clients, so it is fixed text, and the transport error stays the cause.
func TestTransportErrorMessageDoesNotNameTheEndpoint(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	for name, endpoint := range map[string]string{"not retried (unknown authority)": tlsServer.URL, "retried and out of attempts": closedURL} {
		t.Run(name, func(t *testing.T) {
			client, err := New(Config{Endpoint: endpoint, Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Stream(context.Background(), gotato.ModelRequest{})
			var gerr *Error
			if !errors.As(err, &gerr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
			if strings.Contains(gerr.Message, host) || strings.Contains(gerr.Message, "127.0.0.1") {
				t.Errorf("Message %q names the endpoint", gerr.Message)
			}
			if gerr.Unwrap() == nil {
				t.Error("the transport error is not the cause")
			}
		})
	}
}

// A provider that answers 200 with a JSON error body, not a stream, has its
// message surfaced. The retry class is the table of mid-stream errors: an
// authentication error is not sent again, an overload is.
func TestA200WithAJSONErrorBodySurfacesTheProvidersError(t *testing.T) {
	for _, api := range []API{APIChatCompletions, APIResponses, APIAnthropicMessages} {
		for _, tc := range []struct {
			name, body string
			retryable  bool
			hits       int32
		}{
			{"auth", `{"error":{"message":"invalid api key","type":"authentication_error"}}`, false, 1},
			{"overloaded", `{"type":"error","error":{"message":"busy now","type":"overloaded_error"}}`, true, 3},
		} {
			t.Run(string(api)+"/"+tc.name, func(t *testing.T) {
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("\n " + tc.body))
				}))
				defer server.Close()
				client, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: 2, RetryBackoff: time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
				stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
				if err == nil {
					_, err = collectEvents(t, stream)
				}
				var failure *Error
				if !errors.As(err, &failure) || !strings.Contains(failure.Message, map[bool]string{false: "invalid api key", true: "busy now"}[tc.retryable]) {
					t.Fatalf("err = %v, want the provider's message", err)
				}
				if failure.Retryable() != tc.retryable || hits.Load() != tc.hits {
					t.Fatalf("retryable = %v after %d requests, want %v after %d", failure.Retryable(), hits.Load(), tc.retryable, tc.hits)
				}
			})
		}
	}
}
