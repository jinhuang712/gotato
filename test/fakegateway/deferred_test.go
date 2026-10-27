package fakegateway_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/test/fakegateway"
)

// The native form of deferred tools, from the documented shapes of each wire
// API (see "Deferred tools" in the package documentation). The fake plays the
// shape; it is not a real provider.

func schemaOf(prop string) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{prop: map[string]any{"type": "string"}}}
}

func anthropicDeferredTools() []any {
	return []any{
		map[string]any{"name": "search_tools", "description": "d", "input_schema": schemaOf("query")},
		map[string]any{"name": "orders_lookup", "description": "d", "input_schema": schemaOf("id"), "defer_loading": true},
		map[string]any{"name": "shop_invoice", "description": "d", "input_schema": schemaOf("order"), "defer_loading": true},
	}
}

// anthropicSearched is the conversation after the model searched, with the
// search's result content.
func anthropicSearched(content any) []any {
	return []any{
		user("go [quirk:load=orders.lookup]"),
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "search_tools", "input": map[string]any{"names": []string{"orders.lookup"}}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": content}}},
	}
}

func TestAnthropicDeferredToolsLoadByToolReference(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	path := "/v1/messages"
	body := func(messages []any, tools []any) map[string]any {
		return map[string]any{"model": "claude-sonnet-5-5", "max_tokens": 1000, "stream": true, "messages": messages, "tools": tools}
	}
	// The model searches for the tool first.
	r := post(t, srv, path, nil, body([]any{user("go [quirk:load=orders.lookup]")}, anthropicDeferredTools()))
	if r.status != 200 || !strings.Contains(r.body, `"name":"search_tools"`) || !strings.Contains(r.body, `"stop_reason":"tool_use"`) {
		t.Fatalf("a search was expected: %d %s", r.status, r.body)
	}
	// A result that references the tool loads it, and the model calls it.
	loaded := anthropicSearched([]any{
		map[string]any{"type": "text", "text": "Loaded: orders.lookup."},
		map[string]any{"type": "tool_reference", "tool_name": "orders_lookup"},
	})
	r = post(t, srv, path, nil, body(loaded, anthropicDeferredTools()))
	if r.status != 200 || !strings.Contains(r.body, `"name":"orders_lookup"`) {
		t.Fatalf("the loaded tool should be called: %d %s", r.status, r.body)
	}
	// A result that does not reference it leaves it unloaded.
	r = post(t, srv, path, nil, body(anthropicSearched("Not found: orders.lookup."), anthropicDeferredTools()))
	if r.status != 200 || !strings.Contains(answerText(r.body), "fake: orders_lookup is not loaded") {
		t.Fatalf("an unloaded tool should be reported: %d %s", r.status, r.body)
	}
	// The shapes the API refuses.
	cached := anthropicDeferredTools()
	cached[1].(map[string]any)["cache_control"] = map[string]any{"type": "ephemeral"}
	notDeferred := anthropicSearched([]any{map[string]any{"type": "tool_reference", "tool_name": "search_tools"}})
	unknown := anthropicSearched([]any{map[string]any{"type": "tool_reference", "tool_name": "nope"}})
	for name, req := range map[string]map[string]any{
		"every tool deferred":                   body(loaded, anthropicDeferredTools()[1:]),
		"cache_control on a deferred tool":      body(loaded, cached),
		"a reference to a tool not deferred":    body(notDeferred, anthropicDeferredTools()),
		"a reference to a tool not in the list": body(unknown, anthropicDeferredTools()),
	} {
		if r := post(t, srv, path, nil, req); r.status != 400 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
}

func responsesDeferredTools() []any {
	return []any{
		map[string]any{"type": "tool_search", "execution": "client", "description": "d", "parameters": schemaOf("query")},
		map[string]any{"type": "function", "name": "orders_lookup", "parameters": schemaOf("id"), "defer_loading": true},
		map[string]any{"type": "function", "name": "plain", "parameters": schemaOf("x")},
	}
}

func TestResponsesDeferredToolsLoadByToolSearchOutput(t *testing.T) {
	srv := httptest.NewServer(fakegateway.New())
	defer srv.Close()
	path := "/v1/responses"
	body := func(input []any, tools []any) map[string]any {
		return map[string]any{"model": "gpt-6", "stream": true, "store": false, "input": input, "tools": tools}
	}
	// The model searches with a tool_search_call item whose arguments are an object.
	r := post(t, srv, path, nil, body([]any{user("go [quirk:load=orders.lookup]")}, responsesDeferredTools()))
	var call map[string]any
	for _, e := range events(r.body) {
		var ev struct {
			Type string         `json:"type"`
			Item map[string]any `json:"item"`
		}
		if json.Unmarshal([]byte(e), &ev) == nil && ev.Type == "response.output_item.done" && ev.Item["type"] == "tool_search_call" {
			call = ev.Item
		}
	}
	args, _ := call["arguments"].(map[string]any)
	if r.status != 200 || call == nil || call["execution"] != "client" || call["call_id"] == "" || args["names"] == nil {
		t.Fatalf("a tool_search_call was expected: %d %s", r.status, r.body)
	}
	searchCall := map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "c1", "status": "completed", "arguments": map[string]any{"names": []string{"orders.lookup"}}}
	output := func(tools ...any) map[string]any {
		return map[string]any{"type": "tool_search_output", "execution": "client", "call_id": "c1", "status": "completed", "tools": tools}
	}
	lookup := map[string]any{"type": "function", "name": "orders_lookup", "parameters": schemaOf("id"), "defer_loading": true}
	prompt := user("go [quirk:load=orders.lookup]")
	// The output loads the function, and the model calls it.
	r = post(t, srv, path, nil, body([]any{prompt, searchCall, output(lookup)}, responsesDeferredTools()))
	if r.status != 200 || !strings.Contains(r.body, `"type":"function_call"`) || !strings.Contains(r.body, `"name":"orders_lookup"`) {
		t.Fatalf("the loaded function should be called: %d %s", r.status, r.body)
	}
	// An output that loads nothing leaves it unloaded.
	r = post(t, srv, path, nil, body([]any{prompt, searchCall, output()}, responsesDeferredTools()))
	if r.status != 200 || !strings.Contains(answerText(r.body), "fake: orders_lookup is not loaded") {
		t.Fatalf("an unloaded function should be reported: %d %s", r.status, r.body)
	}
	// The shapes the API refuses.
	plain := map[string]any{"type": "function", "name": "plain", "parameters": schemaOf("x")}
	badCall := map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "c1", "arguments": `{"names":[]}`}
	for name, req := range map[string]map[string]any{
		"an output with no call":                body([]any{prompt, output(lookup)}, responsesDeferredTools()),
		"a call with no output":                 body([]any{prompt, searchCall}, responsesDeferredTools()),
		"an output that loads a plain function": body([]any{prompt, searchCall, output(plain)}, responsesDeferredTools()),
		"arguments as a string":                 body([]any{prompt, badCall, output(lookup)}, responsesDeferredTools()),
		"a call with no tool_search tool":       body([]any{prompt, searchCall, output(lookup)}, responsesDeferredTools()[1:]),
	} {
		if r := post(t, srv, path, nil, req); r.status != 400 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
}
