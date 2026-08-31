package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

const (
	defaultCodexBaseURL = "https://chatgpt.com/backend-api"
	codexOAuthTokenURL  = "https://auth.openai.com/oauth/token"
	codexOAuthClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
)

func (c *Client) streamCodex(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	body, names, err := encodeCodexRequest(c.model, request)
	if err != nil {
		return nil, err
	}
	response, err := c.doCodex(ctx, body)
	if err != nil {
		return nil, err
	}
	return &codexStream{
		response: response,
		reader:   bufio.NewReader(response.Body),
		nameMap:  names,
		calls:    make(map[int]*codexCall),
		texts:    make(map[int]bool),
	}, nil
}

func (c *Client) doCodex(ctx context.Context, body []byte) (*http.Response, error) {
	token, accountID, err := c.codexCredentials(ctx)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for key, value := range c.headers {
			req.Header.Set(key, value)
		}
		// These headers are part of the Codex protocol and cannot be replaced
		// by a generic provider header override.
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("chatgpt-account-id", accountID)
		req.Header.Set("originator", "pi")
		req.Header.Set("User-Agent", "gotato-agent/0.1")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		response, requestErr := c.httpClient.Do(req)
		if requestErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < c.maxRetries {
				if err := wait(ctx, c.retryBackoff*time.Duration(attempt+1)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &Error{Retryable: true, Message: requestErr.Error()}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		message := readErrorBody(response.Body)
		_ = response.Body.Close()
		retryable := codexRetryable(response.StatusCode, message)
		if retryable && attempt < c.maxRetries {
			delay := c.retryBackoff * time.Duration(attempt+1)
			if retryAfter := retryAfterDuration(response.Header); retryAfter >= 0 {
				delay = retryAfter
			}
			if err := wait(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &Error{StatusCode: response.StatusCode, Retryable: retryable, Message: message}
	}
}

func codexRetryable(status int, message string) bool {
	if status == http.StatusTooManyRequests && regexpTerminalCodexLimit(message) {
		return false
	}
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || strings.Contains(strings.ToLower(message), "rate limit") ||
		strings.Contains(strings.ToLower(message), "overloaded") || strings.Contains(strings.ToLower(message), "service unavailable")
}

func regexpTerminalCodexLimit(message string) bool {
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

// piCredential is the subset of Pi's auth.json credential format needed by
// the Codex adapter. The access token is never included in errors or logs.
type piCredential struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId,omitempty"`
}

type piAuthFile map[string]json.RawMessage

func (c *Client) codexCredentials(ctx context.Context) (string, string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	credential := piCredential{Type: "api_key", Access: c.apiKey}
	if c.auth.Type == "pi_oauth" {
		path := expandPath(c.auth.File)
		if path == "" {
			return "", "", fmt.Errorf("gateway: pi_oauth requires auth.file")
		}
		provider := c.auth.Provider
		if provider == "" {
			provider = "openai-codex"
		}
		if c.codexCredential != nil && c.codexCredentialPath == path && c.codexCredentialName == provider {
			credential = *c.codexCredential
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return "", "", fmt.Errorf("gateway: read Pi auth file %q: %w", path, err)
			}
			var file piAuthFile
			if err := json.Unmarshal(data, &file); err != nil {
				return "", "", fmt.Errorf("gateway: parse Pi auth file %q: %w", path, err)
			}
			raw, ok := file[provider]
			if !ok {
				return "", "", fmt.Errorf("gateway: Pi auth file has no provider %q", provider)
			}
			if err := json.Unmarshal(raw, &credential); err != nil {
				return "", "", fmt.Errorf("gateway: parse Pi credential %q: %w", provider, err)
			}
			if credential.Type != "" && credential.Type != "oauth" {
				return "", "", fmt.Errorf("gateway: Pi provider %q is not OAuth", provider)
			}
			c.codexCredentialPath = path
			c.codexCredentialName = provider
		}
		if credential.Access == "" {
			return "", "", fmt.Errorf("gateway: Pi provider %q has no access token", provider)
		}
		if tokenExpired(credential.Expires) {
			if credential.Refresh == "" {
				return "", "", fmt.Errorf("gateway: Pi provider %q access token is expired and has no refresh token", provider)
			}
			refreshed, err := refreshCodexToken(ctx, credential.Refresh, c.httpClient)
			if err != nil {
				return "", "", err
			}
			credential = refreshed
			// Keep the refreshed token in the local file when possible. Failure to
			// persist does not make this request fail; the in-memory token remains
			// valid for the lifetime of this Client.
			_ = persistPiCredential(path, provider, credential)
		}
		cached := credential
		c.codexCredential = &cached
	}
	if credential.Access == "" {
		return "", "", fmt.Errorf("gateway: Codex access token is required")
	}
	accountID := strings.TrimSpace(c.auth.AccountID)
	if accountID == "" {
		accountID = credential.AccountID
	}
	if accountID == "" {
		var err error
		accountID, err = codexAccountID(credential.Access)
		if err != nil {
			return "", "", err
		}
	}
	return credential.Access, accountID, nil
}

func tokenExpired(expires int64) bool {
	if expires == 0 {
		return false
	}
	// Pi stores milliseconds since epoch. Accept seconds as a convenience for
	// hand-written credential files.
	if expires < 100000000000 {
		expires *= 1000
	}
	return time.Now().Add(30*time.Second).UnixMilli() >= expires
}

func refreshCodexToken(ctx context.Context, refresh string, client *http.Client) (piCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	form.Set("client_id", codexOAuthClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return piCredential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return piCredential{}, ctx.Err()
		}
		return piCredential{}, fmt.Errorf("gateway: Codex token refresh: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return piCredential{}, fmt.Errorf("gateway: Codex token refresh failed (HTTP %d): %s", response.StatusCode, readErrorBody(response.Body))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return piCredential{}, fmt.Errorf("gateway: parse Codex token refresh: %w", err)
	}
	if payload.AccessToken == "" || payload.RefreshToken == "" || payload.ExpiresIn <= 0 {
		return piCredential{}, fmt.Errorf("gateway: Codex token refresh response is missing required fields")
	}
	return piCredential{
		Type:    "oauth",
		Access:  payload.AccessToken,
		Refresh: payload.RefreshToken,
		Expires: time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UnixMilli(),
	}, nil
}

func persistPiCredential(path, provider string, credential piCredential) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var file piAuthFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	file[provider] = encoded
	updated, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".gotato-auth-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(updated, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return os.ExpandEnv(path)
}

func codexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("gateway: Codex access token is not a JWT and has no account ID")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", fmt.Errorf("gateway: decode Codex access token: %w", err)
	}
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("gateway: decode Codex access token claims: %w", err)
	}
	if claims.Auth.AccountID == "" {
		return "", fmt.Errorf("gateway: Codex access token has no chatgpt account ID")
	}
	return claims.Auth.AccountID, nil
}

