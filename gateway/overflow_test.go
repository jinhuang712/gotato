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

// refusingServer answers every request with status and body, and counts them.
func refusingServer(status int, body string, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestRefusedRequestIsContextOverflowOnEveryWireAPI(t *testing.T) {
	cases := []struct {
		name   string
		api    API
		status int
		body   string
		want   bool
	}{
		{"chat 400", APIChatCompletions, 400, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","code":"context_length_exceeded"}}`, true},
		{"chat 413", APIChatCompletions, 413, `{"error":{"message":"Prompt is too long"}}`, true},
		{"responses 400", APIResponses, 400, `{"error":{"message":"Your input exceeds the context window of this model."}}`, true},
		{"anthropic 400", APIAnthropicMessages, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, true},
		{"anthropic input plus max_tokens", APIAnthropicMessages, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"input length and max_tokens exceed context limit: 190000 + 21333 > 200000"}}`, true},
		{"chat code only", APIChatCompletions, 400, `{"error":{"message":"Bad request","code":"context_length_exceeded"}}`, true},
		{"responses code only", APIResponses, 400, `{"error":{"message":"Invalid request","type":"invalid_request_error","code":"context_length_exceeded"}}`, true},
		{"type only", APIChatCompletions, 400, `{"error":{"message":"Bad request","type":"context_length_exceeded"}}`, true},
		{"numeric and null codes", APIChatCompletions, 400, `{"error":{"message":"Bad request","code":null,"type":"invalid_request_error"}}`, false},
		{"max_tokens alone is too large", APIAnthropicMessages, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 99999 > 8192, which is the maximum allowed number of output tokens for this model"}}`, false},
		{"output cap, chat", APIChatCompletions, 400, `{"error":{"message":"max_tokens is too large: 99999. This model supports at most 8192 completion tokens.","code":"invalid_value","type":"invalid_request_error"}}`, false},
		{"other 400", APIChatCompletions, 400, `{"error":{"message":"messages must not be empty"}}`, false},
		{"phrase on another status", APIChatCompletions, 500, `{"error":{"message":"maximum context length"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := refusingServer(tc.status, tc.body, &calls)
			defer server.Close()
			client, err := New(Config{API: tc.api, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
			var gatewayErr *Error
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("err = %v", err)
			}
			if gatewayErr.Overflow != tc.want || gatewayErr.ContextOverflow() != tc.want {
				t.Fatalf("Overflow = %v, want %v (%+v)", gatewayErr.Overflow, tc.want, gatewayErr)
			}
			if tc.want && gatewayErr.Retryable() {
				t.Fatalf("an overflow must not be retried: %+v", gatewayErr)
			}
			if calls.Load() != 1 {
				t.Fatalf("HTTP attempts = %d, want 1", calls.Load())
			}
		})
	}
}

func TestOverflowPhrasesAddTheTeamsOwnWording(t *testing.T) {
	body := `{"error":{"message":"The request is bigger than we take","code":"Request_Too_Big","type":"invalid_request_error"}}`
	for name, phrases := range map[string][]string{"none": nil, "message": {"bigger than we take"}, "code": {" REQUEST_too_big "}} {
		var calls atomic.Int32
		server := refusingServer(400, body, &calls)
		client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1, OverflowPhrases: append(phrases, "")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
		server.Close()
		var gatewayErr *Error
		if !errors.As(err, &gatewayErr) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if want := name != "none"; gatewayErr.Overflow != want {
			t.Errorf("%s: Overflow = %v, want %v", name, gatewayErr.Overflow, want)
		}
	}
	// The defaults still apply next to a team's own phrases.
	var calls atomic.Int32
	server := refusingServer(400, `{"error":{"message":"prompt is too long"}}`, &calls)
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1, OverflowPhrases: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !gatewayErr.Overflow {
		t.Fatalf("err = %v", err)
	}
}

func TestOverflowPhrasesFromYAMLReachStreamErrors(t *testing.T) {
	config, err := parseYAML([]byte("api: openai-responses\nbase_url: https://gw.example.com\nmodel: m\noverflow_phrases:\n  - too_much_context\n"))
	if err != nil || len(config.OverflowPhrases) != 1 {
		t.Fatalf("config = %+v, err %v", config, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"code\":\"too_much_context\",\"message\":\"nope\"}\n\n"))
	}))
	defer server.Close()
	config.BaseURL, config.APIKey, config.MaxRetries = server.URL, "k", -1
	client, err := New(config)
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
	if !errors.As(err, &gatewayErr) || !gatewayErr.ContextOverflow() {
		t.Fatalf("err = %v", err)
	}
}

func TestOverflowErrorFlowsThroughAWrappedError(t *testing.T) {
	var err error = &Error{StatusCode: 400, Message: "prompt is too long", Overflow: true}
	var overflow interface{ ContextOverflow() bool }
	if !errors.As(err, &overflow) || !overflow.ContextOverflow() {
		t.Fatal("the Error is not found by its method")
	}
	var nilErr *Error
	if nilErr.ContextOverflow() {
		t.Fatal("a nil Error is an overflow")
	}
}

// A rate limit that is still refused after the gateway's own retries reaches
// the Run as a retryable model_failure.
func TestRateLimitReachesTheRunAsRetryable(t *testing.T) {
	var calls atomic.Int32
	server := refusingServer(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, &calls)
	defer server.Close()
	client, err := New(Config{API: APIChatCompletions, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	session, err := gotato.NewSession(gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	_, err = session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if !gotato.IsCode(err, gotato.CodeModelFailure) || !gotato.Retryable(err) || gotato.NotRun(err) {
		t.Fatalf("err = %v, want a retryable model_failure that ran", err)
	}
}

func TestResponsesStreamErrorNamesOverflow(t *testing.T) {
	for _, event := range []string{
		`{"type":"error","code":"context_length_exceeded","message":"Input is too large"}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"too much"}}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
		}))
		client, err := New(Config{API: APIResponses, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = collectEvents(t, stream)
		_ = stream.Close()
		server.Close()
		var gatewayErr *Error
		if !errors.As(err, &gatewayErr) || !gatewayErr.ContextOverflow() {
			t.Fatalf("%s: err = %v", event, err)
		}
	}
}

func TestAnthropicStreamErrorNamesOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)
	}))
	defer server.Close()
	client, err := New(Config{API: APIAnthropicMessages, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1})
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
	if !errors.As(err, &gatewayErr) || !gatewayErr.ContextOverflow() {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicContextWindowExceededIsAnOverflowNotAContinuation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, data := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			writeSSE(t, w, data)
		}
	}))
	defer server.Close()
	client, err := New(Config{API: APIAnthropicMessages, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	limits := gotato.DefaultLimits()
	limits.MaxTruncatedContinuations = 3
	session, err := gotato.NewSession(gotato.WithModel(client), gotato.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	result, err := session.Prompt(context.Background(), gotato.UserMessage("hi"))
	if err == nil || result.Status != gotato.RunStatusFailed || result.Error == nil || result.Error.Code != gotato.CodeContextOverflow {
		t.Fatalf("result = %+v, err = %v; want a failed Run with context_overflow", result, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want 1: an overflow must not trigger an output-limit continuation", calls.Load())
	}
}

func TestContextWindowFromConfigAndYAML(t *testing.T) {
	client, err := New(Config{BaseURL: "https://gw.example.com", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if client.ContextWindowTokens() != 0 {
		t.Fatalf("unset window = %d", client.ContextWindowTokens())
	}
	client, err = New(Config{BaseURL: "https://gw.example.com", Model: "m", ContextWindowTokens: 200000})
	if err != nil || client.ContextWindowTokens() != 200000 {
		t.Fatalf("window = %v, err %v", client, err)
	}
	if _, err := New(Config{BaseURL: "https://gw.example.com", Model: "m", ContextWindowTokens: -1}); err == nil {
		t.Fatal("a negative window was accepted")
	}
	config, err := parseYAML([]byte("base_url: https://gw.example.com\nmodel: m\ncontext_window_tokens: 128000\n"))
	if err != nil || config.ContextWindowTokens != 128000 {
		t.Fatalf("config = %+v, err %v", config, err)
	}
}

// The key was renamed from context_window: the loader is strict, so an old
// file fails and names the key instead of silently losing the window.
func TestOldContextWindowKeyIsRefused(t *testing.T) {
	_, err := parseYAML([]byte("base_url: https://gw.example.com\nmodel: m\ncontext_window: 128000\n"))
	if err == nil || !strings.Contains(err.Error(), "context_window") {
		t.Fatalf("err = %v, want a refusal naming context_window", err)
	}
}

func TestCacheRetentionIsWhatTheClientAsksFor(t *testing.T) {
	cases := []struct {
		name   string
		config Config
		want   time.Duration
	}{
		{"claude, longest", Config{API: APIAnthropicMessages, Provider: ProviderAnthropic, BaseURL: "https://gw.example.com", Model: "m"}, time.Hour},
		{"claude, one hour asked", Config{API: APIAnthropicMessages, Provider: ProviderAnthropic, BaseURL: "https://gw.example.com", Model: "m", CacheRetention: time.Hour}, time.Hour},
		{"claude, shorter keeps the default", Config{API: APIAnthropicMessages, Provider: ProviderAnthropic, BaseURL: "https://gw.example.com", Model: "m", CacheRetention: 10 * time.Minute}, 5 * time.Minute},
		{"claude model behind an unrecognized address", Config{API: APIAnthropicMessages, Provider: ProviderOther, BaseURL: "https://gw.example.com", Model: "m"}, 0},
		{"responses with a ttl", Config{API: APIResponses, Provider: ProviderOpenAI, BaseURL: "https://gw.example.com", Model: "gpt-5.6"}, 30 * time.Minute},
		{"responses without one", Config{API: APIResponses, Provider: ProviderOpenAI, BaseURL: "https://gw.example.com", Model: "gpt-5.6", CacheRetention: 5 * time.Minute}, 0},
		{"chat completions", Config{BaseURL: "https://gw.example.com", Model: "m"}, 0},
	}
	for _, tc := range cases {
		client, err := New(tc.config)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := client.CacheRetention(); got != tc.want {
			t.Errorf("%s: CacheRetention = %s, want %s", tc.name, got, tc.want)
		}
	}
}
