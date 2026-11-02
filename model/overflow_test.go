package model

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
			client := newClientFor(t, wireOf(tc.api), server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}))
			_, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
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

// phraseClassifier is DefaultClassifier plus a team's own wording for an
// overflow, in a message or in a code, as a team wraps it with WithClassifier.
func phraseClassifier(phrases ...string) Classifier {
	return func(f Failure) Verdict {
		v := DefaultClassifier(f)
		for _, phrase := range phrases {
			phrase = strings.ToLower(strings.TrimSpace(phrase))
			if phrase == "" {
				continue
			}
			if strings.Contains(strings.ToLower(f.Message), phrase) || strings.EqualFold(strings.TrimSpace(f.Code), phrase) || strings.EqualFold(strings.TrimSpace(f.Type), phrase) {
				v.Overflow, v.Retryable = true, false
			}
		}
		return v
	}
}

func TestAWrappedClassifierAddsTheTeamsOwnOverflowWording(t *testing.T) {
	body := `{"error":{"message":"The request is bigger than we take","code":"Request_Too_Big","type":"invalid_request_error"}}`
	for name, phrases := range map[string][]string{"none": nil, "message": {"bigger than we take"}, "code": {" REQUEST_too_big "}} {
		var calls atomic.Int32
		server := refusingServer(400, body, &calls)
		client := newClientFor(t, ChatCompletions{}, server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}), WithClassifier(phraseClassifier(phrases...)))
		_, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
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
	client := newClientFor(t, ChatCompletions{}, server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}), WithClassifier(phraseClassifier("x")))
	_, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !gatewayErr.Overflow {
		t.Fatalf("err = %v", err)
	}
}

func TestAWrappedClassifierReachesStreamErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"code\":\"too_much_context\",\"message\":\"nope\"}\n\n"))
	}))
	defer server.Close()
	client := newClientFor(t, Responses{}, server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}), WithClassifier(phraseClassifier("too_much_context")))
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

// A rate limit that is still refused after the Client's own retries reaches
// the Run as a retryable model_failure.
func TestRateLimitReachesTheRunAsRetryable(t *testing.T) {
	var calls atomic.Int32
	server := refusingServer(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, &calls)
	defer server.Close()
	client := newClientFor(t, wireOf(APIChatCompletions), server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}))
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(client))
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
		client := newClientFor(t, wireOf(APIResponses), server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}))
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
	client := newClientFor(t, wireOf(APIAnthropicMessages), server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}))
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
	client := newClientFor(t, wireOf(APIAnthropicMessages), server.URL, modelOf(gotato.ModelFacts{}), WithRetry(Retry{}))
	limits := gotato.DefaultLimits()
	limits.MaxTruncatedContinuations = 3
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(client), gotato.WithLimits(limits))
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

// The window must be stated: a number, or "unknown" written out.
func TestNewRequiresAStatedContextWindow(t *testing.T) {
	conn := Connection{Endpoint: "https://gw.example.com/v1/chat/completions"}
	if _, err := New(ChatCompletions{}, conn, gotato.ModelConfig{Name: "m"}); err == nil {
		t.Fatal("a window left out was accepted")
	}
	client, err := New(ChatCompletions{}, conn, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown()}})
	if err != nil || client.Config().Facts.Window.Tokens() != 0 || !client.Config().Facts.Window.Stated() {
		t.Fatalf("unknown window: %v, err %v", client, err)
	}
	client, err = New(ChatCompletions{}, conn, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(200000)}})
	if err != nil || client.Config().Facts.Window.Tokens() != 200000 {
		t.Fatalf("window = %v, err %v", client, err)
	}
	if _, err := New(ChatCompletions{}, conn, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(-1)}}); err == nil {
		t.Fatal("a negative window was accepted")
	}
}

// The cache retention is a fact the team states. The Anthropic wire asks for
// the one-hour cache only at a retention of an hour or more, and Responses
// asks for prompt_cache_options only at exactly the 30 minutes it takes.
func TestCacheRetentionIsWhatTheWireAsksFor(t *testing.T) {
	cases := []struct {
		name      string
		api       API
		retention time.Duration
		want      string // a fragment of the encoded request, or "" for none
	}{
		{"anthropic, an hour", APIAnthropicMessages, time.Hour, `"ttl":"1h"`},
		{"anthropic, longer than an hour", APIAnthropicMessages, 2 * time.Hour, `"ttl":"1h"`},
		{"anthropic, shorter keeps the default", APIAnthropicMessages, 10 * time.Minute, ""},
		{"anthropic, none stated", APIAnthropicMessages, 0, ""},
		{"responses, 30 minutes", APIResponses, 30 * time.Minute, `"prompt_cache_options":{"ttl":"30m"}`},
		{"responses, another retention", APIResponses, 5 * time.Minute, ""},
		{"chat completions", APIChatCompletions, time.Hour, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := encodeWith(t, tc.api, gotato.ModelFacts{CacheRetention: tc.retention}, []gotato.Message{gotato.UserMessage("hi")})
			marker := `"ttl"`
			if tc.want != "" {
				marker = tc.want
			}
			if has := strings.Contains(body, marker); has != (tc.want != "") {
				t.Fatalf("contains %s = %v, want %v:\n%s", marker, has, tc.want != "", body)
			}
		})
	}
}

// A refusal as too long that names the request's limit in tokens says it by
// ContextLimit, in each provider's wording; one that does not says 0.
func TestARefusalNamesItsLimitWhenItsWordingDoes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    int
	}{
		{"chat", "This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.", 8192},
		{"chat with separators", "This model's maximum context length is 128,000 tokens.", 128000},
		{"anthropic", "prompt is too long: 210000 tokens > 200000 maximum", 200000},
		{"anthropic with max_tokens", "input length and max_tokens exceed context limit: 190000 + 21333 > 200000", 200000},
		{"responses", "Your input exceeds the context window of 400000 tokens.", 400000},
		{"no number", "Prompt is too long", 0},
		{"a number that is not a limit", "prompt is too long for 3 items", 0},
	} {
		verdict := DefaultClassifier(Failure{Status: 400, Message: tc.message, Type: "context_length_exceeded"})
		err := newError(Failure{Status: 400, Message: tc.message}, verdict)
		if got := err.ContextLimit(); got != tc.want {
			t.Errorf("%s: ContextLimit = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := newError(Failure{Status: 500, Message: "maximum context length is 8192 tokens"}, DefaultClassifier(Failure{Status: 500, Message: "maximum context length is 8192 tokens"})).ContextLimit(); got != 0 {
		t.Errorf("an error that is no overflow names the limit %d", got)
	}
	var none *Error
	if none.ContextLimit() != 0 {
		t.Error("a nil error names a limit")
	}
}
