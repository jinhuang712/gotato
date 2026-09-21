// Package gateway provides provider streaming Model adapters for Gotato.
//
// It deliberately depends only on the provider-neutral gotato Model contract and
// net/http. Provider authentication, request encoding, retries, SSE decoding, and
// provider error classification stay inside this package rather than Core.
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

type Config struct {
	// API selects the wire protocol. Empty means openai-completions for
	// backwards compatibility. Supported values include
	// openai-completions and openai-codex-responses.
	API string
	// Endpoint is the complete provider URL. When empty, BaseURL is used.
	Endpoint string
	// BaseURL may be an origin or an API base URL.
	BaseURL string
	APIKey  string
	Model   string
	Auth    AuthConfig

	HTTPClient   *http.Client
	Headers      map[string]string
	MaxRetries   int
	RetryBackoff time.Duration
	// NoRetries disables retries entirely. MaxRetries counts retries after
	// the first attempt; 0 means the default.
	NoRetries bool
}

type Client struct {
	api          string
	endpoint     string
	apiKey       string
	model        string
	httpClient   *http.Client
	headers      map[string]string
	maxRetries   int
	retryBackoff time.Duration
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
		if base == "" {
			return nil, fmt.Errorf("gateway: BaseURL or Endpoint is required")
		}
		path := "/chat/completions"
		if api == APIResponses {
			path = "/responses"
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
		api:          api,
		endpoint:     endpoint,
		apiKey:       config.APIKey,
		model:        config.Model,
		httpClient:   client,
		headers:      cloneHeaders(config.Headers),
		maxRetries:   maxRetries,
		retryBackoff: backoff,
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
	default:
		return "", fmt.Errorf("gateway: unsupported API %q (use %s or %s)", api, APIChatCompletions, APIResponses)
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
	if c.api == APIResponses {
		return c.streamResponses(ctx, request)
	}
	body, names, err := encodeRequest(c.model, request)
	if err != nil {
		return nil, err
	}
	response, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	return &stream{
		response: response,
		reader:   bufio.NewReader(response.Body),
		nameMap:  names,
		calls:    make(map[int]*callBuffer),
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
		// Configurable headers are applied first so protocol headers from this
		// adapter win; a config entry must not be able to disable
		// authentication or break SSE negotiation.
		for key, value := range c.headers {
			req.Header.Set(key, value)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		response, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < c.maxRetries {
				if err := wait(ctx, c.retryBackoff*time.Duration(attempt+1)); err != nil {
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
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		if retryable && attempt < c.maxRetries {
			if err := wait(ctx, c.retryBackoff*time.Duration(attempt+1)); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &Error{StatusCode: response.StatusCode, Retryable: retryable, Message: message}
	}
}

func encodeRequest(model string, request gotato.ModelRequest) ([]byte, map[string]string, error) {
	names := make(map[string]string, len(request.Tools))
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
		name := gatewayFunctionName(spec.ID)
		if previous, exists := names[name]; exists && previous != spec.ID {
			return nil, nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, spec.ID)
		}
		names[name] = spec.ID
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
	for _, call := range message.ToolCalls {
		name := gatewayFunctionName(call.ToolID)
		if previous, exists := names[name]; exists && previous != call.ToolID {
			return wireMessage{}, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, call.ToolID)
		}
		names[name] = call.ToolID
		out.ToolCalls = append(out.ToolCalls, wireToolCall{ID: string(call.ID), Type: "function", Function: wireFunctionCall{Name: name, Arguments: string(call.Arguments)}})
	}
	return out, nil
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

func gatewayFunctionName(id string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(id))
	name := "gotato_" + encoded
	if len(name) <= 64 {
		return name
	}
	hash := sha256.Sum256([]byte(id))
	return "gotato_" + hex.EncodeToString(hash[:])[:56]
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
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
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
	closeOnce sync.Once
}

type callBuffer struct{ id, name, arguments string }

type wireChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
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
		PromptTokens     uint64 `json:"prompt_tokens"`
		CompletionTokens uint64 `json:"completion_tokens"`
		TotalTokens      uint64 `json:"total_tokens"`
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
		if err != nil {
			return gotato.ModelEvent{}, err
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
	return event, nil
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
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
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
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelUsage, Usage: s.usage})
	}
	for _, choice := range chunk.Choices {
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
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelToolCall, ToolCall: &gotato.ToolCall{ID: gotato.ToolCallID(call.id), ToolID: name, Arguments: []byte(call.arguments)}})
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
