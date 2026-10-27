// Package gateway provides provider streaming Model adapters for Gotato: the
// OpenAI-compatible Chat Completions and Responses APIs and the Anthropic
// Messages API.
//
// It builds on the provider-neutral gotato Model contract and net/http, and
// reads its configuration file with yaml.v3 (LoadYAML; gateway.example.yaml is
// the commented file to copy). Provider authentication, request
// encoding, retries, SSE decoding, and provider error classification live here,
// outside Core, and so do the rules a few providers set for sending reasoning
// back and for prompt cache retention (see Config.Provider).
package gateway

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

const (
	defaultMaxRetries   = 2
	defaultRetryBackoff = 200 * time.Millisecond
	// statusOverloaded is the non-standard status a provider returns when it
	// sheds load.
	statusOverloaded = 529
)

type Config struct {
	// API selects the wire protocol: APIChatCompletions (the default when
	// empty), APIResponses, or APIAnthropicMessages. Any other
	// value is refused.
	API API
	// Endpoint is the complete provider URL. When empty, BaseURL is used.
	Endpoint string
	// BaseURL may be an origin or an API base URL.
	BaseURL string
	APIKey  string
	Model   string

	HTTPClient *http.Client
	Headers    map[string]string
	// CacheKeyHeader, when set, sends each request's ModelRequest.CacheKey in
	// this HTTP header, for a provider that routes its prompt cache by a
	// session header such as "session-id".
	CacheKeyHeader string
	// CacheKeyField names the top-level body field that carries the
	// CacheKey. Empty uses the wire API's default: "prompt_cache_key" for
	// openai-responses, none for openai-chat-completions. "none" sends no
	// field. It must not name a field the wire API already sends. The
	// Anthropic Messages API takes no such field; it caches by cache_control.
	CacheKeyField string
	MaxRetries    int
	RetryBackoff  time.Duration
	// NoRetries disables retries entirely. MaxRetries counts retries after
	// the first attempt; 0 means the default.
	NoRetries bool
	// Provider names who serves the model: ProviderAnthropic,
	// ProviderOpenAI, ProviderDeepSeek, or ProviderOther. Empty recognizes
	// only the providers' own addresses (api.anthropic.com, api.openai.com,
	// api.deepseek.com); behind any other address, name the provider here.
	Provider Provider
	// CacheRetention is how long the provider should keep the prompt cache.
	// Zero asks a recognized provider for its longest: an hour on Claude,
	// and 30 minutes on OpenAI's Responses API from GPT-5.6 on. A shorter
	// value than the provider's longer option keeps its default. An
	// unrecognized provider is sent no retention.
	CacheRetention time.Duration
	// NoDropBlock stops asking Claude to drop a thinking block whose prefix
	// changed (block_binding drop_block) on the models that check it; the
	// provider then refuses such a request instead.
	NoDropBlock bool
	// ContextWindowTokens is the model's context window in tokens. Zero means
	// unknown: the gateway keeps no table of windows, and compaction then
	// has no default ceiling. Negative is refused.
	ContextWindowTokens int
	// OverflowPhrases adds a team's own wording to the phrases by which an
	// error says the request was longer than the model's context window.
	// Each is a fragment of an error's message, code or type, matched
	// without regard to case, as the defaults in overflow.go are; a phrase
	// that is only a code, such as "request_too_big", works too. A 400 or
	// 413 that matches any of them is reported as a context overflow.
	OverflowPhrases []string
	// DeferredTools is how the wire API shows the tools a Session loads
	// one by one from its catalog: DeferredFormNative or DeferredFormList. Empty
	// uses the wire API's default: native on openai-responses and
	// anthropic-messages, list on openai-chat-completions. Native keeps the
	// tool list of the request the same while tools load (every catalog
	// tool is sent deferred, and a load shows as a reference to it in the
	// history), so the prompt cache survives a load. List sends the tools
	// loaded so far, so the tool list grows when one loads. A wire API
	// without a native form (openai-chat-completions) always uses list,
	// whatever is set here; Client.DeferredTools reports what is used.
	DeferredTools DeferredForm
}

