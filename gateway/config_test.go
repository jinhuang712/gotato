package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// TestExampleConfigLoads keeps gateway.example.yaml, the file a reader copies,
// loadable: it fails when the file names a key the loader does not know.
func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("GOTATO_GATEWAY_API_KEY", "placeholder")
	config, err := LoadYAML("gateway.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if config.APIKey != "placeholder" || config.Model == "" || config.ContextWindowTokens <= 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestParseYAMLExpandsEnvironmentAndDuration(t *testing.T) {
	t.Setenv("TEST_GOTATO_GATEWAY_KEY", "secret-from-env")
	config, err := parseYAML([]byte(`
base_url: https://gateway.example.com/v1
api_key: ${TEST_GOTATO_GATEWAY_KEY}
model: model-a
max_retries: 3
retry_backoff: 125ms
headers:
  X-Environment: test
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.BaseURL != "https://gateway.example.com/v1" || config.APIKey != "secret-from-env" || config.Model != "model-a" {
		t.Fatalf("config = %+v", config)
	}
	if config.MaxRetries != 3 || config.RetryBackoff != 125*time.Millisecond || config.Headers["X-Environment"] != "test" {
		t.Fatalf("config options = %+v", config)
	}
}

func TestParseYAMLResponsesConfig(t *testing.T) {
	config, err := parseYAML([]byte(`
api: openai-responses
model: gpt-test
api_key: secret
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.API != "openai-responses" || config.Model != "gpt-test" {
		t.Fatalf("config = %+v", config)
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if client.endpoint != "https://api.openai.com/v1/responses" {
		t.Fatalf("endpoint = %s", client.endpoint)
	}
	if _, err := parseYAML([]byte("api: openai-responses\nmodel: m\nauth:\n  type: api_key\n")); err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("an auth key must be rejected by name, err = %v", err)
	}
}

// A spelling that is not a wire protocol is refused, and the error names the
// supported values.
func TestAnAPIAliasIsRefused(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-codex-responses", "anthropic"} {
		_, err := New(Config{API: api, Model: "m", BaseURL: "https://gw.example.com"})
		if err == nil {
			t.Fatalf("api %q accepted", api)
		}
		for _, want := range []API{APIChatCompletions, APIResponses, APIAnthropicMessages} {
			if !strings.Contains(err.Error(), string(want)) {
				t.Errorf("api %q: error %q does not name %q", api, err, want)
			}
		}
	}
}

func TestMinusOneMaxRetriesDisablesRetries(t *testing.T) {
	client, err := New(Config{Model: "m", BaseURL: "https://gw.example.com/v1", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if client.maxRetries != 0 {
		t.Fatalf("maxRetries = %d, want 0", client.maxRetries)
	}
	config, err := parseYAML([]byte("model: m\nbase_url: https://gw.example.com/v1\nmax_retries: -1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxRetries != -1 {
		t.Fatalf("max_retries: -1 not parsed: %+v", config)
	}
	if _, err := New(Config{Model: "m", BaseURL: "https://x", MaxRetries: -2}); err == nil {
		t.Fatal("a MaxRetries below -1 must be rejected")
	}
}

func TestParseYAMLKeepsLiteralDollarAndBareNames(t *testing.T) {
	t.Setenv("TEST_GOTATO_BARE", "expanded")
	config, err := parseYAML([]byte(`
base_url: https://gw.example.com/v1
api_key: $TEST_GOTATO_BARE-literal$dollar
model: m
headers:
  X-Key: tok$abc-${TEST_GOTATO_BARE}
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.APIKey != "$TEST_GOTATO_BARE-literal$dollar" {
		t.Fatalf("api_key = %q", config.APIKey)
	}
	if got := config.Headers["X-Key"]; got != "tok$abc-expanded" {
		t.Fatalf("header = %q", got)
	}
}

func TestParseYAMLEnvValueCannotInjectStructure(t *testing.T) {
	t.Setenv("TEST_GOTATO_YAML", "m\nbase_url: https://evil.example.com/v1")
	config, err := parseYAML([]byte("model: ${TEST_GOTATO_YAML}\nbase_url: https://gw.example.com/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Model != "m\nbase_url: https://evil.example.com/v1" {
		t.Fatalf("model = %q", config.Model)
	}
	if config.BaseURL != "https://gw.example.com/v1" {
		t.Fatalf("base_url = %q", config.BaseURL)
	}
}

// TestParseYAMLRejectsWrongScalarTypes pins that decoding after the
// env-expansion round trip still enforces the declared types.
func TestParseYAMLRejectsWrongScalarTypes(t *testing.T) {
	if _, err := parseYAML([]byte("model: m\nbase_url: https://gw.example.com/v1\nheaders:\n  X-Key: [a, b]\n")); err == nil {
		t.Fatal("sequence header value must be rejected")
	}
	if _, err := parseYAML([]byte("model: m\nbase_url: https://gw.example.com/v1\nheaders:\n  X-Key: {a: 1}\n")); err == nil {
		t.Fatal("mapping header value must be rejected")
	}
}

func TestLoadYAML(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "gateway-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("endpoint: http://127.0.0.1:9999/v1/chat/completions\nmodel: local\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	config, err := LoadYAML(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if config.Endpoint == "" || config.Model != "local" {
		t.Fatalf("loaded config = %+v", config)
	}
}

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
