// Package gateway provides provider streaming Model adapters for Gotato: the
// OpenAI-compatible Chat Completions and Responses APIs and the Anthropic
// Messages API.
//
// It builds on the provider-neutral gotato Model contract and net/http, and
// reads its configuration file with yaml.v3. Provider authentication, request
// encoding, retries, SSE decoding, and provider error classification live here,
// outside Core.
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
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
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
	// empty), APIResponses, or APIAnthropicMessages. The aliases
	// openai-completions, openai-codex-responses, and anthropic are also
	// accepted.
	API string
	// Endpoint is the complete provider URL. When empty, BaseURL is used.
	Endpoint string
	// BaseURL may be an origin or an API base URL.
	BaseURL string
	APIKey  string
	Model   string
	Auth    AuthConfig

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
}

type Client struct {
	api            string
	endpoint       string
	apiKey         string
	model          string
	httpClient     *http.Client
	headers        map[string]string
	cacheKeyHeader string
	cacheKeyField  string
	maxRetries     int
	retryBackoff   time.Duration
}

func New(config Config) (*Client, error) {
	api, err := normalizeAPI(config.API)
	if err != nil {
		return nil, err
	}
	if config.Auth.Type != "" && config.Auth.Type != "api_key" {
		return nil, fmt.Errorf("gateway: unsupported auth type %q: the gateway authenticates with api_key only", config.Auth.Type)
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
	}, nil
}

// Supported wire protocols. Legacy spellings are accepted as aliases.
const (
	APIChatCompletions = "openai-chat-completions"
	APIResponses       = "openai-responses"
)

func normalizeAPI(api string) (string, error) {
	switch strings.TrimSpace(api) {
	case "", APIChatCompletions, "openai-completions":
		return APIChatCompletions, nil
	case APIResponses, "openai-codex-responses":
		return APIResponses, nil
	case APIAnthropicMessages, "anthropic":
		return APIAnthropicMessages, nil
	default:
		return "", fmt.Errorf("gateway: unsupported API %q (use %s, %s, or %s)", api, APIChatCompletions, APIResponses, APIAnthropicMessages)
	}
}

func NewFromEnv() (*Client, error) {
	base := firstEnv("GOTATO_GATEWAY_BASE_URL", "OPENAI_BASE_URL")
	endpoint := firstEnv("GOTATO_GATEWAY_ENDPOINT", "")
	return New(Config{
		API:      firstEnv("GOTATO_GATEWAY_API", ""),
		Endpoint: endpoint,
		BaseURL:  base,
		APIKey:   firstEnv("GOTATO_GATEWAY_API_KEY", "OPENAI_API_KEY"),
		Model:    firstEnv("GOTATO_GATEWAY_MODEL", "OPENAI_MODEL"),
	})
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
	body, names, err := encodeRequest(c.model, request)
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
		calls:    make(map[int]*callBuffer),
		restart:  restarter{client: c, body: body, open: open},
	}, nil
}