type codexRequest struct {
	Model             string          `json:"model"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
	Instructions      string          `json:"instructions"`
	Input             []any           `json:"input"`
	Text              codexText       `json:"text"`
	Include           []string        `json:"include"`
	ToolChoice        string          `json:"tool_choice"`
	ParallelToolCalls bool            `json:"parallel_tool_calls"`
	Tools             []codexTool     `json:"tools,omitempty"`
	Reasoning         *codexReasoning `json:"reasoning,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	MaxOutputTokens   uint32          `json:"max_output_tokens,omitempty"`
}

type codexText struct {
	Verbosity string `json:"verbosity"`
}

type codexReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

type codexTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

type codexMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content,omitempty"`
}

type codexOutputMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []any  `json:"content"`
	Status  string `json:"status"`
}

type codexFunctionCall struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type codexFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func encodeCodexRequest(model string, request gotato.ModelRequest) ([]byte, map[string]string, error) {
	names := make(map[string]string, len(request.Tools))
	input := make([]any, 0, len(request.Messages)*2)
	for _, message := range request.Messages {
		items, err := convertCodexMessage(message, names)
		if err != nil {
			return nil, nil, err
		}
		input = append(input, items...)
	}

	tools := make([]codexTool, 0, len(request.Tools))
	for _, spec := range request.Tools {
		name := gatewayFunctionName(spec.ID)
		if previous, exists := names[name]; exists && previous != spec.ID {
			return nil, nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, spec.ID)
		}
		names[name] = spec.ID
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = []byte(`{"type":"object"}`)
		}
		if !json.Valid(parameters) {
			return nil, nil, fmt.Errorf("gateway: Tool %q has invalid InputSchema", spec.ID)
		}
		tools = append(tools, codexTool{Type: "function", Name: name, Description: spec.Description, Parameters: json.RawMessage(parameters)})
	}

	instructions := request.SystemInstructions
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	payload := codexRequest{
		Model:             model,
		Store:             false,
		Stream:            true,
		Instructions:      instructions,
		Input:             input,
		Text:              codexText{Verbosity: "low"},
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Tools:             tools,
	}
	if request.Options.Temperature != nil {
		payload.Temperature = request.Options.Temperature
	}
	if request.Options.MaxTokens != 0 {
		payload.MaxOutputTokens = request.Options.MaxTokens
	}
	if request.Options.ReasoningEffort != "" {
		summary := request.Options.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		payload.Reasoning = &codexReasoning{Effort: request.Options.ReasoningEffort, Summary: summary}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: encode Codex request: %w", err)
	}
	return body, names, nil
}

