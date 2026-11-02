package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// headerServer answers every call with the wire's text reply and records the
// headers of the last request.
type headerServer struct {
	*httptest.Server
	mu   sync.Mutex
	last http.Header
}

func newHeaderServer(t *testing.T, api API) *headerServer {
	t.Helper()
	s := &headerServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.last = r.Header.Clone()
		s.mu.Unlock()
		writeReply(t, w, textReply[api])
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *headerServer) header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// send builds a Client for the wire over the server and streams one request.
func (s *headerServer) send(t *testing.T, wire WireAPI, conn Connection, model gotato.ModelConfig, ctx context.Context, request gotato.ModelRequest) http.Header {
	t.Helper()
	conn.Endpoint = s.URL
	client, err := New(wire, conn, model, WithRetry(Retry{}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, stream)
	return s.header()
}

func hello() gotato.ModelRequest {
	return gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}}
}

// The key goes where the wire's protocol wants it, and nowhere when there is
// none (a keyless local server).
func TestAPIKeyIsPlacedByTheWire(t *testing.T) {
	for _, tc := range []struct {
		api        API
		key        string
		authHeader string
		want       string
		absent     []string
	}{
		{APIChatCompletions, "sk-1", "Authorization", "Bearer sk-1", []string{"X-Api-Key"}},
		{APIResponses, "sk-1", "Authorization", "Bearer sk-1", []string{"X-Api-Key"}},
		{APIAnthropicMessages, "sk-1", "X-Api-Key", "sk-1", []string{"Authorization"}},
		{APIChatCompletions, "", "Authorization", "", []string{"X-Api-Key"}},
		{APIResponses, "", "Authorization", "", []string{"X-Api-Key"}},
		{APIAnthropicMessages, "", "X-Api-Key", "", []string{"Authorization"}},
	} {
		server := newHeaderServer(t, tc.api)
		header := server.send(t, wireOf(tc.api), Connection{APIKey: tc.key}, modelOf(gotato.ModelFacts{}), context.Background(), hello())
		if got := header.Get(tc.authHeader); got != tc.want {
			t.Errorf("%s key %q: %s = %q, want %q", tc.api, tc.key, tc.authHeader, got, tc.want)
		}
		for _, name := range tc.absent {
			if got := header.Get(name); got != "" {
				t.Errorf("%s key %q: %s = %q, want none", tc.api, tc.key, name, got)
			}
		}
	}
}

// A configured header cannot replace the credential or the stream negotiation
// the wire sets; the wire's headers win.
func TestConfiguredHeadersCannotReplaceTheWiresOwn(t *testing.T) {
	configured := map[string]string{
		"Authorization": "Bearer stolen", "x-api-key": "stolen",
		"Content-Type": "text/plain", "Accept": "*/*", "anthropic-version": "1999-01-01",
		"X-Team": "a",
	}
	for _, api := range wireAPIs {
		server := newHeaderServer(t, api)
		header := server.send(t, wireOf(api), Connection{APIKey: "real", Headers: configured}, modelOf(gotato.ModelFacts{}), context.Background(), hello())
		if got := header.Get("Content-Type"); got != "application/json" {
			t.Errorf("%s: Content-Type = %q", api, got)
		}
		if got := header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("%s: Accept = %q", api, got)
		}
		if got := header.Get("X-Team"); got != "a" {
			t.Errorf("%s: X-Team = %q", api, got)
		}
		switch api {
		case APIAnthropicMessages:
			if got := header.Get("X-Api-Key"); got != "real" {
				t.Errorf("%s: x-api-key = %q", api, got)
			}
			// The credential of the other protocol is a configured header like
			// any other: the wire does not remove it.
		default:
			if got := header.Get("Authorization"); got != "Bearer real" {
				t.Errorf("%s: Authorization = %q", api, got)
			}
		}
	}
}

