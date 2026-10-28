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