// DeferredForm is how deferred tools reach the provider (Config.DeferredTools).
type DeferredForm string

// The forms of Config.DeferredTools.
const (
	DeferredFormNative DeferredForm = "native"
	DeferredFormList   DeferredForm = "list"
)

type Client struct {
	overflow       overflowMatcher
	api            API
	endpoint       string
	apiKey         string
	model          string
	httpClient     *http.Client
	headers        map[string]string
	cacheKeyHeader string
	cacheKeyField  string
	maxRetries     int
	retryBackoff   time.Duration
	provider       Provider
	rules          providerRules
	contextWindow  int
}

func New(config Config) (*Client, error) {
	api, err := normalizeAPI(config.API)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" {
		base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
		if base == "" && api == APIResponses {
			base = defaultResponsesBaseURL
		}
		if base == "" && api == APIAnthropicMessages {
			base = defaultAnthropicBaseURL
		}
		if base == "" {
			return nil, fmt.Errorf("gateway: BaseURL or Endpoint is required")
		}
		path := "/chat/completions"
		switch api {
		case APIResponses:
			path = "/responses"
		case APIAnthropicMessages:
			path = "/messages"
		}
		if !strings.HasSuffix(base, "/v1") {
			path = "/v1" + path
		}
		endpoint = base + path
	}
	if config.Model == "" {
		return nil, fmt.Errorf("gateway: Model is required")
	}
	provider, err := resolveProvider(config.Provider, endpoint)
	if err != nil {
		return nil, err
	}
	retention, err := cacheRetention(config.CacheRetention)
	if err != nil {
		return nil, err
	}
	if config.ContextWindowTokens < 0 {
		return nil, fmt.Errorf("gateway: ContextWindowTokens cannot be negative")
	}
	if config.MaxRetries < 0 {
		return nil, fmt.Errorf("gateway: MaxRetries cannot be negative")
	}
	if config.NoRetries && config.MaxRetries != 0 {
		return nil, fmt.Errorf("gateway: NoRetries and MaxRetries are mutually exclusive")
	}
	maxRetries := config.MaxRetries
	switch {
	case config.NoRetries:
		maxRetries = 0
	case maxRetries == 0:
		maxRetries = defaultMaxRetries
	}
	deferred, err := resolveDeferredTools(config.DeferredTools, api)
	if err != nil {
		return nil, err
	}
	cacheKeyHeader := strings.TrimSpace(config.CacheKeyHeader)
	if strings.ContainsAny(cacheKeyHeader, " \t:") {
		return nil, fmt.Errorf("gateway: CacheKeyHeader %q is not a header name", config.CacheKeyHeader)
	}
	cacheKeyField := strings.TrimSpace(config.CacheKeyField)
	switch {
	case cacheKeyField == "none":
		cacheKeyField = ""
	case cacheKeyField == "" && api == APIResponses:
		cacheKeyField = defaultResponsesCacheKeyField
	case cacheKeyField != "" && api == APIAnthropicMessages:
		return nil, fmt.Errorf("gateway: CacheKeyField is not supported by %s, which caches by cache_control", APIAnthropicMessages)
	}
	backoff := config.RetryBackoff
	if backoff == 0 {
		backoff = defaultRetryBackoff
	}
	if backoff < 0 {
		return nil, fmt.Errorf("gateway: RetryBackoff cannot be negative")
	}
	client := config.HTTPClient
	if client == nil {
		// A stalled upstream must not hold a Run open forever. Overall stream
		// duration stays governed by the caller's context; these bound only
		// connection setup and the wait for response headers.
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				ExpectContinueTimeout: time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		}
	}
	rules := newProviderRules(provider, api, config.Model, retention, config.NoDropBlock)
	rules.deferredNative = deferred == DeferredFormNative
	return &Client{
		api:            api,
		endpoint:       endpoint,
		apiKey:         config.APIKey,
		model:          config.Model,
		httpClient:     client,
		headers:        cloneHeaders(config.Headers),
		cacheKeyHeader: cacheKeyHeader,
		cacheKeyField:  cacheKeyField,
		maxRetries:     maxRetries,
		retryBackoff:   backoff,
		provider:       provider,
		rules:          rules,
		contextWindow:  config.ContextWindowTokens,
		overflow:       newOverflowMatcher(config.OverflowPhrases),
	}, nil
}

