package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The retention is a fact of the model, stated by the team; nothing is guessed
// from a provider, a host or a name. The Anthropic wire asks for the one-hour
// ttl on every cache_control marker when the model keeps the cache an hour or
// more, and sends the provider's default (no ttl) otherwise. The Responses wire
// sends prompt_cache_options only for the one retention it takes, 30 minutes.
// The Chat wire has no field for it.
func TestCacheRetentionFactSteersTheTTL(t *testing.T) {
	for _, tc := range []struct {
		api       API
		retention time.Duration
		ttl       string
	}{
		{APIAnthropicMessages, 0, ""},
		{APIAnthropicMessages, 5 * time.Minute, ""},
		{APIAnthropicMessages, 30 * time.Minute, ""},
		{APIAnthropicMessages, time.Hour, "1h"},
		{APIAnthropicMessages, 2 * time.Hour, "1h"},
		{APIResponses, 0, ""},
		{APIResponses, 10 * time.Minute, ""},
		{APIResponses, 30 * time.Minute, "30m"},
		{APIResponses, time.Hour, ""},
		{APIResponses, 24 * time.Hour, ""},
		{APIChatCompletions, 0, ""},
		{APIChatCompletions, time.Hour, ""},
		{APIChatCompletions, 30 * time.Minute, ""},
	} {
		body := encodeWith(t, tc.api, gotato.ModelFacts{CacheRetention: tc.retention}, twoRounds(tc.api))
		if got := sentCacheTTL(t, tc.api, body); got != tc.ttl {
			t.Errorf("%s retention %v: ttl = %q, want %q\n%s", tc.api, tc.retention, got, tc.ttl, body)
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

// The retention steers the encoder through the wire's Encode as well, from the
// model the Client was built with, and a negative one is refused by New.
func TestCacheRetentionReachesTheWireThroughTheClient(t *testing.T) {
	client, err := New(AnthropicMessages{}, Connection{Endpoint: "https://llm.example.com/v1/messages"},
		gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown(), CacheRetention: 2 * time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.Config().Facts.CacheRetention; got != 2*time.Hour {
		t.Fatalf("Config().Facts.CacheRetention = %v", got)
	}
	encoded, err := client.wire.Encode(Call{Model: client.Config(), Request: gotato.ModelRequest{SystemInstructions: "sys",
		CacheBreakpoints: []gotato.CacheBreakpoint{{After: gotato.CacheAnchorAfterSystem}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sentCacheTTL(t, APIAnthropicMessages, string(encoded.Body)); got != "1h" {
		t.Fatalf("ttl = %q, want 1h", got)
	}
	_, err = New(AnthropicMessages{}, Connection{Endpoint: "https://llm.example.com/v1/messages"},
		gotato.ModelConfig{Name: "m", Facts: gotato.ModelFacts{Window: gotato.WindowUnknown(), CacheRetention: -time.Minute}})
	if err == nil {
		t.Fatal("a negative retention was accepted")
	}
}
