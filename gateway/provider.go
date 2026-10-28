package gateway

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Provider names who serves a model (Config.Provider). A recognized provider
// is sent what its documentation asks for: Claude and OpenAI only the
// reasoning of the current Round, and the longest prompt cache retention they
// offer. DeepSeek, and any provider the gateway does not recognize, gets every
// reasoning artifact back and no field or header beyond the request, which
// every compatible server accepts.
type Provider string

// The providers the gateway recognizes.
const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderDeepSeek  Provider = "deepseek"
	// ProviderOther turns recognition off, even on a provider's own address.
	ProviderOther Provider = "other"
)

// officialHosts are the providers' own addresses. Without a configured
// provider, only these are recognized: a gateway in front of the providers
// may serve any model under any name, so a model name is never used to guess.
var officialHosts = map[string]Provider{
	"api.anthropic.com": ProviderAnthropic,
	"api.openai.com":    ProviderOpenAI,
	"api.deepseek.com":  ProviderDeepSeek,
}

const (
	// anthropicBindingBeta is the beta header block_binding needs; without
	// it, the provider refuses the field.
	anthropicBindingBeta = "thinking-binding-controls-2026-08-01"
	// anthropicLongTTL is the longest cache_control ttl. A cache write with
	// it costs twice the input price; the default, five minutes, 1.25 times.
	anthropicLongTTL = "1h"
	// responsesTTL is the one prompt_cache_options.ttl the Responses API
	// takes, from GPT-5.6 on. It is also their default, so asking for it
	// only makes the retention explicit.
	responsesTTL = "30m"
)

// providerRules are what the gateway sends a recognized provider beyond the
// request itself. The zero value sends nothing extra and every reasoning
// artifact, as for DeepSeek or an unrecognized provider.
type providerRules struct {
	// roundReasoning sends only the reasoning of the current Round (see
	// roundStart).
	roundReasoning bool
	// anthropicCacheTTL is the ttl of every cache_control marker, or empty
	// for the provider's default.
	anthropicCacheTTL string
	// blockBinding asks Anthropic to drop a thinking block whose prefix
	// changed instead of refusing the request.
	blockBinding bool
	// responsesCacheTTL is prompt_cache_options.ttl on the Responses API,
	// or empty to send no such field.
	responsesCacheTTL string
	// deferredNative sends the catalog's tools deferred (see
	// Config.DeferredTools).
	deferredNative bool
}

// resolveProvider returns the provider configured, or the one whose own
// address endpoint is, or "" for a provider the gateway does not recognize.
func resolveProvider(configured Provider, endpoint string) (Provider, error) {
	switch provider := Provider(strings.ToLower(strings.TrimSpace(string(configured)))); provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek:
		return provider, nil
	case ProviderOther:
		return "", nil
	case "":
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", nil
		}
		return officialHosts[strings.ToLower(parsed.Hostname())], nil
	default:
		return "", fmt.Errorf("gateway: unsupported provider %q (use %s, %s, %s or %s)", configured, ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek, ProviderOther)
	}
}

// newProviderRules derives the rules for one model. A retention shorter than
// a provider's longer option gets the provider's default, its shortest.
func newProviderRules(provider Provider, api API, model string, retention time.Duration, noDropBlock bool) providerRules {
	long := func(option time.Duration) bool { return retention == 0 || retention >= option }
	var rules providerRules
	switch provider {
	case ProviderAnthropic:
		rules.roundReasoning = true
		if api == APIAnthropicMessages {
			if long(time.Hour) {
				rules.anthropicCacheTTL = anthropicLongTTL
			}
			rules.blockBinding = !noDropBlock && checksThinkingPrefix(model)
		}
	case ProviderOpenAI:
		rules.roundReasoning = true
		// Models before GPT-5.6 take prompt_cache_retention instead, and
		// their default for an organization without Zero Data Retention is
		// already their longest, 24 hours. The chat API is not documented
		// to take either field.
		if api == APIResponses && long(30*time.Minute) && takesPromptCacheOptions(model) {
			rules.responsesCacheTTL = responsesTTL
		}
	}
	return rules
}
