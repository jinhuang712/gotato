package fakegateway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/internal/fakegateway"
)

// The tests here speak raw HTTP, so they check the fake against the wire
// formats and need no Gotato code.

type reply struct {
	status int
	body   string
	err    error
}

func post(t *testing.T, srv *httptest.Server, path string, header map[string]string, body any) reply {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("x-api-key", "k")
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: string(data), err: err}
}

// events returns the data of each SSE event.
func events(body string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, data)
		}
	}
	return out
}

// answerText joins the text deltas of a stream.
func answerText(body string) string {
	var out strings.Builder
	for _, e := range events(body) {
		var ev struct {
			Type    string          `json:"type"`
			Delta   json.RawMessage `json:"delta"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(e), &ev) != nil || strings.Contains(ev.Type, "reasoning") {
			continue
		}
		var s string
		var obj struct {
			Text string `json:"text"`
		}
		switch {
		case json.Unmarshal(ev.Delta, &s) == nil && s != "":
			out.WriteString(s)
		case json.Unmarshal(ev.Delta, &obj) == nil:
			out.WriteString(obj.Text)
		}
		for _, c := range ev.Choices {
			out.WriteString(c.Delta.Content)
		}
	}
	return out.String()
}

func user(text string) map[string]any { return map[string]any{"role": "user", "content": text} }

var apis = []struct {
	name, path string
	body       func(text string, extra map[string]any) map[string]any
}{
	{fakegateway.APIAnthropic, "/v1/messages", func(text string, extra map[string]any) map[string]any {
		b := map[string]any{"model": "claude-sonnet-5-5", "max_tokens": 1000, "stream": true, "messages": []any{user(text)}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}},
	{fakegateway.APIResponses, "/v1/responses", func(text string, extra map[string]any) map[string]any {
		b := map[string]any{"model": "gpt-6", "stream": true, "store": false, "input": []any{user(text)}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}},
	{fakegateway.APIChat, "/v1/chat/completions", func(text string, extra map[string]any) map[string]any {
		b := map[string]any{"model": "gpt-6", "stream": true, "stream_options": map[string]any{"include_usage": true}, "messages": []any{user(text)}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}},
}

func TestPlainReplyOnEachWireAPI(t *testing.T) {
	finals := map[string][]string{
		fakegateway.APIAnthropic: {"message_stop", "text_delta", `"stop_reason":"end_turn"`},
		fakegateway.APIResponses: {"response.completed", "response.output_text.delta", `"cached_tokens":0`},
		fakegateway.APIChat:      {"[DONE]", `"finish_reason":"stop"`, `"prompt_tokens"`},
	}
	for _, api := range apis {
		t.Run(api.name, func(t *testing.T) {
			srv := httptest.NewServer(fakegateway.New())
			defer srv.Close()
			r := post(t, srv, api.path, nil, api.body("hello there", nil))
			if r.status != 200 || r.err != nil {
				t.Fatalf("%d %v %s", r.status, r.err, r.body)
			}
			for _, want := range finals[api.name] {
				if !strings.Contains(r.body, want) {
					t.Errorf("stream lacks %s:\n%s", want, r.body)
				}
			}
			if joined := strings.ReplaceAll(r.body, `","`, ""); !strings.Contains(r.body, "fake") {
				t.Errorf("no echo in %s", joined)
			}
		})
	}
}

func TestUnknownDirectiveAndBadValuesAreA400(t *testing.T) {
	for _, api := range apis {
		for _, text := range []string{"hi [quirk:overflw]", "hi [quirk:delay=soon]", "hi [quirk:cached]", "hi [quirk:cut-after-text=x]"} {
			srv := httptest.NewServer(fakegateway.New())
			r := post(t, srv, api.path, nil, api.body(text, nil))
			srv.Close()
			if r.status != 400 || !strings.Contains(r.body, "fakegateway:") {
				t.Errorf("%s %q: %d %s", api.name, text, r.status, r.body)
			}
		}
		srv := httptest.NewServer(fakegateway.New())
		r := post(t, srv, api.path, map[string]string{fakegateway.QuirkHeader: "nonsense"}, api.body("hi", nil))
		srv.Close()
		if r.status != 400 {
			t.Errorf("%s header typo: %d %s", api.name, r.status, r.body)
		}
	}
}

func TestOverflowIsRefusedInEachProvidersWords(t *testing.T) {
	want := map[string][]string{
		fakegateway.APIAnthropic: {`"type":"invalid_request_error"`, "prompt is too long: 210001 tokens > 200000 maximum"},
		fakegateway.APIResponses: {`"code":"context_length_exceeded"`, "Your input exceeds the context window of this model."},
		fakegateway.APIChat:      {`"code":"context_length_exceeded"`, "This model's maximum context length is 200000 tokens. However, your messages resulted in 210001 tokens."},
	}
	for _, api := range apis {
		t.Run(api.name, func(t *testing.T) {
			g := fakegateway.New()
			srv := httptest.NewServer(g)
			defer srv.Close()
			r := post(t, srv, api.path, nil, api.body("hi [quirk:overflow]", nil))
			if r.status != 400 {
				t.Fatalf("%d %s", r.status, r.body)
			}
			for _, w := range want[api.name] {
				if !strings.Contains(r.body, w) {
					t.Errorf("refusal lacks %q: %s", w, r.body)
				}
			}
			// The header does the same.
			if r := post(t, srv, api.path, map[string]string{fakegateway.QuirkHeader: "overflow"}, api.body("hi", nil)); r.status != 400 {
				t.Errorf("header: %d", r.status)
			}
			if got := g.Requests(); got[0].Outcome != "refused" || got[0].UserText != "hi" || got[0].Quirks["overflow"] != "" {
				t.Errorf("recorded %+v", got[0])
			}
		})
	}
}

func TestOverflowOnceThenAnswers(t *testing.T) {
	for _, api := range apis {
		srv := httptest.NewServer(fakegateway.New())
		first := post(t, srv, api.path, nil, api.body("hi [quirk:overflow=once]", nil))
		second := post(t, srv, api.path, nil, api.body("hi [quirk:overflow=once]", nil))
		srv.Close()
		if first.status != 400 || second.status != 200 {
			t.Errorf("%s: %d then %d", api.name, first.status, second.status)
		}
	}
}

func TestARequestPastTheWindowIsRefusedAndAMaxTokensPastTheLimitIsNotAnOverflow(t *testing.T) {
	for _, api := range apis {
		srv := httptest.NewServer(fakegateway.New(fakegateway.WithWindow(300), fakegateway.WithMaxOutput(500)))
		long := post(t, srv, api.path, nil, api.body(strings.Repeat("word ", 400), map[string]any{"max_tokens": 100}))
		if long.status != 400 || !strings.Contains(long.body, "too long") && !strings.Contains(long.body, "context") {
			t.Errorf("%s long: %d %s", api.name, long.status, long.body)
		}
		extra := map[string]any{"max_tokens": 99999, "max_output_tokens": 99999}
		if api.name == fakegateway.APIAnthropic {
			delete(extra, "max_output_tokens")
		}
		big := post(t, srv, api.path, nil, api.body("hi", extra))
		srv.Close()
		lower := strings.ToLower(big.body)
		if big.status != 400 || !strings.Contains(lower, "99999") || strings.Contains(lower, "context") || strings.Contains(lower, "too long") {
			t.Errorf("%s max_tokens: %d %s", api.name, big.status, big.body)
		}
	}
}

func TestMidstreamOverflow(t *testing.T) {
	want := map[string]string{
		fakegateway.APIAnthropic: `"stop_reason":"model_context_window_exceeded"`,
		fakegateway.APIResponses: `"type":"error"`,
		fakegateway.APIChat:      `"code":"context_length_exceeded"`,
	}
	for _, api := range apis {
		srv := httptest.NewServer(fakegateway.New())
		r := post(t, srv, api.path, nil, api.body("hi [quirk:overflow-midstream]", nil))
		failed := post(t, srv, "/v1/responses", nil, apis[1].body("hi [quirk:overflow-midstream=failed]", nil))
		srv.Close()
		if r.status != 200 || !strings.Contains(r.body, want[api.name]) || strings.Contains(r.body, "response.completed") {
			t.Errorf("%s: %d %s", api.name, r.status, r.body)
		}
		if !strings.Contains(r.body, "fake") && !strings.Contains(r.body, "f") {
			t.Errorf("no text before the overflow")
		}
		if !strings.Contains(failed.body, "response.failed") || !strings.Contains(failed.body, "context_length_exceeded") {
			t.Errorf("failed variant: %s", failed.body)
		}
	}
}

func TestCutAfterTextAndInArgumentsThenAnswers(t *testing.T) {
	tools := map[string]any{
		fakegateway.APIAnthropic: []any{map[string]any{"name": "echo", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}},
		fakegateway.APIResponses: []any{map[string]any{"type": "function", "name": "echo", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}},
		fakegateway.APIChat:      []any{map[string]any{"type": "function", "function": map[string]any{"name": "echo", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}},
	}
	for _, api := range apis {
		srv := httptest.NewServer(fakegateway.New())
		for _, text := range []string{"a long enough question [quirk:cut-after-text]", "use-tool [quirk:cut-in-args]"} {
			body := api.body(text, map[string]any{"tools": tools[api.name]})
			cut := post(t, srv, api.path, nil, body)
			if cut.err == nil {
				t.Errorf("%s %q: the stream was not cut: %s", api.name, text, cut.body)
			}
			if !strings.Contains(cut.body, "data:") {
				t.Errorf("%s %q: nothing before the cut", api.name, text)
			}
			again := post(t, srv, api.path, nil, body)
			if again.err != nil || again.status != 200 || !strings.Contains(again.body, "echo") && !strings.Contains(again.body, "fake") {
				t.Errorf("%s %q: second try %v %d", api.name, text, again.err, again.status)
			}
		}
		srv.Close()
	}
}

func TestCachedTokensInEachProvidersField(t *testing.T) {
	want := map[string]string{
		fakegateway.APIAnthropic: `"cache_read_input_tokens":1200`,
		fakegateway.APIResponses: `"input_tokens_details":{"cached_tokens":1200}`,
		fakegateway.APIChat:      `"prompt_tokens_details":{"audio_tokens":0,"cached_tokens":1200}`,
	}
	for _, api := range apis {
		srv := httptest.NewServer(fakegateway.New())
		r := post(t, srv, api.path, nil, api.body(strings.Repeat("filler text ", 400)+"[quirk:cached=1200]", nil))
		srv.Close()
		if !strings.Contains(r.body, want[api.name]) {
			t.Errorf("%s: %s", api.name, r.body)
		}
	}
}

func TestChatSendsNoUsageUnlessAsked(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	r := post(t, srv, "/v1/chat/completions", nil, map[string]any{"model": "m", "stream": true, "messages": []any{user("hi")}})
	if strings.Contains(r.body, "prompt_tokens") {
		t.Fatalf("usage without include_usage: %s", r.body)
	}
}

func TestDelayHonorsCancellation(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	for _, api := range apis {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		b, _ := json.Marshal(api.body("hi [quirk:delay=30s]", nil))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+api.path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer k")
		req.Header.Set("x-api-key", "k")
		req.Header.Set("anthropic-version", "2023-06-01")
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		cancel()
		if err == nil || time.Since(start) > 5*time.Second {
			t.Errorf("%s: err %v after %v", api.name, err, time.Since(start))
		}
	}
	// A short delay is waited for.
	start := time.Now()
	if r := post(t, srv, apis[0].path, nil, apis[0].body("hi [quirk:delay=200ms]", nil)); r.status != 200 || time.Since(start) < 200*time.Millisecond {
		t.Errorf("short delay: %d after %v", r.status, time.Since(start))
	}
}

func TestRequestsAreRecorded(t *testing.T) {
	g := fakegateway.New()
	srv := httptest.NewServer(g)
	defer srv.Close()
	post(t, srv, "/v1/responses", map[string]string{"session-id": "s1"}, apis[1].body("record me", map[string]any{"prompt_cache_key": "k1"}))
	got := g.Requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	r := got[0]
	if r.API != fakegateway.APIResponses || r.Method != "POST" || r.Path != "/v1/responses" || r.Header.Get("session-id") != "s1" ||
		r.JSON["prompt_cache_key"] != "k1" || r.UserText != "record me" || r.Status != 200 || r.Outcome != "ok" || len(r.Body) == 0 {
		t.Fatalf("recorded %+v", r)
	}
	g.Reset()
	if len(g.Requests()) != 0 {
		t.Fatal("Reset kept requests")
	}
}

func TestAuthAndMalformedToolLoopsAreRefused(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	for _, api := range apis {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+api.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s without key: %d", api.name, resp.StatusCode)
		}
	}
	orphan := map[string]map[string]any{
		fakegateway.APIAnthropic: {"messages": []any{user("q"), map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "x"}}}}},
		fakegateway.APIResponses: {"input": []any{user("q"), map[string]any{"type": "function_call_output", "call_id": "c1", "output": "x"}}},
		fakegateway.APIChat:      {"messages": []any{user("q"), map[string]any{"role": "tool", "tool_call_id": "c1", "content": "x"}}},
	}
	for _, api := range apis {
		r := post(t, srv, api.path, nil, api.body("q", orphan[api.name]))
		if r.status != 400 {
			t.Errorf("%s orphan result: %d %s", api.name, r.status, r.body)
		}
	}
}

// reasoningTurn runs the first request of a tool loop that reasons, and
// returns the assistant side the next request must replay.
func TestAnthropicThinkingMustBeReplayedAsReceived(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	tool := []any{map[string]any{"name": "echo", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}
	first := post(t, srv, "/v1/messages", nil, apis[0].body("use-tool [quirk:reasoning]", map[string]any{"tools": tool}))
	var thinking, signature, id, args string
	for _, e := range events(first.body) {
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				ID string `json:"id"`
			} `json:"content_block"`
			Delta struct {
				Type, Thinking, Signature, PartialJSON string `json:"-"`
			}
		}
		var raw map[string]any
		_ = json.Unmarshal([]byte(e), &raw)
		_ = ev
		if d, ok := raw["delta"].(map[string]any); ok {
			switch d["type"] {
			case "thinking_delta":
				thinking += d["thinking"].(string)
			case "signature_delta":
				signature = d["signature"].(string)
			case "input_json_delta":
				args += d["partial_json"].(string)
			}
		}
		if cb, ok := raw["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
			id = cb["id"].(string)
		}
	}
	if thinking == "" || signature == "" || id == "" || args != `{"value":"from-tool"}` {
		t.Fatalf("stream: %q %q %q %q\n%s", thinking, signature, id, args, first.body)
	}
	next := func(blocks []any) reply {
		return post(t, srv, "/v1/messages", nil, apis[0].body("use-tool [quirk:reasoning]", map[string]any{"tools": tool, "messages": []any{
			user("use-tool [quirk:reasoning]"),
			map[string]any{"role": "assistant", "content": blocks},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "from-tool"}}},
		}}))
	}
	use := map[string]any{"type": "tool_use", "id": id, "name": "echo", "input": map[string]any{"value": "from-tool"}}
	block := map[string]any{"type": "thinking", "thinking": thinking, "signature": signature}
	if r := next([]any{block, use}); r.status != 200 || answerText(r.body) != "tool said: from-tool" {
		t.Fatalf("replayed: %d %s", r.status, r.body)
	}
	if r := next([]any{use}); r.status != 400 || !strings.Contains(r.body, "Expected `thinking` or `redacted_thinking`, but found `tool_use`") {
		t.Fatalf("dropped: %d %s", r.status, r.body)
	}
	edited := map[string]any{"type": "thinking", "thinking": thinking + "!", "signature": signature}
	if r := next([]any{edited, use}); r.status != 400 || !strings.Contains(r.body, "cannot be modified") {
		t.Fatalf("edited: %d %s", r.status, r.body)
	}
	forged := map[string]any{"type": "thinking", "thinking": thinking, "signature": "AAAA"}
	if r := next([]any{forged, use}); r.status != 400 || !strings.Contains(r.body, "Invalid `signature`") {
		t.Fatalf("forged: %d %s", r.status, r.body)
	}
	// A thinking block bound to another prefix: refused, unless the request
	// asks to drop it (and sends the beta header).
	changed := apis[0].body("use-tool [quirk:reasoning]", map[string]any{"tools": tool, "system": "a different system prompt", "messages": []any{
		user("use-tool [quirk:reasoning]"),
		map[string]any{"role": "assistant", "content": []any{block, use}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "from-tool"}}},
	}})
	if r := post(t, srv, "/v1/messages", nil, changed); r.status != 400 || !strings.Contains(r.body, "bound to a different conversation") {
		t.Fatalf("rebound: %d %s", r.status, r.body)
	}
	changed["thinking"] = map[string]any{"type": "adaptive", "block_binding": map[string]any{"prefix_mismatch_behavior": "drop_block"}}
	if r := post(t, srv, "/v1/messages", nil, changed); r.status != 400 || !strings.Contains(r.body, "block_binding: Extra inputs are not permitted") {
		t.Fatalf("no beta header: %d %s", r.status, r.body)
	}
	if r := post(t, srv, "/v1/messages", map[string]string{"anthropic-beta": "thinking-binding-controls-2026-08-01"}, changed); r.status != 200 {
		t.Fatalf("drop_block: %d %s", r.status, r.body)
	}
}

func TestResponsesReasoningItemsMustBeReplayed(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	tool := []any{map[string]any{"type": "function", "name": "echo", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}
	body := apis[1].body("use-tool [quirk:reasoning]", map[string]any{"tools": tool, "include": []string{"reasoning.encrypted_content"}})
	first := post(t, srv, "/v1/responses", nil, body)
	var reasoning, call map[string]any
	for _, e := range events(first.body) {
		var raw struct {
			Type string         `json:"type"`
			Item map[string]any `json:"item"`
		}
		_ = json.Unmarshal([]byte(e), &raw)
		if raw.Type == "response.output_item.done" {
			switch raw.Item["type"] {
			case "reasoning":
				reasoning = raw.Item
			case "function_call":
				call = raw.Item
			}
		}
	}
	if reasoning == nil || call == nil || reasoning["encrypted_content"] == nil {
		t.Fatalf("stream: %s", first.body)
	}
	next := func(items ...any) reply {
		input := append([]any{user("use-tool [quirk:reasoning]")}, items...)
		input = append(input, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": "from-tool"})
		return post(t, srv, "/v1/responses", nil, apis[1].body("x", map[string]any{"tools": tool, "input": input}))
	}
	if r := next(reasoning, call); r.status != 200 || answerText(r.body) != "tool said: from-tool" {
		t.Fatalf("replayed: %d %s", r.status, r.body)
	}
	if r := next(call); r.status != 400 || !strings.Contains(r.body, "of type 'function_call' was provided without its required 'reasoning' item") {
		t.Fatalf("dropped: %d %s", r.status, r.body)
	}
	alone := post(t, srv, "/v1/responses", nil, apis[1].body("x", map[string]any{"input": []any{user("use-tool [quirk:reasoning]"), reasoning, user("next [quirk:reasoning]")}}))
	if alone.status != 400 || !strings.Contains(alone.body, "of type 'reasoning' was provided without its required following item") {
		t.Fatalf("reasoning alone: %d %s", alone.status, alone.body)
	}
	forged := map[string]any{"id": reasoning["id"], "type": "reasoning", "summary": []any{}, "encrypted_content": "nope"}
	if r := next(forged, call); r.status != 400 || !strings.Contains(r.body, "could not be verified") {
		t.Fatalf("forged: %d %s", r.status, r.body)
	}
	// Without the include the reasoning item carries no encrypted content.
	plain := post(t, srv, "/v1/responses", nil, apis[1].body("use-tool [quirk:reasoning]", map[string]any{"tools": tool}))
	if strings.Contains(plain.body, "encrypted_content") {
		t.Fatalf("encrypted content without include: %s", plain.body)
	}
}

func TestChatReasoningContentMustBePassedBackInTheToolLoop(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	tool := []any{map[string]any{"type": "function", "function": map[string]any{"name": "echo", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}}
	first := post(t, srv, "/v1/chat/completions", nil, apis[2].body("use-tool [quirk:reasoning]", map[string]any{"tools": tool}))
	if !strings.Contains(first.body, "reasoning_content") {
		t.Fatalf("no reasoning: %s", first.body)
	}
	next := func(reasoning string) reply {
		assistant := map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "echo", "arguments": "{}"}}}}
		if reasoning != "" {
			assistant["reasoning_content"] = reasoning
		}
		return post(t, srv, "/v1/chat/completions", nil, apis[2].body("x", map[string]any{"tools": tool, "messages": []any{
			user("use-tool [quirk:reasoning]"), assistant, map[string]any{"role": "tool", "tool_call_id": "c1", "content": "from-tool"}}}))
	}
	if r := next("thinking about: use-tool"); r.status != 200 {
		t.Fatalf("replayed: %d %s", r.status, r.body)
	}
	if r := next(""); r.status != 400 || !strings.Contains(r.body, "`reasoning_content` in the thinking mode must be passed back to the API.") {
		t.Fatalf("dropped: %d %s", r.status, r.body)
	}
}

func TestMidConversationSystemMessages(t *testing.T) {
	g := fakegateway.New()
	srv := httptest.NewServer(g)
	defer srv.Close()
	system := func(text string) map[string]any { return map[string]any{"role": "system", "content": text} }
	assistant := map[string]any{"role": "assistant", "content": "ok"}
	toolUse := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "x", "input": map[string]any{}}}}
	toolResult := map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "r"}}}
	anthropic := func(model string, messages ...any) reply {
		return post(t, srv, "/v1/messages", nil, apis[0].body("x", map[string]any{"model": model, "messages": messages}))
	}
	// After a user turn, last or before an assistant turn: accepted on the models that take it.
	if r := anthropic("claude-sonnet-5-5", user("q"), system("s"), assistant, user("again")); r.status != 200 {
		t.Fatalf("after user: %d %s", r.status, r.body)
	}
	if r := anthropic("claude-opus-5-5-20260101", user("q"), toolUse, toolResult, system("s")); r.status != 200 {
		t.Fatalf("after tool results: %d %s", r.status, r.body)
	}
	if got := g.Requests(); len(got[0].MidSystem) != 1 || got[0].MidSystem[0] != 1 || got[1].MidSystem[0] != 3 {
		t.Fatalf("recorded positions: %+v %+v", got[0].MidSystem, got[1].MidSystem)
	}
	for name, r := range map[string]reply{
		"sonnet 5":               anthropic("claude-sonnet-5", user("q"), system("s"), assistant, user("again")),
		"first entry":            anthropic("claude-sonnet-5-5", system("s"), user("q")),
		"after an assistant":     anthropic("claude-sonnet-5-5", user("q"), assistant, system("s")),
		"between use and result": anthropic("claude-sonnet-5-5", user("q"), toolUse, system("s"), toolResult),
	} {
		if r.status != 400 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	// A user message after a user message is merged, not refused.
	if r := anthropic("claude-sonnet-5", user("q"), user("again")); r.status != 200 {
		t.Errorf("two user turns: %d %s", r.status, r.body)
	}
	// OpenAI takes developer and system messages anywhere, and the fake records them.
	g.Reset()
	r := post(t, srv, "/v1/responses", nil, apis[1].body("x", map[string]any{"input": []any{
		map[string]any{"role": "developer", "content": "first"}, user("q"), map[string]any{"role": "developer", "content": "mid"}}}))
	if r.status != 200 {
		t.Fatalf("responses developer: %d %s", r.status, r.body)
	}
	r = post(t, srv, "/v1/chat/completions", nil, apis[2].body("x", map[string]any{"messages": []any{
		system("first"), user("q"), map[string]any{"role": "assistant", "content": "a"}, map[string]any{"role": "developer", "content": "mid"}, user("q2")}}))
	if r.status != 200 {
		t.Fatalf("chat developer: %d %s", r.status, r.body)
	}
	got := g.Requests()
	if len(got[0].MidSystem) != 1 || got[0].MidSystem[0] != 2 || len(got[1].MidSystem) != 1 || got[1].MidSystem[0] != 3 {
		t.Fatalf("recorded positions: %+v", got)
	}
}
