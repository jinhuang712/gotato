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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
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

type Error struct {
	StatusCode int
	retryable  bool
	Message    string
	// Overflow is set when the provider refused the request, with a 400 or
	// 413, because it was longer than the model's context window.
	Overflow bool
}

// ContextOverflow reports whether the request was refused for being too long.
// The runtime reads it by this method, so it imports nothing from here, and
// ends the Run with the code context_overflow instead of model_failure.
func (e *Error) ContextOverflow() bool { return e != nil && e.Overflow }

// ContextLimit returns the limit, in tokens, that a refusal as too long names
// in its message, such as the "200000" of "prompt is too long: 210000 tokens >
// 200000 maximum", or 0 when it names none. Compaction reads it, by this
// method, to size its merge of the oldest Segments.
func (e *Error) ContextLimit() int {
	if e == nil || !e.Overflow {
		return 0
	}
	return limitIn(e.Message)
}

// Retryable reports whether the same request may succeed when sent again
// (a rate limit, an overload, a broken stream). The runtime reads it by this
// method, and sets Retryable on the Run's model_failure.
func (e *Error) Retryable() bool { return e != nil && e.retryable }

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.StatusCode == 0 {
		return "gateway: " + e.Message
	}
	return fmt.Sprintf("gateway: HTTP %d: %s", e.StatusCode, e.Message)
}