type Error struct {
	StatusCode int
	Retryable  bool
	Message    string
}

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
				if err := wait(ctx, withJitter(c.retryBackoff*time.Duration(attempt+1))); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &Error{Retryable: true, Message: err.Error()}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		message := readErrorBody(response.Body)
		_ = response.Body.Close()
		retryable := retryableStatus(response.StatusCode, message)
		if retryable && attempt < c.maxRetries {
			if err := wait(ctx, c.retryDelay(response.Header, attempt)); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &Error{StatusCode: response.StatusCode, Retryable: retryable, Message: message}
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

func encodeRequest(model string, request gotato.ModelRequest) ([]byte, map[string]string, error) {
	names, err := functionNames(request)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]wireMessage, 0, len(request.Messages)+1)
	if request.SystemInstructions != "" {
		messages = append(messages, wireMessage{Role: "system", Content: request.SystemInstructions})
	}
	for _, message := range request.Messages {
		converted, err := convertMessage(message, names)
		if err != nil {
			return nil, nil, err
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
	if request.Options.Temperature != nil || request.Options.MaxTokens != 0 {
		payload.Temperature = request.Options.Temperature
		if request.Options.MaxTokens != 0 {
			payload.MaxTokens = request.Options.MaxTokens
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode request: %w", err)
	}
	return body, names, nil
}

func convertMessage(message gotato.Message, names map[string]string) (wireMessage, error) {
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
	if role != string(gotato.RoleUser) && role != string(gotato.RoleAssistant) {
		return wireMessage{}, fmt.Errorf("gateway: unsupported Message role %q", role)
	}
	for _, part := range message.Parts {
		if part.Kind != gotato.ContentText && part.Kind != gotato.ContentReasoning && (part.Text != "" || len(part.Data) > 0) {
			return wireMessage{}, fmt.Errorf("gateway: unsupported content kind %q", part.Kind)
		}
	}
	out := wireMessage{Role: role, Content: gotato.TextOf(message)}
	if role == string(gotato.RoleAssistant) {
		out.ReasoningContent = gotato.ReasoningOf(message)
	}
	for _, call := range message.ToolCalls {
		name, err := assignFunctionName(names, call.ToolID)
		if err != nil {
			return wireMessage{}, err
		}
		out.ToolCalls = append(out.ToolCalls, wireToolCall{ID: string(call.ID), Type: "function", Function: wireFunctionCall{Name: name, Arguments: string(replayArguments(call.Arguments))}})
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
	if trimmed := bytes.TrimSpace(arguments); len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
		return arguments
	}
	return []byte(`{}`)
}

// toolResultText renders a Tool Result Message as text for a provider. Every
// text-bearing Part kind counts, because a typed Tool returns its output as a
// ContentJSON Part; gotato.TextOf would drop it.
func toolResultText(message gotato.Message) string {
	var b strings.Builder
	for _, part := range message.Parts {
		switch part.Kind {
		case gotato.ContentText, gotato.ContentReasoning, gotato.ContentJSON:
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
}
type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type stream struct {
	response  *http.Response
	reader    *bufio.Reader
	nameMap   map[string]string
	calls     map[int]*callBuffer
	queue     []gotato.ModelEvent
	finish    gotato.StopReason
	usage     gotato.Usage
	finished  bool
	restart   restarter
	closeOnce sync.Once
}

type callBuffer struct{ id, name, arguments string }

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
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
	s.calls = make(map[int]*callBuffer)
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
	if chunk.Usage != nil {
		s.usage = gotato.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		cached := chunk.Usage.PromptCacheHitTokens
		if d := chunk.Usage.PromptTokensDetails; d != nil && d.CachedTokens > cached {
			cached = d.CachedTokens
		}
		s.usage.CacheReadTokens = min(cached, s.usage.InputTokens)
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelUsage, Usage: s.usage})
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.ReasoningContent != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDelta, Text: choice.Delta.ReasoningContent})
		}
		if choice.Delta.Content != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: choice.Delta.Content})
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := s.calls[delta.Index]
			if call == nil {
				call = &callBuffer{}
				s.calls[delta.Index] = call
			}
			if delta.ID != "" {
				call.id = delta.ID
			}
			if delta.Function.Name != "" {
				call.name += delta.Function.Name
			}
			call.arguments += delta.Function.Arguments
		}
		if choice.FinishReason != nil {
			s.finish = mapStopReason(*choice.FinishReason)
		}
	}
}

func (s *stream) finishStream() {
	indices := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := s.calls[index]
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
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelToolCall, ToolCall: &gotato.ToolCall{ID: gotato.ToolCallID(call.id), ToolID: name, Arguments: []byte(arguments)}})
	}
	if s.finish == "" {
		s.finish = gotato.StopEndTurn
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: s.finish, Usage: s.usage})
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
		return gotato.StopToolCalls
	case "length":
		return gotato.StopMaxTokens
	case "content_filter":
		return gotato.StopError
	case "stop", "":
		return gotato.StopEndTurn
	default:
		return gotato.StopEndTurn
	}
}

func readErrorBody(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, 64<<10))
	if len(data) == 0 {
		return "empty response"
	}
	var structured struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &structured) == nil && structured.Error.Message != "" {
		return structured.Error.Message
	}
	message := strings.TrimSpace(string(data))
	if len(message) > 512 {
		message = message[:512]
	}
	return message
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

func firstEnv(names ...string) string {
	for _, name := range names {
		if name == "" {
			continue
		}
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
