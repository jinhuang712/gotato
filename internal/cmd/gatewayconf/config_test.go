package gatewayconf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/gotato"
)

// base is the smallest valid file; tests append keys to it.
const base = "endpoint: https://gw.example.com/v1/chat/completions\nmodel: m\nwindow: 200000\n"

// TestExampleConfigLoads keeps gateway.example.yaml, the file a reader copies,
// loadable: it fails when the file names a key the loader does not know.
func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("GOTATO_GATEWAY_API_KEY", "placeholder")
	config, err := Load("gateway.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if config.Connection.APIKey != "placeholder" || config.Model.Name == "" || config.Model.Facts.Window.Tokens() <= 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestParseExpandsEnvironmentAndDuration(t *testing.T) {
	t.Setenv("TEST_GOTATO_GATEWAY_KEY", "secret-from-env")
	config, err := Parse([]byte(base + `
api_key: ${TEST_GOTATO_GATEWAY_KEY}
cache_retention: 30m
headers:
  X-Environment: test
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Connection.Endpoint != "https://gw.example.com/v1/chat/completions" || config.Connection.APIKey != "secret-from-env" || config.Model.Name != "m" {
		t.Fatalf("config = %+v", config)
	}
	if config.Model.Facts.CacheRetention != 30*time.Minute || config.Connection.Headers["X-Environment"] != "test" {
		t.Fatalf("config options = %+v", config)
	}
}

func TestParseReadsTheModelFactsAndSampling(t *testing.T) {
	config, err := Parse([]byte(base + `
round_reasoning: true
thinking_binding: true
temperature: 0.25
max_tokens: 4096
reasoning_effort: high
reasoning_summary: auto
`))
	if err != nil {
		t.Fatal(err)
	}
	facts, sampling := config.Model.Facts, config.Model.Sampling
	if !facts.RoundReasoning || !facts.ThinkingBinding || facts.Window.Tokens() != 200000 {
		t.Fatalf("facts = %+v", facts)
	}
	if sampling.Temperature == nil || *sampling.Temperature != 0.25 || sampling.MaxTokens != 4096 || sampling.ReasoningEffort != "high" || sampling.ReasoningSummary != "auto" {
		t.Fatalf("sampling = %+v", sampling)
	}
}

func TestParseRejectsAnUnknownKeyByName(t *testing.T) {
	_, err := Parse([]byte(base + "auth:\n  type: api_key\n"))
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("an auth key must be rejected by name, err = %v", err)
	}
	for _, old := range []string{"base_url: https://x\n", "provider: openai\n", "no_drop_block: true\n", "context_window_tokens: 5\n"} {
		if _, err := Parse([]byte(base + old)); err == nil || !strings.Contains(err.Error(), strings.Split(old, ":")[0]) {
			t.Errorf("%q: err = %v", old, err)
		}
	}
}

func TestParseKeepsLiteralDollarAndBareNames(t *testing.T) {
	t.Setenv("TEST_GOTATO_BARE", "expanded")
	config, err := Parse([]byte(base + `
api_key: $TEST_GOTATO_BARE-literal$dollar
headers:
  X-Key: tok$abc-${TEST_GOTATO_BARE}
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Connection.APIKey != "$TEST_GOTATO_BARE-literal$dollar" {
		t.Fatalf("api_key = %q", config.Connection.APIKey)
	}
	if got := config.Connection.Headers["X-Key"]; got != "tok$abc-expanded" {
		t.Fatalf("header = %q", got)
	}
}

func TestParseEnvValueCannotInjectStructure(t *testing.T) {
	t.Setenv("TEST_GOTATO_YAML", "m\nendpoint: https://evil.example.com/v1")
	config, err := Parse([]byte("model: ${TEST_GOTATO_YAML}\nwindow: 1000\nendpoint: https://gw.example.com/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Model.Name != "m\nendpoint: https://evil.example.com/v1" {
		t.Fatalf("model = %q", config.Model.Name)
	}
	if config.Connection.Endpoint != "https://gw.example.com/v1" {
		t.Fatalf("endpoint = %q", config.Connection.Endpoint)
	}
}

// TestParseRejectsWrongScalarTypes pins that decoding after the env-expansion
// round trip still enforces the declared types.
func TestParseRejectsWrongScalarTypes(t *testing.T) {
	for _, extra := range []string{
		"headers:\n  X-Key: [a, b]\n",
		"headers:\n  X-Key: {a: 1}\n",
		"max_tokens: lots\n",
		"round_reasoning: maybe\n",
		"max_retries: many\n",
	} {
		if _, err := Parse([]byte(base + extra)); err == nil {
			t.Errorf("%q was accepted", extra)
		}
	}
}

func TestLoadReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte("endpoint: http://127.0.0.1:9999/v1/chat/completions\nmodel: local\nwindow: unknown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Connection.Endpoint == "" || config.Model.Name != "local" {
		t.Fatalf("loaded config = %+v", config)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("a missing file must be an error")
	}
}

func TestDurationsAreParsedAndRefusedWhenWrong(t *testing.T) {
	good, err := Parse([]byte(base + "connect_timeout: 4s\nresponse_header_timeout: 3m\nmax_retry_wait: 20s\nretry_backoff: 125ms\n"))
	if err != nil {
		t.Fatal(err)
	}
	if good.Connection.HTTPClient == nil {
		t.Fatal("the timeouts must make an HTTP client")
	}
	transport := good.Connection.HTTPClient.Transport.(*http.Transport)
	if transport.ResponseHeaderTimeout != 3*time.Minute || transport.TLSHandshakeTimeout != 4*time.Second {
		t.Fatalf("timeouts: header %v, tls %v", transport.ResponseHeaderTimeout, transport.TLSHandshakeTimeout)
	}
	for _, extra := range []string{"cache_retention: soon\n", "retry_backoff: x\n", "max_retry_wait: 1\n", "connect_timeout: -1s\n"} {
		if _, err := Parse([]byte(base + extra)); err == nil {
			t.Errorf("%q was accepted", extra)
		}
	}
}

func TestWindowForms(t *testing.T) {
	endpoint := "endpoint: https://gw.example.com/v1/chat/completions\nmodel: m\n"
	config, err := Parse([]byte(endpoint + "window: 200000\n"))
	if err != nil || config.Model.Facts.Window != gotato.WindowTokens(200000) {
		t.Fatalf("number: %+v, %v", config.Model.Facts.Window, err)
	}
	config, err = Parse([]byte(endpoint + "window: unknown\n"))
	if err != nil || config.Model.Facts.Window != gotato.WindowUnknown() {
		t.Fatalf("unknown: %+v, %v", config.Model.Facts.Window, err)
	}
	if _, err := Parse([]byte(endpoint)); err == nil || !strings.Contains(err.Error(), "Window") {
		t.Fatalf("a missing window must be refused naming Window, err = %v", err)
	}
	for _, window := range []string{"0", "-5", "lots", "1.5"} {
		if _, err := Parse([]byte(endpoint + "window: " + window + "\n")); err == nil {
			t.Errorf("window %q was accepted", window)
		}
	}
}

func TestWireNamesMapToWires(t *testing.T) {
	for _, tc := range []struct {
		extra string
		want  gateway.WireAPI
	}{
		{"", gateway.ChatCompletions{}},
		{"api: openai-chat-completions\ncache_key_field: prompt_cache_key\n", gateway.ChatCompletions{CacheKeyField: "prompt_cache_key"}},
		{"api: openai-chat-completions\ndeferred_tools: list\n", gateway.ChatCompletions{}},
		{"api: openai-responses\ndeferred_tools: list\n", gateway.Responses{Deferred: gateway.DeferredFormList}},
		{"api: openai-responses\ncache_key_field: prompt_cache_key\n", gateway.Responses{CacheKeyField: "prompt_cache_key"}},
		{"api: anthropic-messages\n", gateway.AnthropicMessages{}},
		{"api: anthropic-messages\nanthropic_version: 2023-06-01\ndeferred_tools: native\n", gateway.AnthropicMessages{Version: "2023-06-01", Deferred: gateway.DeferredFormNative}},
	} {
		config, err := Parse([]byte(base + tc.extra))
		if err != nil {
			t.Errorf("%q: %v", tc.extra, err)
			continue
		}
		if config.Wire != tc.want {
			t.Errorf("%q: wire = %#v, want %#v", tc.extra, config.Wire, tc.want)
		}
	}
}

// A spelling that is not a wire protocol is refused, and the error names the
// supported values.
func TestAnAPIAliasIsRefused(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-codex-responses", "anthropic"} {
		_, err := Parse([]byte(base + "api: " + api + "\n"))
		if err == nil {
			t.Fatalf("api %q accepted", api)
		}
		for _, want := range []string{APIChatCompletions, APIResponses, APIAnthropicMessages} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("api %q: error %q does not name %q", api, err, want)
			}
		}
	}
}