func convertCodexMessage(message gotato.Message, names map[string]string) ([]any, error) {
	role := string(message.Role)
	switch message.Role {
	case gotato.RoleUser:
		content, err := codexInputContent(message.Parts)
		if err != nil {
			return nil, err
		}
		if len(content) == 0 {
			return nil, nil
		}
		return []any{codexMessage{Role: "user", Content: content}}, nil
	case gotato.RoleAssistant:
		items := make([]any, 0, len(message.Parts)+len(message.ToolCalls))
		for _, part := range message.Parts {
			switch part.Kind {
			case gotato.ContentReasoning:
				if len(part.Signature) == 0 {
					continue
				}
				var reasoning json.RawMessage
				if err := json.Unmarshal(part.Signature, &reasoning); err != nil {
					return nil, fmt.Errorf("gateway: assistant reasoning signature is invalid: %w", err)
				}
				items = append(items, reasoning)
			case gotato.ContentText:
				if part.Text == "" {
					continue
				}
				items = append(items, codexOutputMessage{
					Type: "message", Role: "assistant", Status: "completed",
					Content: []any{map[string]any{"type": "output_text", "text": part.Text, "annotations": []any{}}},
				})
			case gotato.ContentImage, gotato.ContentJSON:
				if len(part.Data) > 0 || part.Text != "" {
					return nil, fmt.Errorf("gateway: Codex assistant content kind %q is unsupported", part.Kind)
				}
			}
		}
		for _, call := range message.ToolCalls {
			name := gatewayFunctionName(call.ToolID)
			if previous, exists := names[name]; exists && previous != call.ToolID {
				return nil, fmt.Errorf("gateway: Tool IDs collide after encoding: %q and %q", previous, call.ToolID)
			}
			names[name] = call.ToolID
			arguments := string(call.Arguments)
			if arguments == "" {
				arguments = "{}"
			}
			if !json.Valid([]byte(arguments)) {
				return nil, fmt.Errorf("gateway: Tool Call %q has invalid arguments", call.ID)
			}
			callID, itemID := splitCodexCallID(string(call.ID))
			items = append(items, codexFunctionCall{Type: "function_call", ID: itemID, CallID: callID, Name: name, Arguments: arguments})
		}
		return items, nil
	case gotato.RoleToolResult:
		callID := ""
		if message.ToolResult != nil {
			callID, _ = splitCodexCallID(string(message.ToolResult.CallID))
		}
		if callID == "" {
			return nil, fmt.Errorf("gateway: Tool result has no Call ID")
		}
		output := gotato.TextOf(message)
		if output == "" && message.ToolResult != nil {
			output = message.ToolResult.SafeError
		}
		if output == "" {
			output = "(no tool output)"
		}
		return []any{codexFunctionOutput{Type: "function_call_output", CallID: callID, Output: output}}, nil
	default:
		return nil, fmt.Errorf("gateway: unsupported Message role %q", role)
	}
}

