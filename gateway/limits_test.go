package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

func TestRequestHeadersReturnsACopy(t *testing.T) {
	ctx := WithRequestHeaders(context.Background(), map[string]string{"x-session": "a"})
	RequestHeaders(ctx)["x-session"] = "mutated"
	if got := RequestHeaders(ctx)["x-session"]; got != "a" {
		t.Fatalf("a caller's write reached the context: x-session = %q", got)
	}
}

func TestHTTPTimeoutsAreSettingsWithTheirOldDefaults(t *testing.T) {
	transportOf := func(config Config) *http.Transport {
		t.Helper()
		config.BaseURL, config.Model = "http://127.0.0.1:1", "m"
		client, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		return client.httpClient.Transport.(*http.Transport)
	}
	def := transportOf(Config{})
	if def.ResponseHeaderTimeout != 60*time.Second || def.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("defaults changed: header %v, tls %v", def.ResponseHeaderTimeout, def.TLSHandshakeTimeout)
	}
	set := transportOf(Config{ConnectTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Minute})
	if set.ResponseHeaderTimeout != 5*time.Minute || set.TLSHandshakeTimeout != 3*time.Second {
		t.Fatalf("settings ignored: header %v, tls %v", set.ResponseHeaderTimeout, set.TLSHandshakeTimeout)
	}
	for _, config := range []Config{{ConnectTimeout: -1}, {ResponseHeaderTimeout: -1}, {MaxRetryWait: -1}} {
		config.BaseURL, config.Model = "http://127.0.0.1:1", "m"
		if _, err := New(config); err == nil {
			t.Errorf("%+v: a negative duration was accepted", config)
		}
	}
}

func TestRetryAfterIsCappedByMaxRetryWait(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", MaxRetries: 1, RetryBackoff: time.Millisecond, MaxRetryWait: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	stream, err := client.Stream(ctx, gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Retry-After: 3600 was waited out: %v", elapsed)
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", calls.Load())
	}
}

func TestParseYAMLReadsTheHTTPLimits(t *testing.T) {
	config, err := parseYAML([]byte(`
base_url: https://gateway.example.com/v1
model: m
connect_timeout: 4s
response_header_timeout: 3m
max_retry_wait: 20s
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnectTimeout != 4*time.Second || config.ResponseHeaderTimeout != 3*time.Minute || config.MaxRetryWait != 20*time.Second {
		t.Fatalf("config = %+v", config)
	}
}