func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		// Configurable and per-request headers are applied first so protocol
		// headers from this adapter win; neither may disable authentication
		// or break SSE negotiation.
		c.applyHeaders(ctx, req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.api == APIAnthropicMessages {
			// A configured anthropic-version header pins another API version.
			if req.Header.Get("anthropic-version") == "" {
				req.Header.Set("anthropic-version", anthropicVersion)
			}
			if c.rules.blockBinding {
				addAnthropicBeta(req.Header, anthropicBindingBeta)
			}
			if c.apiKey != "" {
				req.Header.Set("x-api-key", c.apiKey)
			}
		} else if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		response, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < c.maxRetries {
				delay := withJitter(c.retryBackoff * time.Duration(attempt+1))
				// The error text can name hosts, and Events reach clients.
				gotato.ReportModelRetry(ctx, gotato.ModelRetry{Attempt: attempt + 1, Wait: delay, Error: "the request could not be sent"})
				if err := wait(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &Error{retryable: true, Message: err.Error()}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		body := readErrorBody(response.Body)
		message := body.Message
		_ = response.Body.Close()
		retryable := retryableStatus(response.StatusCode, message)
		if retryable && attempt < c.maxRetries {
			delay := c.retryDelay(response.Header, attempt)
			gotato.ReportModelRetry(ctx, gotato.ModelRetry{Attempt: attempt + 1, Wait: delay, Error: fmt.Sprintf("the provider answered %d", response.StatusCode)})
			if err := wait(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		return nil, c.overflow.httpError(response.StatusCode, retryable, body)
	}
}

// retryDelay returns how long to wait before the next attempt. A provider
// Retry-After/retry-after-ms header wins; otherwise the configured backoff
// grows linearly with the attempt and carries jitter.
func (c *Client) retryDelay(header http.Header, attempt int) time.Duration {
	if retryAfter := retryAfterDuration(header); retryAfter >= 0 {
		return retryAfter
	}
	return withJitter(c.retryBackoff * time.Duration(attempt+1))
}

// withJitter adds up to 25% to a backoff so a fleet recovering from the same
// outage does not retry in lockstep. The delay never shrinks below the base.
func withJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	span := delay / 4
	if span <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int63n(int64(span)+1))
}

// retryableStatus is the shared retry policy for both wire protocols. A 429
// that names a terminal quota is not retryable; transient 429/5xx responses
// and known overload phrasing are.
func retryableStatus(status int, message string) bool {
	if status == http.StatusTooManyRequests && terminalQuotaLimit(message) {
		return false
	}
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || status == statusOverloaded || strings.Contains(strings.ToLower(message), "rate limit") ||
		strings.Contains(strings.ToLower(message), "overloaded") || strings.Contains(strings.ToLower(message), "service unavailable")
}

// terminalQuotaLimit reports whether a 429 message names a quota that
// retrying cannot fix (an exhausted balance or usage cap) rather than a
// transient rate limit.
func terminalQuotaLimit(message string) bool {
	lower := strings.ToLower(message)
	for _, phrase := range []string{"usage limit", "freeusagelimiterror", "gousagelimiterror", "insufficient_quota", "out of budget", "quota exceeded", "available balance"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func retryAfterDuration(header http.Header) time.Duration {
	if value := strings.TrimSpace(header.Get("retry-after-ms")); value != "" {
		if millis, err := time.ParseDuration(value + "ms"); err == nil && millis >= 0 {
			return millis
		}
	}
	value := strings.TrimSpace(header.Get("retry-after"))
	if value == "" {
		return -1
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds >= 0 {
		return seconds
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
		return 0
	}
	return -1
}

func encodeRequest(model string, request gotato.ModelRequest, rules providerRules) ([]byte, map[string]string, error) {
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]wireMessage, 0, len(request.Messages)+1)
	if request.SystemInstructions != "" {
		messages = append(messages, wireMessage{Role: "system", Content: request.SystemInstructions})
	}
	start := roundStart(request.Messages)
	for i, message := range request.Messages {
		keep := rules.keepsReasoning(i, start)
		converted, err := convertMessage(message, names, keep)
		if err != nil {
			return nil, nil, err
		}
		// A reply that was only reasoning has nothing left to send.
		if !keep && converted.Role == "assistant" && converted.Content == "" && len(converted.ToolCalls) == 0 && gotato.ReasoningOf(message) != "" {
			continue
		}
		messages = append(messages, converted)
	}
	tools := make([]wireTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		name, err := assignFunctionName(names, spec.ID)
		if err != nil {
			return nil, nil, err
		}
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = []byte(`{}`)
		}
		if !json.Valid(parameters) {
			return nil, nil, fmt.Errorf("gateway: Tool %q has invalid InputSchema", spec.ID)
		}
		tools = append(tools, wireTool{Type: "function", Function: wireFunction{Name: name, Description: spec.Description, Parameters: json.RawMessage(parameters)}})
	}
	payload := wireRequest{Model: model, Messages: messages, Tools: tools, Stream: true, StreamOptions: &wireStreamOptions{IncludeUsage: true}}
	if request.Config.Temperature != nil || request.Config.MaxTokens != 0 {
		payload.Temperature = request.Config.Temperature
		if request.Config.MaxTokens != 0 {
			payload.MaxTokens = request.Config.MaxTokens
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode request: %w", err)
	}
	return body, names, nil
}

func convertMessage(message gotato.Message, names map[string]string, keepReasoning bool) (wireMessage, error) {
	role := string(message.Role)
	if role == string(gotato.RoleToolResult) {
		content := toolResultText(message)
		if content == "" && message.ToolResult != nil {
			content = message.ToolResult.SafeError
		}
		if content == "" {
			content = "(no tool output)"
		}
		callID := ""
		if message.ToolResult != nil {
			callID = string(message.ToolResult.CallID)
		}
		return wireMessage{Role: "tool", Content: content, ToolCallID: callID}, nil
	}
	if message.Role == gotato.RoleNotice {
		// Chat Completions has no system message mid-conversation: a notice
		// (G-F29) goes as a user message on the wire, tagged text, and the
		// Record keeps it a notice.
		role = string(gotato.RoleUser)
	}
	if role != string(gotato.RoleUser) && role != string(gotato.RoleAssistant) {
		return wireMessage{}, fmt.Errorf("gateway: unsupported Message role %q", role)
	}
	for _, part := range message.Parts {
		if part.Kind != gotato.ContentKindText && part.Kind != gotato.ContentKindReasoning && (part.Text != "" || len(part.Data) > 0) {
			return wireMessage{}, fmt.Errorf("gateway: unsupported content kind %q", part.Kind)
		}
	}
	out := wireMessage{Role: role, Content: gotato.TextOf(message)}
	if role == string(gotato.RoleAssistant) && keepReasoning {
		out.ReasoningContent = gotato.ReasoningOf(message)
	}
	for _, call := range message.ToolCalls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return wireMessage{}, err
		}
		wire := wireToolCall{ID: string(call.ID), Type: "function", Function: wireFunctionCall{Name: name, Arguments: string(replayArguments(call.Arguments))}}
		// The call's extra_content goes back as it came. Another wire API's
		// artifact, such as a Responses item ID, is not an object.
		if isJSONObject(call.Signature) {
			wire.ExtraContent = json.RawMessage(call.Signature)
		}
		out.ToolCalls = append(out.ToolCalls, wire)
	}
	return out, nil
}

// replayArguments returns a past Tool Call's arguments in a form every wire
// API accepts back: the arguments themselves when they are a JSON object, and
// {} otherwise. A call whose arguments are not an object (most often one the
// output limit cut off) never ran; the failed Tool Result after it tells the
// model why. Sending the broken text back would make the provider refuse the
// whole request, and so end the Run the Loop meant to continue.
func replayArguments(arguments []byte) []byte {
	if isJSONObject(arguments) {
		return arguments
	}
	return []byte(`{}`)
}

