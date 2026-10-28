package gateway

import (
	"os"
	"strings"
	"testing"
	"time"
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
