package gateway

import (
	"testing"
	"time"
)

// The provider comes from the configuration, or from the provider's own
// address. A model name is never used to guess it: behind a gateway, a model
// name may be an alias for anything.
func TestTheProviderIsConfiguredOrKnownByItsOfficialAddress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
		want   Provider
	}{
		{"Anthropic's address", Config{API: APIAnthropicMessages}, ProviderAnthropic},
		{"OpenAI's address", Config{API: APIResponses}, ProviderOpenAI},
		{"OpenAI's chat address", Config{BaseURL: "https://api.openai.com/v1"}, ProviderOpenAI},
		{"DeepSeek's chat address", Config{BaseURL: "https://api.deepseek.com"}, ProviderDeepSeek},
		{"DeepSeek's Anthropic address", Config{API: APIAnthropicMessages, BaseURL: "https://api.deepseek.com/anthropic"}, ProviderDeepSeek},
		{"a gateway in front", Config{API: APIAnthropicMessages, BaseURL: "https://gateway.example.com"}, ""},
		{"a model name says nothing", Config{BaseURL: "https://gateway.example.com/v1", Model: "claude-opus-5-5"}, ""},
		{"configured", Config{API: APIAnthropicMessages, BaseURL: "https://gateway.example.com", Provider: "anthropic"}, ProviderAnthropic},
		{"configured over the address", Config{BaseURL: "https://api.openai.com/v1", Provider: "deepseek"}, ProviderDeepSeek},
		{"other turns recognition off", Config{API: APIAnthropicMessages, Provider: "other"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config.Model == "" {
				tc.config.Model = "m"
			}
			client, err := New(tc.config)
			if err != nil {
				t.Fatal(err)
			}
			if client.provider != tc.want {
				t.Fatalf("provider = %q, want %q", client.provider, tc.want)
			}
			recognized := tc.want == ProviderAnthropic || tc.want == ProviderOpenAI
			if client.RoundReasoningOnly() != recognized {
				t.Fatalf("RoundReasoningOnly = %v, want %v", client.RoundReasoningOnly(), recognized)
			}
		})
	}
	if _, err := New(Config{BaseURL: "https://gateway.example.com", Model: "m", Provider: "gemini"}); err == nil {
		t.Fatal("an unknown provider name was accepted")
	}
}

func TestParseYAMLProviderRetentionAndDropBlock(t *testing.T) {
	config, err := parseYAML([]byte("api: anthropic-messages\nbase_url: https://gateway.example.com\nmodel: m\nprovider: anthropic\ncache_retention: 10m\nno_drop_block: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Provider != ProviderAnthropic || config.CacheRetention != 10*time.Minute || !config.NoDropBlock {
		t.Fatalf("config = %+v", config)
	}
	for _, bad := range []string{"cache_retention: soon\n", "provider: gemini\n"} {
		if _, err := parseYAML([]byte("base_url: https://gateway.example.com\nmodel: m\n" + bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
