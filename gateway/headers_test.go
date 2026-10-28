package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// TestRequestHeadersFromContext: a host attaches per-conversation headers
// through the Run context; they override configuration but never protocol
// headers, and each request carries only its own context's values.
func TestRequestHeadersFromContext(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("X-Session")] = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "m", Headers: map[string]string{"User-Agent": "configured/1.0", "X-Team": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	request := gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}}

	for _, session := range []string{"conversation-1", "conversation-2"} {
		ctx := WithRequestHeaders(context.Background(), map[string]string{"X-Session": session, "Authorization": "Bearer stolen"})
		ctx = WithRequestHeaders(ctx, map[string]string{"X-Team": "b"})
		stream, err := client.Stream(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		drain(t, stream)
	}
	for _, session := range []string{"conversation-1", "conversation-2"} {
		h := seen[session]
		if h == nil {
			t.Fatalf("no request carried X-Session %s", session)
		}
		if h.Get("Authorization") != "Bearer secret" {
			t.Fatalf("per-request headers overrode authorization: %q", h.Get("Authorization"))
		}
		if h.Get("User-Agent") != "configured/1.0" {
			t.Fatalf("user agent = %q", h.Get("User-Agent"))
		}
		if h.Get("X-Team") != "b" {
			t.Fatalf("nested WithRequestHeaders did not merge innermost-wins: %q", h.Get("X-Team"))
		}
	}

	// Without configuration or context headers the default user agent applies.
	plain, _ := New(Config{BaseURL: server.URL, Model: "m"})
	stream, err := plain.Stream(WithRequestHeaders(context.Background(), map[string]string{"X-Session": "plain"}), request)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, stream)
	if got := seen["plain"].Get("User-Agent"); got != defaultUserAgent {
		t.Fatalf("default user agent = %q", got)
	}
	var unset context.Context
	if RequestHeaders(context.Background()) != nil || RequestHeaders(unset) != nil {
		t.Fatal("empty contexts carry no headers")
	}
}

// TestChatCacheReadTokens: both the OpenAI detail field and DeepSeek's field
// populate Usage.CacheReadTokens, clamped to the input count.
func TestChatCacheReadTokens(t *testing.T) {
	cases := map[string]uint64{
		`{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":64}}`: 64,
		`{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_cache_hit_tokens":80}`:                 80,
		`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":80}`:                   10,
		`{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}`:                                              0,
	}
	for usage, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}],\"usage\":" + usage + "}\n\ndata: [DONE]\n\n"))
		}))
		client, _ := New(Config{BaseURL: server.URL, Model: "m"})
		stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
		if err != nil {
			t.Fatal(err)
		}
		var got *gotato.Usage
		for _, ev := range drain(t, stream) {
			if ev.Kind == gotato.ModelEventKindUsage {
				u := ev.Usage
				got = &u
			}
		}
		server.Close()
		if got == nil || got.CacheReadTokens != want {
			t.Fatalf("usage %s: cached = %+v, want %d", usage, got, want)
		}
	}
}

// TestRequestHeadersReachEveryWireAPI: per-request and configured headers go
// out on every wire API, and none of them replaces authentication.
func TestRequestHeadersReachEveryWireAPI(t *testing.T) {
	for _, api := range []API{APIChatCompletions, APIResponses, APIAnthropicMessages} {
		t.Run(string(api), func(t *testing.T) {
			var seen http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Clone()
				w.Header().Set("Content-Type", "text/event-stream")
			}))
			defer server.Close()
			client, err := New(Config{API: api, BaseURL: server.URL, APIKey: "secret", Model: "m", Headers: map[string]string{"User-Agent": "configured/1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithRequestHeaders(context.Background(), map[string]string{"X-Session": "s1", "Authorization": "Bearer stolen", "X-Api-Key": "stolen"})
			stream, err := client.Stream(ctx, gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
			if err != nil {
				t.Fatal(err)
			}
			_ = stream.Close()
			if seen.Get("X-Session") != "s1" || seen.Get("User-Agent") != "configured/1.0" {
				t.Fatalf("headers = %v", seen)
			}
			if api == APIAnthropicMessages {
				if seen.Get("X-Api-Key") != "secret" {
					t.Fatalf("per-request headers overrode x-api-key: %q", seen.Get("X-Api-Key"))
				}
			} else if seen.Get("Authorization") != "Bearer secret" {
				t.Fatalf("per-request headers overrode authorization: %q", seen.Get("Authorization"))
			}
		})
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
	defer func() { _ = stream.Close() }()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" || accept != "text/event-stream" {
		t.Fatalf("protocol headers were overridden: auth=%q accept=%q", auth, accept)
	}
}

// A setting that can never work is refused by New, not found on the first
// request and sent again as if the network had failed.
func TestNewRefusesAHeaderOrEndpointThatCanNeverBeSent(t *testing.T) {
	for name, config := range map[string]Config{
		"cache key header": {BaseURL: "http://h", Model: "m", CacheKeyHeader: "session(id"},
		"header name":      {BaseURL: "http://h", Model: "m", Headers: map[string]string{"bad name": "v"}},
		"header value":     {BaseURL: "http://h", Model: "m", Headers: map[string]string{"X-A": "a\r\nX-B: b"}},
		"endpoint scheme":  {Endpoint: "ftp://h/x", Model: "m"},
		"endpoint no host": {Endpoint: "http:///x", Model: "m"},
		"endpoint garbage": {Endpoint: "http://h\x7f/x", Model: "m"},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("New accepted %s", name)
		}
	}
}

// A per-request header that net/http refuses fails the call once, not
// retryable.
func TestAnInvalidRequestHeaderIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Model: "m", MaxRetries: 3, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestHeaders(context.Background(), map[string]string{"bad name": "v"})
	_, err = client.Stream(ctx, gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	var failure *Error
	if !errors.As(err, &failure) || failure.Retryable() || hits.Load() != 0 {
		t.Fatalf("err = %v, retryable and %d requests; want a plain Error and no request", err, hits.Load())
	}
}
