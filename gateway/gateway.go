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

// Config is the settings of one Client: which wire API to speak, where, as
// whom, and how to retry. Only Model and an address (Endpoint or BaseURL) are
// required: the Responses and Anthropic Messages APIs have a default address.
// The Responses API also needs an APIKey. LoadYAML reads a Config from a file.
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
	// MaxRetries counts retries after the first attempt. 0 means the
	// default; -1 turns retries off. Any other negative value is refused.
	MaxRetries   int
	RetryBackoff time.Duration
	// MaxRetryWait caps the wait before a retry, including a provider's
	// Retry-After. 0 means 60 seconds; negative is refused.
	MaxRetryWait time.Duration
	// ConnectTimeout bounds opening a connection: the dial and the TLS
	// handshake each. 0 means 10 seconds; negative is refused. It, and
	// ResponseHeaderTimeout, apply to the HTTP client the gateway makes; a
	// Config.HTTPClient is used as it is and keeps its own.
	ConnectTimeout time.Duration
	// ResponseHeaderTimeout bounds the wait for the response headers after
	// the request is sent. A model behind a proxy that sends headers only
	// with its first token may need more. 0 means 60 seconds; negative is
	// refused. The stream that follows is bounded by the caller's context.
	ResponseHeaderTimeout time.Duration
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
	// provider then refuses such a request instead. The models that take
	// block_binding are chosen by name (thinkingPrefixModels in
	// reasoning_replay.go); this is the opt-out.
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

// Client is a gotato.Model that streams from one provider endpoint over one
// wire API. It keeps no per-request state, so one Client serves many
// concurrent Runs.
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
	maxRetryWait   time.Duration
	provider       Provider
	rules          providerRules
	contextWindow  int
}

// New checks config and returns a Client for it. It makes no request: a
// refusal here is a mistake in the settings, and every one is reported
// before the first call to Stream.
func New(config Config) (*Client, error) {
	api, err := normalizeAPI(config.API)
	if err != nil {
		return nil, err
	}
	endpoint, err := resolveEndpoint(config, api)
	if err != nil {
		return nil, err
	}
	if err := checkEndpoint(endpoint); err != nil {
		return nil, err
	}
	if config.Model == "" {
		return nil, fmt.Errorf("gateway: Model is required")
	}
	if err := checkHeaders(config.Headers); err != nil {
		return nil, fmt.Errorf("gateway: Headers: %w", err)
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
	maxRetries, err := resolveRetries(config)
	if err != nil {
		return nil, err
	}
	deferred, err := resolveDeferredTools(config.DeferredTools, api)
	if err != nil {
		return nil, err
	}
	cacheKeyHeader, cacheKeyField, err := resolveCacheKey(config, api)
	if err != nil {
		return nil, err
	}
	backoff := config.RetryBackoff
	if backoff == 0 {
		backoff = defaultRetryBackoff
	}
	if backoff < 0 {
		return nil, fmt.Errorf("gateway: RetryBackoff cannot be negative")
	}
	maxRetryWait, connect, headerWait, err := resolveHTTPLimits(config)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = newHTTPClient(connect, headerWait)
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
		maxRetryWait:   maxRetryWait,
		provider:       provider,
		rules:          rules,
		contextWindow:  config.ContextWindowTokens,
		overflow:       newOverflowMatcher(config.OverflowPhrases),
	}, nil
}