// The configured headers apply under the request context's, which apply under
// the wire's; the cache key header comes last of the two.
func TestHeaderPrecedenceIsConnectionThenContextThenWire(t *testing.T) {
	server := newHeaderServer(t, APIChatCompletions)
	ctx := WithRequestHeaders(context.Background(), map[string]string{"X-Team": "from-context", "X-Run": "r1"})
	request := hello()
	request.CacheKey = "ck"
	header := server.send(t, ChatCompletions{}, Connection{APIKey: "k", CacheKeyHeader: "session-id",
		Headers: map[string]string{"X-Team": "from-connection", "X-Fixed": "f", "User-Agent": "team-agent/1"}},
		modelOf(gotato.ModelFacts{}), ctx, request)
	for name, want := range map[string]string{"X-Team": "from-context", "X-Run": "r1", "X-Fixed": "f", "Session-Id": "ck", "User-Agent": "team-agent/1"} {
		if got := header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// The default User-Agent when none is configured.
	header = server.send(t, ChatCompletions{}, Connection{}, modelOf(gotato.ModelFacts{}), context.Background(), hello())
	if got := header.Get("User-Agent"); got != defaultUserAgent || !strings.HasPrefix(got, "gotato/") {
		t.Errorf("User-Agent = %q", got)
	}
}

// CacheKeyHeader carries the request's cache key on any wire, and nothing is
// sent when there is no key or no header name.
func TestCacheKeyHeaderIsSentOnEveryWire(t *testing.T) {
	for _, api := range wireAPIs {
		server := newHeaderServer(t, api)
		request := hello()
		request.CacheKey = "session-42"
		header := server.send(t, wireOf(api), Connection{APIKey: "k", CacheKeyHeader: "x-session-affinity"}, modelOf(gotato.ModelFacts{}), context.Background(), request)
		if got := header.Get("X-Session-Affinity"); got != "session-42" {
			t.Errorf("%s: header = %q", api, got)
		}
		header = server.send(t, wireOf(api), Connection{APIKey: "k", CacheKeyHeader: "x-session-affinity"}, modelOf(gotato.ModelFacts{}), context.Background(), hello())
		if got := header.Get("X-Session-Affinity"); got != "" {
			t.Errorf("%s: no cache key but header = %q", api, got)
		}
		header = server.send(t, wireOf(api), Connection{APIKey: "k"}, modelOf(gotato.ModelFacts{}), context.Background(), request)
		if got := header.Get("X-Session-Affinity"); got != "" {
			t.Errorf("%s: no header name but header = %q", api, got)
		}
	}
}

// anthropic-version is the wire's Version, else a configured header, else
// 2023-06-01.
func TestAnthropicVersionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wire       AnthropicMessages
		configured string
		want       string
	}{
		{"default", AnthropicMessages{}, "", "2023-06-01"},
		{"configured header", AnthropicMessages{}, "2024-02-02", "2024-02-02"},
		{"wire version", AnthropicMessages{Version: "2025-05-05"}, "", "2025-05-05"},
		{"wire version wins over the header", AnthropicMessages{Version: "2025-05-05"}, "2024-02-02", "2025-05-05"},
	} {
		server := newHeaderServer(t, APIAnthropicMessages)
		conn := Connection{APIKey: "k"}
		if tc.configured != "" {
			conn.Headers = map[string]string{"anthropic-version": tc.configured}
		}
		header := server.send(t, tc.wire, conn, modelOf(gotato.ModelFacts{}), context.Background(), hello())
		if got := header.Get("anthropic-version"); got != tc.want {
			t.Errorf("%s: anthropic-version = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The binding beta is sent iff the model's ThinkingBinding fact says so, merged
// after any beta the Connection or the context names.
func TestThinkingBindingFactSendsTheBetaHeaderMergedWithTheConfiguredOne(t *testing.T) {
	for _, tc := range []struct {
		name       string
		binding    bool
		configured string
		want       string
	}{
		{"no binding, nothing configured", false, "", ""},
		{"no binding, configured kept", false, "files-2025-01-01", "files-2025-01-01"},
		{"binding alone", true, "", anthropicBindingBeta},
		{"binding after the configured", true, "files-2025-01-01", "files-2025-01-01," + anthropicBindingBeta},
		{"binding already configured", true, anthropicBindingBeta, anthropicBindingBeta},
	} {
		server := newHeaderServer(t, APIAnthropicMessages)
		conn := Connection{APIKey: "k"}
		if tc.configured != "" {
			conn.Headers = map[string]string{"anthropic-beta": tc.configured}
		}
		header := server.send(t, AnthropicMessages{}, conn, modelOf(gotato.ModelFacts{ThinkingBinding: tc.binding}), context.Background(), hello())
		if got := header.Get("anthropic-beta"); got != tc.want {
			t.Errorf("%s: anthropic-beta = %q, want %q", tc.name, got, tc.want)
		}
	}
	// A beta from the request's context is merged too.
	server := newHeaderServer(t, APIAnthropicMessages)
	ctx := WithRequestHeaders(context.Background(), map[string]string{"anthropic-beta": "ctx-beta"})
	header := server.send(t, AnthropicMessages{}, Connection{APIKey: "k"}, modelOf(gotato.ModelFacts{ThinkingBinding: true}), ctx, hello())
	if got := header.Get("anthropic-beta"); got != "ctx-beta,"+anthropicBindingBeta {
		t.Errorf("anthropic-beta = %q", got)
	}
}

// RequestHeaders returns a copy: a caller's write does not reach the context.
func TestRequestHeadersReturnsACopy(t *testing.T) {
	ctx := WithRequestHeaders(context.Background(), map[string]string{"x-session": "a"})
	RequestHeaders(ctx)["x-session"] = "mutated"
	if got := RequestHeaders(ctx)["x-session"]; got != "a" {
		t.Fatalf("a caller's write reached the context: x-session = %q", got)
	}
}

// A bad header on the request's context is refused at Stream, not sent.
func TestStreamRefusesABadContextHeader(t *testing.T) {
	server := newHeaderServer(t, APIChatCompletions)
	client := chatClientAt(t, server.URL)
	ctx := WithRequestHeaders(context.Background(), map[string]string{"X-Run": "a\r\nInjected: 1"})
	if _, err := client.Stream(ctx, hello()); err == nil {
		t.Fatal("a header with a newline was accepted")
	}
	if server.header() != nil {
		t.Fatal("the request was sent")
	}
}

// NewHTTPClient puts its timeouts on the transport of the client; a zero
// duration sets no bound.
func TestNewHTTPClientSetsItsTimeoutsOnTheTransport(t *testing.T) {
	transport := func(connect, header time.Duration) *http.Transport {
		t.Helper()
		client := NewHTTPClient(connect, header)
		got, ok := client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("transport = %T", client.Transport)
		}
		return got
	}
	set := transport(3*time.Second, 5*time.Minute)
	if set.ResponseHeaderTimeout != 5*time.Minute || set.TLSHandshakeTimeout != 3*time.Second {
		t.Fatalf("settings ignored: header %v, tls %v", set.ResponseHeaderTimeout, set.TLSHandshakeTimeout)
	}
	if set.DialContext == nil {
		t.Fatal("no dialer carries the connect timeout")
	}
	zero := transport(0, 0)
	if zero.ResponseHeaderTimeout != 0 || zero.TLSHandshakeTimeout != 0 {
		t.Fatalf("zero is no bound: %v, %v", zero.ResponseHeaderTimeout, zero.TLSHandshakeTimeout)
	}
	// The client has no overall timeout: the stream is bounded by the context.
	if NewHTTPClient(time.Second, time.Second).Timeout != 0 {
		t.Fatal("the client has an overall timeout, which would cut every long stream")
	}
}

// With no HTTPClient the Client uses NewHTTPClient(10s, 60s); one that is
// passed is used as it is.
func TestTheDefaultHTTPClientHasTheOldTimeoutsAndAGivenOneIsKept(t *testing.T) {
	client, err := New(ChatCompletions{}, Connection{Endpoint: "http://127.0.0.1:1/chat"}, modelOf(gotato.ModelFacts{}))
	if err != nil {
		t.Fatal(err)
	}
	transport := client.httpClient.Transport.(*http.Transport)
	if transport.ResponseHeaderTimeout != 60*time.Second || transport.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("defaults changed: header %v, tls %v", transport.ResponseHeaderTimeout, transport.TLSHandshakeTimeout)
	}
	mine := &http.Client{Timeout: time.Hour}
	client, err = New(ChatCompletions{}, Connection{Endpoint: "http://127.0.0.1:1/chat", HTTPClient: mine}, modelOf(gotato.ModelFacts{}))
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient != mine {
		t.Fatal("the HTTP client passed was replaced")
	}
}

// A RoundTripper on the passed client is where any other authentication goes:
// it sees the request the wire built, and may sign it.
func TestAnotherAuthenticationGoesInTheRoundTripper(t *testing.T) {
	server := newHeaderServer(t, APIChatCompletions)
	signing := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("X-Signature", "signed:"+r.Method)
		return http.DefaultTransport.RoundTrip(r)
	})}
	client, err := New(ChatCompletions{}, Connection{Endpoint: server.URL, HTTPClient: signing}, modelOf(gotato.ModelFacts{}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	drain(t, stream)
	if got := server.header().Get("X-Signature"); got != "signed:POST" {
		t.Fatalf("X-Signature = %q", got)
	}
	if got := server.header().Get("Authorization"); got != "" {
		t.Fatalf("a key was sent when none was set: %q", got)
	}
}
