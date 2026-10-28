package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Without a setting, a recognized provider keeps the cache as long as it
// offers: an hour on Claude, 30 minutes on OpenAI models from GPT-5.6. A
// shorter setting falls back to the provider's default, which is the
// provider's shortest. OpenAI models before GPT-5.6 and the chat API keep
// their own default.
func TestCacheRetentionIsTheProvidersLongestUnlessSetShorter(t *testing.T) {
	for _, tc := range []struct {
		provider  Provider
		api       API
		model     string
		retention time.Duration
		ttl       string
	}{
		{ProviderAnthropic, APIAnthropicMessages, "claude-opus-4-6", 0, "1h"},
		{ProviderAnthropic, APIAnthropicMessages, "claude-opus-4-6", 2 * time.Hour, "1h"},
		{ProviderAnthropic, APIAnthropicMessages, "claude-opus-4-6", time.Hour, "1h"},
		{ProviderAnthropic, APIAnthropicMessages, "claude-opus-4-6", 30 * time.Minute, ""},
		{ProviderAnthropic, APIAnthropicMessages, "claude-opus-4-6", 5 * time.Minute, ""},
		{ProviderOpenAI, APIResponses, "gpt-6.1-sol", 0, "30m"},
		{ProviderOpenAI, APIResponses, "gpt-6", 0, "30m"},
		{ProviderOpenAI, APIResponses, "gpt-5.6", 0, "30m"},
		{ProviderOpenAI, APIResponses, "gpt-5.6-mini", 24 * time.Hour, "30m"},
		{ProviderOpenAI, APIResponses, "gpt-6.1-sol", 10 * time.Minute, ""},
		{ProviderOpenAI, APIResponses, "gpt-5.5", 0, ""},
		{ProviderOpenAI, APIResponses, "gpt-4.1", 0, ""},
		{ProviderOpenAI, APIResponses, "o3", 0, ""},
		{ProviderOpenAI, APIChatCompletions, "gpt-6.1-sol", 0, ""},
		{ProviderDeepSeek, APIChatCompletions, "deepseek-chat", 0, ""},
		{"", APIAnthropicMessages, "claude-opus-4-6", 0, ""},
		{"", APIResponses, "gpt-6.1-sol", 0, ""},
	} {
		body := encodeWith(t, tc.api, newProviderRules(tc.provider, tc.api, tc.model, tc.retention, false), twoRounds(tc.api))
		if got := sentCacheTTL(t, tc.api, body); got != tc.ttl {
			t.Errorf("%q %s %s retention %v: ttl = %q, want %q\n%s", tc.provider, tc.api, tc.model, tc.retention, got, tc.ttl, body)
		}
	}
}

// sentCacheTTL returns the cache ttl an encoded request asks for, and fails
// when an Anthropic request's markers disagree.
func sentCacheTTL(t *testing.T, api API, body string) string {
	t.Helper()
	switch api {
	case APIAnthropicMessages:
		markers := cacheControls(t, body)
		if len(markers) == 0 {
			t.Fatalf("no cache_control in %s", body)
		}
		first, _ := markers[0]["ttl"].(string)
		for _, marker := range markers {
			if ttl, _ := marker["ttl"].(string); ttl != first || marker["type"] != "ephemeral" {
				t.Fatalf("cache_control markers disagree: %v", markers)
			}
		}
		return first
	default:
		raw, ok := topLevel(t, body)["prompt_cache_options"]
		if !ok {
			return ""
		}
		var options struct {
			TTL string `json:"ttl"`
		}
		if err := json.Unmarshal(raw, &options); err != nil {
			t.Fatal(err)
		}
		return options.TTL
	}
}

// The model's own CacheRetention is the only setting.
func TestCacheRetentionComesFromTheConfig(t *testing.T) {
	ttl := func(retention time.Duration) string {
		t.Helper()
		client, err := New(Config{API: APIAnthropicMessages, Model: "claude-opus-4-6", CacheRetention: retention})
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := encodeAnthropicRequest(client.model, gotato.ModelRequest{SystemInstructions: "sys",
			CacheBreakpoints: []gotato.CacheBreakpoint{{After: gotato.CacheAnchorAfterSystem}}}, client.rules)
		if err != nil {
			t.Fatal(err)
		}
		return sentCacheTTL(t, APIAnthropicMessages, string(body))
	}
	if got := ttl(5 * time.Minute); got != "" {
		t.Fatalf("model setting 5m: ttl = %q", got)
	}
	if _, err := New(Config{API: APIAnthropicMessages, Model: "m", CacheRetention: -time.Minute}); err == nil {
		t.Fatal("a negative retention was accepted")
	}
}
