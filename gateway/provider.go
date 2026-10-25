package gateway

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// Providers the gateway recognizes. A recognized provider is sent what its
// documentation asks for: Claude and OpenAI only the reasoning of the current
// Round, and the longest prompt cache retention they offer. DeepSeek, and any
// provider the gateway does not recognize, gets every reasoning artifact back
// and no field or header beyond the request, which every compatible server
// accepts.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderDeepSeek  = "deepseek"
	// ProviderOther turns recognition off, even on a provider's own address.
	ProviderOther = "other"
)

// CacheRetentionEnv names the environment variable that sets the prompt
// cache retention of every model the process builds, over each model's own
// Config.CacheRetention, for example GOTATO_GATEWAY_CACHE_RETENTION=5m.
const CacheRetentionEnv = "GOTATO_GATEWAY_CACHE_RETENTION"

// officialHosts are the providers' own addresses. Without a configured
// provider, only these are recognized: a gateway in front of the providers
// may serve any model under any name, so a model name is never used to guess.
var officialHosts = map[string]string{
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

// thinkingPrefixModels check the prefix each thinking block was written
// under: the system prompt, the tools and every Message before it. Only they
// take block_binding, and only with thinking.type "adaptive", which is their
// default. Sending "adaptive" to another Claude model would turn its thinking
// on (Opus 4.6, Sonnet 4.6) or be refused (Opus 4.5 and before).
var thinkingPrefixModels = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}

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
func resolveProvider(configured, endpoint string) (string, error) {
	switch provider := strings.ToLower(strings.TrimSpace(configured)); provider {
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

// cacheRetention returns the retention every request asks for: the global
// setting when it is set, and the model's own otherwise. Zero means the
// provider's longest.
func cacheRetention(configured time.Duration) (time.Duration, error) {
	retention := configured
	if raw := strings.TrimSpace(os.Getenv(CacheRetentionEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("gateway: invalid %s %q: %w", CacheRetentionEnv, raw, err)
		}
		retention = parsed
	}
	if retention < 0 {
		return 0, fmt.Errorf("gateway: CacheRetention cannot be negative")
	}
	return retention, nil
}

// newProviderRules derives the rules for one model. A retention shorter than
// a provider's longer option gets the provider's default, its shortest.
func newProviderRules(provider, api, model string, retention time.Duration, noDropBlock bool) providerRules {
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

func checksThinkingPrefix(model string) bool {
	for _, name := range thinkingPrefixModels {
		if model == name || strings.HasPrefix(model, name+"-") || strings.HasPrefix(model, name+"@") {
			return true
		}
	}
	return false
}

// takesPromptCacheOptions reports whether an OpenAI model is GPT-5.6 or
// later, such as gpt-5.6-mini or gpt-6.1-sol.
func takesPromptCacheOptions(model string) bool {
	version, ok := strings.CutPrefix(model, "gpt-")
	if !ok {
		return false
	}
	if end := strings.IndexFunc(version, func(r rune) bool { return r != '.' && (r < '0' || r > '9') }); end >= 0 {
		version = version[:end]
	}
	majorText, minorText, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return false
	}
	minor := 0
	if minorText != "" {
		if minor, err = strconv.Atoi(minorText); err != nil {
			return false
		}
	}
	return major > 5 || (major == 5 && minor >= 6)
}

// roundStart returns the index of the last user Message: where the current
// Round starts, or -1 when there is none. A Round is a user Message and every
// reply and Tool Result after it. A Steer is a user Message, so it starts a
// new Round; a Continue without a new prompt stays in the Round it resumes;
// Tool Results have a role of their own and never start one, and neither does
// a runtime notice (G-F29): it is not the user speaking.
//
// With roundReasoning, an assistant reply before this index is sent without
// its reasoning. Claude and OpenAI need reasoning back only inside the tool
// loop in progress. What is left out is always every reasoning artifact from
// the start of the Transcript up to the Round, and it is never sent again, so
// Claude's check of the prefix a thinking block was written under still holds
// for every block that is sent.
func roundStart(messages []gotato.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == gotato.RoleUser {
			return i
		}
	}
	return -1
}

// keepsReasoning reports whether the reply at index keeps its reasoning.
func (r providerRules) keepsReasoning(index, start int) bool {
	return !r.roundReasoning || index > start
}

// RoundReasoningOnly reports whether the provider is sent only the reasoning
// of the current Round, so a Checkpoint can leave the reasoning out of the
// Messages it keeps (modelctx.RoundReasoner). It is true for a recognized
// Claude or OpenAI provider.
func (c *Client) RoundReasoningOnly() bool { return c.rules.roundReasoning }
