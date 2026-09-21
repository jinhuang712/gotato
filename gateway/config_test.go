package gateway

import (
	"os"
	"testing"
	"time"
)

func TestParseYAMLExpandsEnvironmentAndDuration(t *testing.T) {
	t.Setenv("TEST_GOTATO_GATEWAY_KEY", "secret-from-env")
	config, err := ParseYAML([]byte(`
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
	config, err := ParseYAML([]byte(`
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
	if _, err := ParseYAML([]byte("api: openai-responses\nmodel: m\nauth:\n  type: pi_oauth\n")); err == nil {
		t.Fatal("pi_oauth must be rejected")
	}
	if _, err := New(Config{API: "openai-codex-responses", Model: "m", BaseURL: "https://gw.example.com"}); err != nil {
		t.Fatalf("legacy alias rejected: %v", err)
	}
}

func TestNoRetriesDisablesRetries(t *testing.T) {
	client, err := New(Config{Model: "m", BaseURL: "https://gw.example.com/v1", NoRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	if client.maxRetries != 0 {
		t.Fatalf("maxRetries = %d, want 0", client.maxRetries)
	}
	config, err := ParseYAML([]byte("model: m\nbase_url: https://gw.example.com/v1\nno_retries: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !config.NoRetries {
		t.Fatalf("no_retries not parsed: %+v", config)
	}
	if _, err := New(Config{Model: "m", BaseURL: "https://x", NoRetries: true, MaxRetries: 2}); err == nil {
		t.Fatal("NoRetries with MaxRetries must be rejected")
	}
}

func TestParseYAMLKeepsLiteralDollarAndBareNames(t *testing.T) {
	t.Setenv("TEST_GOTATO_BARE", "expanded")
	config, err := ParseYAML([]byte(`
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
	config, err := ParseYAML([]byte("model: ${TEST_GOTATO_YAML}\nbase_url: https://gw.example.com/v1\n"))
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
	if _, err := ParseYAML([]byte("model: m\nbase_url: https://gw.example.com/v1\nheaders:\n  X-Key: [a, b]\n")); err == nil {
		t.Fatal("sequence header value must be rejected")
	}
	if _, err := ParseYAML([]byte("model: m\nbase_url: https://gw.example.com/v1\nheaders:\n  X-Key: {a: 1}\n")); err == nil {
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