func codexInputContent(parts []gotato.ContentPart) ([]any, error) {
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case gotato.ContentText:
			if part.Text != "" {
				content = append(content, map[string]any{"type": "input_text", "text": part.Text})
			}
		case gotato.ContentImage:
			if len(part.Data) == 0 {
				continue
			}
			mimeType := part.MIMEType
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			content = append(content, map[string]any{
				"type":      "input_image",
				"detail":    "auto",
				"image_url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data),
			})
		case gotato.ContentReasoning, gotato.ContentJSON:
			if part.Text != "" || len(part.Data) > 0 {
				return nil, fmt.Errorf("gateway: Codex user content kind %q is unsupported", part.Kind)
			}
		}
	}
	return content, nil
}

func splitCodexCallID(id string) (callID, itemID string) {
	if index := strings.IndexByte(id, '|'); index >= 0 {
		return id[:index], id[index+1:]
	}
	return id, ""
}

type codexStream struct {
	response  *http.Response
	reader    *bufio.Reader
	nameMap   map[string]string
	calls     map[int]*codexCall
	texts     map[int]bool
	queue     []gotato.ModelEvent
	finished  bool
	terminal  error
	closeOnce sync.Once
}

type codexCall struct {
	id        string
	callID    string
	name      string
	arguments string
	emitted   bool
}

type codexOutputItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	Summary          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

type codexResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Usage *struct {
		InputTokens        uint64 `json:"input_tokens"`
		OutputTokens       uint64 `json:"output_tokens"`
		TotalTokens        uint64 `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens     uint64 `json:"cached_tokens"`
			CacheWriteTokens uint64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
	Output []codexOutputItem `json:"output,omitempty"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type codexEvent struct {
	Type        string           `json:"type"`
	OutputIndex int              `json:"output_index"`
	Delta       string           `json:"delta"`
	Arguments   string           `json:"arguments"`
	Item        *codexOutputItem `json:"item,omitempty"`
	Response    *codexResponse   `json:"response,omitempty"`
	Code        string           `json:"code,omitempty"`
	Message     string           `json:"message,omitempty"`
}

func (s *codexStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.queue) == 0 {
		if s.finished {
			if s.terminal != nil {
				err := s.terminal
				s.terminal = nil
				return gotato.ModelEvent{}, err
			}
			return gotato.ModelEvent{}, io.EOF
		}
		data, err := nextSSEData(ctx, s.reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return gotato.ModelEvent{}, fmt.Errorf("gateway: Codex stream ended before completion")
			}
			return gotato.ModelEvent{}, err
		}
		if data == "" || data == "[DONE]" {
			continue
		}
		var event codexEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return gotato.ModelEvent{}, fmt.Errorf("gateway: invalid Codex SSE event: %w", err)
		}
		s.processCodexEvent(event, []byte(data))
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}

func nextSSEData(ctx context.Context, reader *bufio.Reader) (string, error) {
	var lines []string
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
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

func (s *codexStream) processCodexEvent(event codexEvent, raw []byte) {
	switch event.Type {
	case "response.output_item.added":
		if event.Item == nil {
			return
		}
		s.ensureCodexCall(event.OutputIndex, event.Item)
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta != "" {
			s.texts[event.OutputIndex] = true
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: event.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDelta, Text: event.Delta})
		}
	case "response.function_call_arguments.delta":
		call := s.calls[event.OutputIndex]
		if call == nil {
			call = &codexCall{}
			s.calls[event.OutputIndex] = call
		}
		call.arguments += event.Delta
	case "response.function_call_arguments.done":
		call := s.calls[event.OutputIndex]
		if call == nil {
			call = &codexCall{}
			s.calls[event.OutputIndex] = call
		}
		call.arguments = event.Arguments
	case "response.output_item.done":
		if event.Item == nil {
			return
		}
		itemRaw := codexItemRaw(raw)
		s.finishCodexItem(event.OutputIndex, *event.Item, itemRaw)
	case "response.completed", "response.incomplete":
		s.finishCodexResponse(event.Response)
	case "response.failed":
		message := event.Message
		if event.Response != nil && event.Response.Error != nil {
			message = event.Response.Error.Message
			if message == "" {
				message = event.Response.Error.Code
			}
		}
		if message == "" {
			message = "Codex response failed"
		}
		s.finishWithError(fmt.Errorf("gateway: Codex response failed: %s", message))
	case "error":
		message := event.Message
		if message == "" {
			message = event.Code
		}
		if message == "" {
			message = "Codex stream error"
		}
		s.finishWithError(fmt.Errorf("gateway: Codex stream error: %s", message))
	}
}