// isJSONObject reports whether data is one JSON object.
func isJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// toolResultText renders a Tool Result Message as text for a provider. Every
// text-bearing Part kind counts, because a typed Tool returns its output as a
// ContentKindJSON Part; gotato.TextOf would drop it.
func toolResultText(message gotato.Message) string {
	var b strings.Builder
	for _, part := range message.Parts {
		switch part.Kind {
		case gotato.ContentKindText, gotato.ContentKindReasoning, gotato.ContentKindJSON:
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

type wireRequest struct {
	Model         string             `json:"model"`
	Messages      []wireMessage      `json:"messages"`
	Tools         []wireTool         `json:"tools,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *wireStreamOptions `json:"stream_options,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	MaxTokens     uint32             `json:"max_tokens,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}
type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// ReasoningContent is the model's thinking (DeepSeek), streamed as
	// delta.reasoning_content; a thinking model requires it back.
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}
type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
	// ExtraContent is provider data on a call, kept as it came: Gemini puts
	// the thought signature it requires back here (google.thought_signature).
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}
type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type stream struct {
	response *http.Response
	reader   *bufio.Reader
	nameMap  map[string]string
	// calls are the Tool calls in the order they first appeared; byIndex
	// is the call each provider index names now, and last the call the
	// latest delta added to.
	calls     []*callBuffer
	byIndex   map[int]*callBuffer
	last      *callBuffer
	queue     []gotato.ModelEvent
	finish    gotato.StopReason
	usage     gotato.Usage
	model     string
	finished  bool
	restart   restarter
	overflow  overflowMatcher
	closeOnce sync.Once
}

type callBuffer struct {
	id, name, arguments string
	// extra is the call's extra_content, from whichever delta carried it.
	extra json.RawMessage
}

type wireChunk struct {
	// Model is the model that answered, which a gateway with fallbacks may
	// have chosen over the requested one.
	Model string `json:"model"`
	// Error is an error the provider sent in the middle of the stream, as
	// {"error": {...}} in place of a chunk.
	Error *struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
	} `json:"error"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
				ExtraContent json.RawMessage `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        uint64 `json:"prompt_tokens"`
		CompletionTokens    uint64 `json:"completion_tokens"`
		TotalTokens         uint64 `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens uint64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details,omitempty"`
		// PromptCacheHitTokens is DeepSeek's name for cached prompt tokens.
		PromptCacheHitTokens uint64 `json:"prompt_cache_hit_tokens,omitempty"`
	} `json:"usage"`
}

func (s *stream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.finished {
			return gotato.ModelEvent{}, io.EOF
		}
		data, err := s.nextData(ctx)
		if errors.Is(err, io.EOF) && s.finish != "" {
			// A provider that omits [DONE] after a finish_reason still
			// completed the response.
			s.finishStream()
			continue
		}
		if err != nil {
			response, rerr := s.restart.recover(ctx, err)
			if rerr != nil {
				return gotato.ModelEvent{}, rerr
			}
			s.reopen(response)
			continue
		}
		if data == "[DONE]" {
			s.finishStream()
			continue
		}
		if data == "" {
			continue
		}
		var chunk wireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return gotato.ModelEvent{}, fmt.Errorf("gateway: invalid SSE chunk: %w", err)
		}
		if chunk.Error != nil {
			// An error is not an interruption: it is not sent again as a
			// cut stream is, and an overflow is reported as one.
			s.finished = true
			message := chunk.Error.Message
			if message == "" {
				message = chunk.Error.Type
			}
			return gotato.ModelEvent{}, s.overflow.streamError("Chat Completions stream error", message, errorCode(chunk.Error.Code), chunk.Error.Type)
		}
		s.processChunk(chunk)
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	s.restart.delivered = true
	return event, nil
}

// reopen continues on a re-issued response, discarding everything decoded
// from the interrupted one (nothing of it reached the caller).
func (s *stream) reopen(response *http.Response) {
	if s.response != nil && s.response.Body != nil {
		_ = s.response.Body.Close()
	}
	s.response = response
	s.reader = bufio.NewReader(response.Body)
	s.calls = nil
	s.byIndex = make(map[int]*callBuffer)
	s.last = nil
	s.queue = nil
	s.finish = ""
	s.usage = gotato.Usage{}
}

// SSE framing limits. They bound what one upstream line or event can allocate,
// so a misbehaving provider cannot grow the stream buffer without limit.
const (
	maxSSELineBytes  = 1 << 20
	maxSSEEventBytes = 4 << 20
)

// readBoundedLine reads one '\n'-terminated line without letting a line that
// never terminates grow the buffer without bound.
func readBoundedLine(reader *bufio.Reader) (string, error) {
	var buf []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return string(buf), err
		}
		if b == '\n' {
			return string(buf), nil
		}
		buf = append(buf, b)
		if len(buf) > maxSSELineBytes {
			return "", fmt.Errorf("gateway: SSE line exceeds %d bytes", maxSSELineBytes)
		}
	}
}

