package gateway

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

const newTestEndpoint = "https://llm.example.com/v1/chat/completions"

func statedModel() gotato.ModelConfig {
	return gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(8000)}}
}

// New reports every mistake of its settings before the first call, and makes
// no request.
func TestNewRefusesSettingsThatCannotWork(t *testing.T) {
	negative, nan := -1.0, math.NaN()
	for _, tc := range []struct {
		name  string
		wire  WireAPI
		conn  Connection
		model gotato.ModelConfig
		opts  []Option
		want  string
	}{
		{"no wire", nil, Connection{Endpoint: newTestEndpoint}, statedModel(), nil, "WireAPI is required"},
		{"no endpoint", ChatCompletions{}, Connection{}, statedModel(), nil, "endpoint"},
		{"endpoint is not a URL", ChatCompletions{}, Connection{Endpoint: "://nope"}, statedModel(), nil, "endpoint"},
		{"endpoint without a host", ChatCompletions{}, Connection{Endpoint: "https:///v1/chat"}, statedModel(), nil, "endpoint"},
		{"endpoint with another scheme", ChatCompletions{}, Connection{Endpoint: "ftp://llm.example.com/v1"}, statedModel(), nil, "endpoint"},
		{"endpoint is a bare host", ChatCompletions{}, Connection{Endpoint: "llm.example.com"}, statedModel(), nil, "endpoint"},
		{"no model name", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Facts: gotato.ModelFacts{Window: gotato.WindowUnknown()}}, nil, "Name"},
		{"blank model name", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Name: "  ", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown()}}, nil, "Name"},
		{"window not stated", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Name: "m"}, nil, "not stated"},
		{"window of zero tokens", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(0)}}, nil, "Window"},
		{"negative retention", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown(), CacheRetention: -time.Second}}, nil, "CacheRetention"},
		{"negative temperature", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown()}, Sampling: gotato.Sampling{Temperature: &negative}}, nil, "Temperature"},
		{"temperature that is not a number", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown()}, Sampling: gotato.Sampling{Temperature: &nan}}, nil, "Temperature"},
		{"header name with a space", ChatCompletions{}, Connection{Endpoint: newTestEndpoint, Headers: map[string]string{"x team": "a"}}, statedModel(), nil, "Headers"},
		{"header value with a newline", ChatCompletions{}, Connection{Endpoint: newTestEndpoint, Headers: map[string]string{"X-Team": "a\nb"}}, statedModel(), nil, "Headers"},
		{"cache key header with a space", ChatCompletions{}, Connection{Endpoint: newTestEndpoint, CacheKeyHeader: "session id"}, statedModel(), nil, "CacheKeyHeader"},
		{"negative retries", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, statedModel(), []Option{WithRetry(Retry{Max: -1})}, "Retry"},
		{"negative backoff", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, statedModel(), []Option{WithRetry(Retry{Max: 1, Backoff: -time.Second})}, "Retry"},
		{"negative max wait", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, statedModel(), []Option{WithRetry(Retry{Max: 1, MaxWait: -time.Second})}, "Retry"},
		{"negative backoff with retries off", ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, statedModel(), []Option{WithRetry(Retry{Backoff: -time.Second})}, "Retry"},
		{"chat cache key field the request sends", ChatCompletions{CacheKeyField: "messages"}, Connection{Endpoint: newTestEndpoint}, statedModel(), nil, "already sends"},
		{"responses cache key field the request sends", Responses{CacheKeyField: "input"}, Connection{Endpoint: newTestEndpoint}, statedModel(), nil, "already sends"},
		{"responses bad deferred form", Responses{Deferred: "sideways"}, Connection{Endpoint: newTestEndpoint}, statedModel(), nil, "Deferred"},
		{"anthropic bad deferred form", AnthropicMessages{Deferred: "sideways"}, Connection{Endpoint: newTestEndpoint}, statedModel(), nil, "Deferred"},
	} {
		client, err := New(tc.wire, tc.conn, tc.model, tc.opts...)
		if err == nil {
			t.Errorf("%s: accepted (client %v)", tc.name, client)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
	}
}