func codexItemRaw(event []byte) []byte {
	var envelope struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil || len(envelope.Item) == 0 {
		return nil
	}
	return append([]byte(nil), envelope.Item...)
}

func (s *codexStream) ensureCodexCall(index int, item *codexOutputItem) {
	if item.Type != "function_call" {
		return
	}
	call := s.calls[index]
	if call == nil {
		call = &codexCall{}
		s.calls[index] = call
	}
	if item.ID != "" {
		call.id = item.ID
	}
	if item.CallID != "" {
		call.callID = item.CallID
	}
	if item.Name != "" {
		call.name = item.Name
	}
	if item.Arguments != "" {
		call.arguments = item.Arguments
	}
}

func (s *codexStream) finishCodexItem(index int, item codexOutputItem, raw []byte) {
	if item.Type == "reasoning" {
		artifact, err := json.Marshal(item)
		if err == nil && len(raw) > 0 {
			artifact = append([]byte(nil), raw...)
		}
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelReasoningDone, ReasoningArtifact: artifact})
		return
	}
	if item.Type == "message" && !s.texts[index] {
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: content.Text})
			}
		}
		return
	}
	if item.Type != "function_call" {
		return
	}
	s.ensureCodexCall(index, &item)
	s.emitCodexCall(index)
}

func (s *codexStream) emitCodexCall(index int) {
	call := s.calls[index]
	if call == nil || call.emitted {
		return
	}
	call.emitted = true
	name := call.name
	if original, ok := s.nameMap[name]; ok {
		name = original
	}
	if name == "" || call.callID == "" {
		return
	}
	arguments := call.arguments
	if arguments == "" {
		arguments = "{}"
	}
	id := call.callID
	if call.id != "" {
		id += "|" + call.id
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelToolCall, ToolCall: &gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: name, Arguments: []byte(arguments)}})
}

func (s *codexStream) finishCodexResponse(response *codexResponse) {
	if response == nil {
		s.finishWithError(fmt.Errorf("gateway: Codex completion has no response"))
		return
	}
	indices := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		s.emitCodexCall(index)
	}
	if response.Usage != nil {
		s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelUsage, Usage: gotato.Usage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}})
	}
	stop := codexStopReason(response.Status, response.IncompleteDetails)
	for _, call := range s.calls {
		if call.emitted {
			stop = gotato.StopToolCalls
			break
		}
	}
	s.queue = append(s.queue, gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: stop})
	s.finished = true
}

func codexStopReason(status string, details *struct {
	Reason string `json:"reason"`
}) gotato.StopReason {
	if status == "incomplete" && details != nil && details.Reason == "max_output_tokens" {
		return gotato.StopMaxTokens
	}
	if status == "failed" || status == "cancelled" || (status == "incomplete" && (details == nil || details.Reason != "max_output_tokens")) {
		return gotato.StopError
	}
	return gotato.StopEndTurn
}

func (s *codexStream) finishWithError(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.terminal = err
}

func (s *codexStream) Close() error {
	s.closeOnce.Do(func() {
		if s.response != nil && s.response.Body != nil {
			_ = s.response.Body.Close()
		}
	})
	return nil
}