// readSSEEvent joins the data: lines of one SSE event, enforcing the line and
// event size limits.
func readSSEEvent(ctx context.Context, reader *bufio.Reader) (string, error) {
	var lines []string
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		raw, err := readBoundedLine(reader)
		line := strings.TrimSuffix(raw, "\r")
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			payload := strings.TrimSpace(after)
			total += len(payload)
			if total > maxSSEEventBytes {
				return "", fmt.Errorf("gateway: SSE event exceeds %d bytes", maxSSEEventBytes)
			}
			lines = append(lines, payload)
		}
		if line == "" && len(lines) > 0 {
			return strings.Join(lines, "\n"), nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(lines) > 0 {
				return strings.Join(lines, "\n"), nil
			}
			return "", err
		}
	}
}

func (s *stream) nextData(ctx context.Context) (string, error) {
	return readSSEEvent(ctx, s.reader)
}

func (s *stream) processChunk(chunk wireChunk) {
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		s.usage = gotato.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		cached := chunk.Usage.PromptCacheHitTokens
		if d := chunk.Usage.PromptTokensDetails; d != nil && d.CachedTokens > cached {
			cached = d.CachedTokens
		}
		s.usage.CacheReadTokens = min(cached, s.usage.InputTokens)
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindUsage, Usage: s.usage})
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.ReasoningContent != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindReasoningDelta, Text: choice.Delta.ReasoningContent})
		}
		if choice.Delta.Content != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: choice.Delta.Content})
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := s.callFor(delta.Index, delta.ID)
			if delta.ID != "" {
				call.id = delta.ID
			}
			if delta.Function.Name != "" {
				call.name += delta.Function.Name
			}
			call.arguments += delta.Function.Arguments
			if isJSONObject(delta.ExtraContent) {
				call.extra = delta.ExtraContent
			}
		}
		if choice.FinishReason != nil {
			s.finish = mapStopReason(*choice.FinishReason)
		}
	}
}

// callFor returns the Tool call a delta adds to. The chat API names each
// call by its index, and sends its ID once, in its first delta. Gemini's chat
// API sends parallel calls whole, each with its own ID, but without an
// index, with a null one, or all at index 0. So a delta that names an ID
// other than the one its index holds starts a new call, and a delta without
// an index adds to the last call.
func (s *stream) callFor(index *int, id string) *callBuffer {
	call := s.last
	if index != nil {
		call = s.byIndex[*index]
	}
	if call == nil || (id != "" && call.id != "" && id != call.id) {
		call = &callBuffer{}
		s.calls = append(s.calls, call)
		if index != nil {
			s.byIndex[*index] = call
		}
	}
	s.last = call
	return call
}

func (s *stream) finishStream() {
	for _, call := range s.calls {
		name := call.name
		if original, ok := s.nameMap[name]; ok {
			name = original
		}
		// A provider may stream a call that takes no arguments with empty
		// arguments. Core accepts only JSON, so the call would fail as invalid
		// arguments; the Responses and Anthropic adapters send {} too.
		arguments := call.arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindToolCall, ToolCall: &gotato.ToolCall{ID: gotato.ToolCallID(call.id), ToolID: name, Arguments: []byte(arguments), Signature: call.extra}})
	}
	if s.finish == "" {
		s.finish = gotato.StopReasonEndTurn
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelEventKindDone, StopReason: s.finish, Usage: s.usage, Model: s.model})
	s.finished = true
}

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	return nil
}

func mapStopReason(reason string) gotato.StopReason {
	switch reason {
	case "tool_calls", "function_call":
		return gotato.StopReasonToolCalls
	case "length":
		return gotato.StopReasonMaxTokens
	case "content_filter":
		return gotato.StopReasonError
	case "stop", "":
		return gotato.StopReasonEndTurn
	default:
		return gotato.StopReasonEndTurn
	}
}

// errorBody is what a refused request's body says: the message to show, and
// the code and type the provider named, either of which may carry the reason
// alone.
type errorBody struct {
	Message, Code, Type string
}

func readErrorBody(body io.Reader) errorBody {
	data, _ := io.ReadAll(io.LimitReader(body, 64<<10))
	if len(data) == 0 {
		return errorBody{Message: "empty response"}
	}
	var structured struct {
		Error struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
		} `json:"error"`
	}
	out := errorBody{}
	if json.Unmarshal(data, &structured) == nil {
		out = errorBody{Message: structured.Error.Message, Code: errorCode(structured.Error.Code), Type: structured.Error.Type}
	}
	if out.Message == "" {
		out.Message = strings.TrimSpace(string(data))
		if len(out.Message) > 512 {
			out.Message = out.Message[:512]
		}
	}
	return out
}

// errorCode reads an error's code, which providers write as a string, as a
// number or as null.
func errorCode(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}