func TestNewAcceptsTheMinimumAndTheDefaultsAreWhatTheDocsSay(t *testing.T) {
	for _, window := range []gotato.Window{gotato.WindowTokens(1), gotato.WindowUnknown()} {
		client, err := New(ChatCompletions{}, Connection{Endpoint: "http://127.0.0.1:1/chat"}, gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: window}})
		if err != nil {
			t.Fatalf("window %v: %v", window, err)
		}
		if client.retry != DefaultRetry {
			t.Errorf("retry = %+v, want DefaultRetry %+v", client.retry, DefaultRetry)
		}
		if client.httpClient == nil || client.classify == nil {
			t.Errorf("no HTTP client or classifier by default: %+v", client)
		}
	}
	if DefaultRetry != (Retry{Max: 2, Backoff: 200 * time.Millisecond, MaxWait: 60 * time.Second}) {
		t.Fatalf("DefaultRetry = %+v", DefaultRetry)
	}
	// Nil options and a nil classifier are not mistakes.
	if _, err := New(ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, statedModel(), nil, WithClassifier(nil)); err != nil {
		t.Fatal(err)
	}
	// Valid cache key fields, and "none", are accepted.
	for _, wire := range []WireAPI{ChatCompletions{CacheKeyField: "prompt_cache_key"}, ChatCompletions{CacheKeyField: "none"}, Responses{CacheKeyField: "none"}, Responses{CacheKeyField: "session_hint"}, Responses{Deferred: DeferredFormList}, AnthropicMessages{Deferred: DeferredFormList}} {
		if _, err := New(wire, Connection{Endpoint: newTestEndpoint}, statedModel()); err != nil {
			t.Errorf("%+v: %v", wire, err)
		}
	}
}

// Config returns the ModelConfig the Client was built with, so the runtime
// reads the facts from it; the Temperature it holds is a copy on both sides.
func TestConfigReturnsTheModelConfigAndACopyOfTheTemperature(t *testing.T) {
	temperature := 0.5
	model := gotato.ModelConfig{
		Name:     "m",
		Facts:    gotato.ModelFacts{Window: gotato.WindowTokens(8000), CacheRetention: time.Hour, RoundReasoning: true, ThinkingBinding: true},
		Sampling: gotato.Sampling{Temperature: &temperature, MaxTokens: 100, ReasoningEffort: "high"},
	}
	client, err := New(ChatCompletions{}, Connection{Endpoint: newTestEndpoint}, model)
	if err != nil {
		t.Fatal(err)
	}
	// A change of the caller's value after New does not reach the Client.
	temperature = 0.9
	got := client.Config()
	if got.Name != "m" || got.Facts != model.Facts || got.Sampling.MaxTokens != 100 || got.Sampling.ReasoningEffort != "high" {
		t.Fatalf("Config() = %+v", got)
	}
	if got.Sampling.Temperature == nil || *got.Sampling.Temperature != 0.5 {
		t.Fatalf("Config().Sampling.Temperature = %v, want 0.5", got.Sampling.Temperature)
	}
	// And a change of what Config returned does not reach it either.
	*got.Sampling.Temperature = 1.5
	if again := client.Config(); *again.Sampling.Temperature != 0.5 {
		t.Fatalf("a write to Config()'s Temperature reached the Client: %v", *again.Sampling.Temperature)
	}
	if window := client.Config().Facts.Window.Tokens(); window != 8000 {
		t.Fatalf("window = %d", window)
	}
	var _ gotato.Model = client
}

// New copies the configured headers, so a change of the caller's map after New
// does not reach the requests.
func TestNewCopiesTheConnectionHeaders(t *testing.T) {
	headers := map[string]string{"X-Team": "a"}
	client, err := New(ChatCompletions{}, Connection{Endpoint: newTestEndpoint, Headers: headers}, statedModel())
	if err != nil {
		t.Fatal(err)
	}
	headers["X-Team"] = "changed"
	headers["X-New"] = "added"
	got := client.baseHeader(context.Background(), "")
	if got.Get("X-Team") != "a" || got.Get("X-New") != "" {
		t.Fatalf("headers = %v", got)
	}
	if got.Get("User-Agent") != defaultUserAgent {
		t.Fatalf("User-Agent = %q", got.Get("User-Agent"))
	}
}
