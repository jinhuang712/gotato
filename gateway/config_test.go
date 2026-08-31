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

func TestParseYAMLCodexConfig(t *testing.T) {
	t.Setenv("TEST_GOTATO_PI_HOME", "/tmp/pi")
	config, err := ParseYAML([]byte(`
api: openai-codex-responses
model: gpt-test
auth:
  type: pi_oauth
  provider: openai-codex
  file: ${TEST_GOTATO_PI_HOME}/auth.json
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.API != "openai-codex-responses" || config.Model != "gpt-test" {
		t.Fatalf("config = %+v", config)
	}
	if config.Auth.Type != "pi_oauth" || config.Auth.File != "/tmp/pi/auth.json" {
		t.Fatalf("auth = %+v", config.Auth)
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if client.endpoint != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("endpoint = %q", client.endpoint)
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