// resolveDeferredTools returns the form a wire API uses for deferred tools:
// the configured one when the wire API has it, its default when none is
// configured, and list for a wire API without a native form.
func resolveDeferredTools(configured DeferredForm, api API) (DeferredForm, error) {
	form := DeferredForm(strings.ToLower(strings.TrimSpace(string(configured))))
	switch form {
	case "", DeferredFormNative, DeferredFormList:
	default:
		return "", fmt.Errorf("gateway: unsupported DeferredTools %q (use %s or %s)", configured, DeferredFormNative, DeferredFormList)
	}
	if api == APIChatCompletions {
		return DeferredFormList, nil
	}
	if form == "" {
		return DeferredFormNative, nil
	}
	return form, nil
}

// DeferredTools reports the form this Client sends deferred tools in:
// DeferredFormNative or DeferredFormList.
func (c *Client) DeferredTools() DeferredForm {
	if c.rules.deferredNative {
		return DeferredFormNative
	}
	return DeferredFormList
}

// ContextWindowTokens returns the model's context window in tokens as configured,
// or 0 when it is not. The gateway keeps no table of windows.
func (c *Client) ContextWindowTokens() int { return c.contextWindow }

// CacheRetention returns how long the provider keeps the prompt cache after a
// request, as far as this client knows: the cache_control ttl it sends on
// Anthropic Messages (1h, or 5m, the provider's default, when a shorter
// retention is configured), and 30m when it sends the Responses API a
// prompt_cache_options ttl. It is 0, unknown, everywhere else.
func (c *Client) CacheRetention() time.Duration {
	switch {
	case c.provider == ProviderAnthropic && c.api == APIAnthropicMessages:
		if c.rules.anthropicCacheTTL == anthropicLongTTL {
			return time.Hour
		}
		return 5 * time.Minute
	case c.rules.responsesCacheTTL == responsesTTL:
		return 30 * time.Minute
	}
	return 0
}

// API is a wire protocol (Config.API): APIChatCompletions, APIResponses or
// APIAnthropicMessages.
type API string

const (
	APIChatCompletions API = "openai-chat-completions"
	APIResponses       API = "openai-responses"
)

func normalizeAPI(api API) (API, error) {
	switch API(strings.TrimSpace(string(api))) {
	case "", APIChatCompletions:
		return APIChatCompletions, nil
	case APIResponses:
		return APIResponses, nil
	case APIAnthropicMessages:
		return APIAnthropicMessages, nil
	default:
		return "", fmt.Errorf("gateway: unsupported API %q (use %s, %s, or %s)", api, APIChatCompletions, APIResponses, APIAnthropicMessages)
	}
}

func (c *Client) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch c.api {
	case APIResponses:
		return c.streamResponses(ctx, request)
	case APIAnthropicMessages:
		return c.streamAnthropic(ctx, request)
	}
	body, names, err := encodeRequest(c.model, request, c.rules)
	if err == nil {
		body, err = c.withCacheKeyField(body, request.CacheKey)
	}
	if err != nil {
		return nil, err
	}
	open := c.withCacheKeyHeader(c.do, request.CacheKey)
	response, err := open(ctx, body)
	if err != nil {
		return nil, err
	}
	return &stream{
		response: response,
		reader:   bufio.NewReader(response.Body),
		nameMap:  names,
		byIndex:  make(map[int]*callBuffer),
		restart:  restarter{client: c, body: body, open: open},
		overflow: c.overflow,
	}, nil
}