// resolveEndpoint returns the URL requests go to: Config.Endpoint as given, or
// the wire API's path under BaseURL (its own origin for Responses and
// Anthropic Messages when BaseURL is empty). A path gets /v1 in front unless
// BaseURL already ends in it.
func resolveEndpoint(config Config, api API) (string, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint != "" {
		return endpoint, nil
	}
	base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if base == "" && api == APIResponses {
		base = defaultResponsesBaseURL
	}
	if base == "" && api == APIAnthropicMessages {
		base = defaultAnthropicBaseURL
	}
	if base == "" {
		return "", fmt.Errorf("gateway: BaseURL or Endpoint is required")
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
	return base + path, nil
}

// resolveRetries returns how many times a failed request is sent again: none
// with MaxRetries -1, MaxRetries when set, the default otherwise.
func resolveRetries(config Config) (int, error) {
	switch {
	case config.MaxRetries < -1:
		return 0, fmt.Errorf("gateway: MaxRetries cannot be below -1")
	case config.MaxRetries == -1:
		return 0, nil
	case config.MaxRetries == 0:
		return defaultMaxRetries, nil
	}
	return config.MaxRetries, nil
}

// resolveCacheKey returns the header and the body field that carry the
// request's CacheKey, empty for one that is not sent.
func resolveCacheKey(config Config, api API) (header, field string, err error) {
	header = strings.TrimSpace(config.CacheKeyHeader)
	if header != "" && !validHeaderName(header) {
		return "", "", fmt.Errorf("gateway: CacheKeyHeader %q is not a header name", config.CacheKeyHeader)
	}
	field = strings.TrimSpace(config.CacheKeyField)
	switch {
	case field == "none":
		field = ""
	case field == "" && api == APIResponses:
		field = defaultResponsesCacheKeyField
	case field != "" && api == APIAnthropicMessages:
		return "", "", fmt.Errorf("gateway: CacheKeyField is not supported by %s, which caches by cache_control", APIAnthropicMessages)
	}
	// [CONTRACT] a cache key field that equals a field the request sends would silently replace it; refused at New, not at the first call.
	if reservedBodyField(api, field) {
		return "", "", fmt.Errorf("gateway: CacheKeyField %q is a field %s already sends", field, api)
	}
	return header, field, nil
}

// The limits the gateway uses when Config sets none.
const (
	defaultMaxRetryWait          = 60 * time.Second
	defaultConnectTimeout        = 10 * time.Second
	defaultResponseHeaderTimeout = 60 * time.Second
)

// resolveHTTPLimits returns MaxRetryWait, ConnectTimeout and
// ResponseHeaderTimeout with their defaults for zero.
func resolveHTTPLimits(config Config) (retryWait, connect, header time.Duration, err error) {
	if config.MaxRetryWait < 0 || config.ConnectTimeout < 0 || config.ResponseHeaderTimeout < 0 {
		return 0, 0, 0, fmt.Errorf("gateway: MaxRetryWait, ConnectTimeout and ResponseHeaderTimeout cannot be negative")
	}
	retryWait, connect, header = config.MaxRetryWait, config.ConnectTimeout, config.ResponseHeaderTimeout
	if retryWait == 0 {
		retryWait = defaultMaxRetryWait
	}
	if connect == 0 {
		connect = defaultConnectTimeout
	}
	if header == 0 {
		header = defaultResponseHeaderTimeout
	}
	return retryWait, connect, header, nil
}

// newHTTPClient is the client used when Config.HTTPClient is nil. A
// stalled upstream must not hold a Run open forever. Overall stream duration
// stays governed by the caller's context; these bound only connection setup
// and the wait for response headers.
func newHTTPClient(connect, header time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connect, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   connect,
			ResponseHeaderTimeout: header,
			ExpectContinueTimeout: time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// DeferredTools reports the form this Client sends deferred tools in:
// DeferredFormNative or DeferredFormList.
func (c *Client) DeferredTools() DeferredForm {
	if c.rules.deferredNative {
		return DeferredFormNative
	}
	return DeferredFormList
}

// ContextWindowTokens returns the model's context window in tokens as
// configured, or 0 when it is not. The gateway keeps no table of windows.
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

// The wire protocols a Config can name. An empty API means APIChatCompletions.
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

// Stream sends the request and returns the reply as it arrives. A refused
// request is an *Error; a stream cut after its headers is sent again within
// MaxRetries (see restart.go). Closing the stream, or canceling ctx, ends the
// connection.
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
	return c.streamChat(ctx, request)
}
