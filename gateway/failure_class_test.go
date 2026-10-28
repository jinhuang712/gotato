package gateway

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// streamErrorCases is one mid-stream provider error per wire, with whether it
// is transient. Every wire classifies by the same rule, and none of them
// sends the request again once the stream has started.
var streamErrorCases = []struct {
	name      string
	api       API
	frames    []string
	retryable bool
}{
	{"anthropic overloaded", APIAnthropicMessages, []string{`{"type":"message_start","message":{"usage":{}}}`, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`}, true},
	{"anthropic api_error", APIAnthropicMessages, []string{`{"type":"error","error":{"type":"api_error","message":"boom"}}`}, true},
	{"anthropic invalid request", APIAnthropicMessages, []string{`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`}, false},
	{"chat server_error type", APIChatCompletions, []string{`{"error":{"type":"server_error","message":"boom"}}`}, true},
	{"chat rate limit code", APIChatCompletions, []string{`{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`}, true},
	{"chat invalid request", APIChatCompletions, []string{`{"error":{"type":"invalid_request_error","message":"bad"}}`}, false},
	{"chat quota", APIChatCompletions, []string{`{"error":{"code":"insufficient_quota","type":"insufficient_quota","message":"no balance"}}`}, false},
	{"responses failed server_error", APIResponses, []string{`{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`}, true},
	{"responses error rate limit", APIResponses, []string{`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`}, true},
	{"responses failed invalid prompt", APIResponses, []string{`{"type":"response.failed","response":{"error":{"code":"invalid_prompt","message":"bad"}}}`}, false},
}

func TestStreamErrorsAreClassifiedAlikeOnEveryWire(t *testing.T) {
	for _, tc := range streamErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				for _, frame := range tc.frames {
					writeSSE(t, w, frame)
				}
			}))
			defer server.Close()
			client, err := New(Config{API: tc.api, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: 2, RetryBackoff: time.Millisecond})
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
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if gatewayErr.Retryable() != tc.retryable {
				t.Fatalf("Retryable() = %v, want %v (%v)", gatewayErr.Retryable(), tc.retryable, err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("requests = %d, want 1: a stream that started is never sent again by the gateway", got)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestTransportErrorsRetryOnlyWhatCanPass(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"unknown authority", x509.UnknownAuthorityError{}, false},
		{"bad certificate", x509.CertificateInvalidError{Reason: x509.Expired}, false},
		{"hostname mismatch", x509.HostnameError{Host: "h"}, false},
		{"no such host", &net.DNSError{Err: "no such host", Name: "typo.example", IsNotFound: true}, false},
		{"connection reset", syscall.ECONNRESET, true},
		{"timeout", timeoutError{}, true},
		{"refused", syscall.ECONNREFUSED, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return nil, tc.err
			})}
			client, err := New(Config{BaseURL: "http://example.invalid", Model: "m", HTTPClient: httpClient, MaxRetries: 2, RetryBackoff: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
			var gatewayErr *Error
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if gatewayErr.Retryable() != tc.retryable {
				t.Fatalf("Retryable() = %v, want %v", gatewayErr.Retryable(), tc.retryable)
			}
			want := int32(1)
			if tc.retryable {
				want = 3
			}
			if got := attempts.Load(); got != want {
				t.Fatalf("attempts = %d, want %d", got, want)
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("the cause is not reachable with errors.Is: %v", err)
			}
		})
	}
}