func TestWireOptionsAreRefusedOnTheWiresThatLackThem(t *testing.T) {
	for _, extra := range []string{
		"api: openai-chat-completions\nanthropic_version: 2023-06-01\n",
		"api: openai-responses\nanthropic_version: 2023-06-01\n",
		"api: anthropic-messages\ncache_key_field: prompt_cache_key\n",
		"api: openai-chat-completions\ndeferred_tools: native\n",
	} {
		if _, err := Parse([]byte(base + extra)); err == nil {
			t.Errorf("%q was accepted", extra)
		}
	}
}

func TestMissingEndpointIsRefused(t *testing.T) {
	if _, err := Parse([]byte("model: m\nwindow: 1000\n")); err == nil {
		t.Fatal("a file with no endpoint was accepted")
	}
}

// reply serves one failure, then a good chat stream, and counts the attempts.
func reply(t *testing.T, failure func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			failure(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func clientFor(t *testing.T, server *httptest.Server, extra string) *gateway.Client {
	t.Helper()
	config, err := Parse([]byte("endpoint: " + server.URL + "/v1/chat/completions\nmodel: m\nwindow: 200000\n" + extra))
	if err != nil {
		t.Fatal(err)
	}
	client, err := config.New()
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func streamOnce(t *testing.T, client *gateway.Client) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Stream(ctx, gotato.ModelRequest{})
	if err == nil {
		_ = stream.Close()
	}
	return err
}

func tooManyRequests(w http.ResponseWriter) { http.Error(w, "slow down", http.StatusTooManyRequests) }

func TestRetryKeysMapToARetry(t *testing.T) {
	// No retry key: the default retries.
	server, calls := reply(t, tooManyRequests)
	if err := streamOnce(t, clientFor(t, server, "")); err != nil || calls.Load() != 2 {
		t.Fatalf("default retry: err = %v, attempts = %d", err, calls.Load())
	}
	// max_retries: 0 is off.
	server, calls = reply(t, tooManyRequests)
	err := streamOnce(t, clientFor(t, server, "max_retries: 0\n"))
	var gerr *gateway.Error
	if !errors.As(err, &gerr) || gerr.StatusCode != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Fatalf("retries off: err = %v, attempts = %d", err, calls.Load())
	}
	// max_retry_wait caps a long Retry-After.
	server, calls = reply(t, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3600")
		tooManyRequests(w)
	})
	start := time.Now()
	if err := streamOnce(t, clientFor(t, server, "max_retries: 1\nretry_backoff: 1ms\nmax_retry_wait: 30ms\n")); err != nil || calls.Load() != 2 {
		t.Fatalf("capped wait: err = %v, attempts = %d", err, calls.Load())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Retry-After: 3600 was waited out: %v", elapsed)
	}
	// -1 is refused.
	if _, err := Parse([]byte(base + "max_retries: -1\n")); err == nil {
		t.Fatal("max_retries: -1 was accepted")
	}
}

func TestOverflowPhrasesWrapTheDefaultClassifier(t *testing.T) {
	refuse := func(message string) func(w http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"` + message + `"}}`))
		}
	}
	extra := "max_retries: 0\noverflow_phrases:\n  - Too Many Words\n"
	for _, tc := range []struct {
		name, message string
		overflow      bool
	}{
		{"the team's phrase", "this has too many words in it", true},
		{"a default phrase", "maximum context length is 8192 tokens", true},
		{"an ordinary refusal", "messages must not be empty", false},
	} {
		server, _ := reply(t, refuse(tc.message))
		err := streamOnce(t, clientFor(t, server, extra))
		var gerr *gateway.Error
		if !errors.As(err, &gerr) || gerr.StatusCode != http.StatusBadRequest || gerr.Overflow != tc.overflow {
			t.Errorf("%s: err = %#v, want overflow %v", tc.name, err, tc.overflow)
		}
	}
}
