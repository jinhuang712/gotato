package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

var (
	searchSpec = gotato.ToolSpec{ID: gotato.SearchToolName, Description: "the catalog", InputSchema: []byte(`{"type":"object","properties":{"names":{"type":"array","items":{"type":"string"}}}}`)}
	lookupSpec = gotato.ToolSpec{ID: "orders.lookup", Description: "Look up an order", InputSchema: []byte(`{"type":"object"}`)}
	refundSpec = gotato.ToolSpec{ID: "orders.refund", Description: "Refund an order", InputSchema: []byte(`{"type":"object"}`)}
)

// loadingTurns returns the requests of two consecutive Turns, the second after
// the Model loaded orders.lookup with search_tools, as a Session builds them.
func loadingTurns() (first, second gotato.ModelRequest) {
	first = gotato.ModelRequest{
		Messages:      []gotato.Message{gotato.UserMessage("find order 7")},
		Tools:         []gotato.ToolSpec{searchSpec},
		UnloadedTools: []gotato.ToolSpec{lookupSpec, refundSpec},
		CacheBreakpoints: []gotato.CacheBreakpoint{
			{After: gotato.CacheAfterTools}, {After: gotato.CacheAfterMessage, Index: 0},
		},
	}
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "call_1", ToolID: gotato.SearchToolName, Arguments: []byte(`{"names":["orders.lookup"]}`)}}
	result := gotato.Message{
		Role:  gotato.RoleToolResult,
		Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "Loaded: orders.lookup."}},
		ToolResult: &gotato.ToolResult{
			CallID: "call_1", Status: gotato.ToolResultOK, Loaded: []string{"orders.lookup"},
		},
	}
	second = gotato.ModelRequest{
		Messages:      []gotato.Message{gotato.UserMessage("find order 7"), call, result},
		Tools:         []gotato.ToolSpec{lookupSpec, searchSpec},
		LoadedTools:   []string{"orders.lookup"},
		UnloadedTools: []gotato.ToolSpec{refundSpec},
		CacheBreakpoints: []gotato.CacheBreakpoint{
			{After: gotato.CacheAfterTools}, {After: gotato.CacheAfterMessage, Index: 2},
		},
	}
	return first, second
}

type bodyEncoder func(*Client, gotato.ModelRequest) ([]byte, error)

func encodersByAPI() map[string]bodyEncoder {
	return map[string]bodyEncoder{
		APIAnthropicMessages: func(c *Client, r gotato.ModelRequest) ([]byte, error) {
			body, _, err := encodeAnthropicRequest(c.model, r, c.rules)
			return body, err
		},
		APIResponses: func(c *Client, r gotato.ModelRequest) ([]byte, error) {
			body, _, err := encodeResponsesRequest(c.model, r, c.rules)
			return body, err
		},
		APIChatCompletions: func(c *Client, r gotato.ModelRequest) ([]byte, error) {
			body, _, err := encodeRequest(c.model, r, c.rules)
			return body, err
		},
	}
}

func rawTools(t *testing.T, body []byte) []byte {
	t.Helper()
	var envelope struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Tools
}

func newDeferredClient(t *testing.T, api, form string) *Client {
	t.Helper()
	client, err := New(Config{API: api, Model: "m", BaseURL: "https://gw.example.com", DeferredTools: form})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// A load changes the tool list of the list form, and leaves it byte for byte
// the same in the native form (G-G07).
func TestEachWireAPIKeepsTheToolListWhileAToolLoads(t *testing.T) {
	first, second := loadingTurns()
	for api, encode := range encodersByAPI() {
		for _, form := range []string{DeferredNative, DeferredList, ""} {
			client := newDeferredClient(t, api, form)
			a, err := encode(client, first)
			if err != nil {
				t.Fatal(err)
			}
			b, err := encode(client, second)
			if err != nil {
				t.Fatal(err)
			}
			same := bytes.Equal(rawTools(t, a), rawTools(t, b))
			if want := client.DeferredTools() == DeferredNative; same != want {
				t.Errorf("%s with %q (%s): tool list unchanged = %v, want %v\n%s\n%s", api, form, client.DeferredTools(), same, want, rawTools(t, a), rawTools(t, b))
			}
		}
	}
}

func TestDeferredToolsDefaultsPerWireAPI(t *testing.T) {
	cases := []struct{ api, configured, want string }{
		{APIAnthropicMessages, "", DeferredNative},
		{APIResponses, "", DeferredNative},
		{APIChatCompletions, "", DeferredList},
		{APIAnthropicMessages, "list", DeferredList},
		{APIResponses, "list", DeferredList},
		{APIResponses, "native", DeferredNative},
		// Chat Completions has no native form: it uses list whatever is set.
		{APIChatCompletions, "native", DeferredList},
		{APIChatCompletions, "list", DeferredList},
		{"", "native", DeferredList},
	}
	for _, c := range cases {
		client, err := New(Config{API: c.api, Model: "m", BaseURL: "https://gw.example.com", DeferredTools: c.configured})
		if err != nil {
			t.Fatal(err)
		}
		if got := client.DeferredTools(); got != c.want {
			t.Errorf("%s with %q = %s, want %s", c.api, c.configured, got, c.want)
		}
	}
	if _, err := New(Config{Model: "m", BaseURL: "https://gw.example.com", DeferredTools: "hosted"}); err == nil {
		t.Fatal("an unknown form must be refused")
	}
}

func TestParseYAMLReadsDeferredTools(t *testing.T) {
	config, err := parseYAML([]byte("api: openai-responses\nmodel: m\ndeferred_tools: list\n"))
	if err != nil {
		t.Fatal(err)
	}
	if config.DeferredTools != "list" {
		t.Fatalf("DeferredTools = %q", config.DeferredTools)
	}
	if _, err := parseYAML([]byte("api: openai-responses\nmodel: m\ndeferred_tools: both\n")); err == nil {
		t.Fatal("an unknown form must be refused")
	}
}

func TestAnthropicNativeFormDefersTheCatalogAndReferencesALoad(t *testing.T) {
	_, second := loadingTurns()
	client := newDeferredClient(t, APIAnthropicMessages, "")
	body, err := encodersByAPI()[APIAnthropicMessages](client, second)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Tools []struct {
			Name         string         `json:"name"`
			DeferLoading bool           `json:"defer_loading"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"tools"`
		Messages []struct {
			Content []struct {
				Type    string `json:"type"`
				Content []struct {
					Type     string `json:"type"`
					ToolName string `json:"tool_name"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range payload.Tools {
		label := tool.Name
		if tool.DeferLoading {
			label += "*"
		}
		if tool.CacheControl != nil {
			label += "^"
		}
		names = append(names, label)
	}
	if got := strings.Join(names, " "); got != "search_tools^ orders_lookup* orders_refund*" {
		t.Fatalf("tools = %s", got)
	}
	last := payload.Messages[len(payload.Messages)-1].Content[0]
	if last.Type != "tool_result" || len(last.Content) != 2 || last.Content[0].Type != "text" ||
		last.Content[1].Type != "tool_reference" || last.Content[1].ToolName != "orders_lookup" {
		t.Fatalf("tool result = %+v", last)
	}
}

func TestResponsesNativeFormUsesClientToolSearch(t *testing.T) {
	_, second := loadingTurns()
	client := newDeferredClient(t, APIResponses, "")
	body, err := encodersByAPI()[APIResponses](client, second)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Tools []map[string]any `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Tools) != 3 || payload.Tools[0]["type"] != "tool_search" || payload.Tools[0]["execution"] != "client" ||
		payload.Tools[1]["name"] != "orders_lookup" || payload.Tools[1]["defer_loading"] != true ||
		payload.Tools[2]["name"] != "orders_refund" || payload.Tools[2]["defer_loading"] != true {
		t.Fatalf("tools = %v", payload.Tools)
	}
	if len(payload.Input) != 3 {
		t.Fatalf("input = %v", payload.Input)
	}
	call, output := payload.Input[1], payload.Input[2]
	if call["type"] != "tool_search_call" || call["call_id"] != "call_1" || call["execution"] != "client" {
		t.Fatalf("call = %v", call)
	}
	if args, ok := call["arguments"].(map[string]any); !ok || args["names"] == nil {
		t.Fatalf("a tool_search_call carries its arguments as an object: %v", call["arguments"])
	}
	loaded, _ := output["tools"].([]any)
	if output["type"] != "tool_search_output" || output["call_id"] != "call_1" || len(loaded) != 1 ||
		loaded[0].(map[string]any)["name"] != "orders_lookup" {
		t.Fatalf("output = %v", output)
	}
}

// The Model's tool_search_call comes back as a call to the search tool, with
// arguments given as an object.
func TestResponsesStreamReadsAToolSearchCall(t *testing.T) {
	var item responsesOutputItem
	if err := json.Unmarshal([]byte(`{"type":"tool_search_call","execution":"client","call_id":"c1","status":"completed","arguments":{"names":["orders.lookup"]}}`), &item); err != nil {
		t.Fatal(err)
	}
	stream := &responsesStream{calls: map[int]*responsesCall{}, texts: map[int]bool{}, nameMap: map[string]string{"search_tools": gotato.SearchToolName}}
	stream.finishResponsesItem(0, item, nil)
	if len(stream.queue) != 1 || stream.queue[0].Kind != gotato.ModelToolCall {
		t.Fatalf("queue = %+v", stream.queue)
	}
	call := stream.queue[0].ToolCall
	if call.ToolID != gotato.SearchToolName || string(call.ID) != "c1" || !json.Valid(call.Arguments) || !strings.Contains(string(call.Arguments), "orders.lookup") {
		t.Fatalf("call = %+v (%s)", call, call.Arguments)
	}
}

// A loaded tool whose reference left the history stays visible, as a plain tool.
func TestAnUnreferencedLoadedToolIsSentPlain(t *testing.T) {
	_, second := loadingTurns()
	second.Messages = second.Messages[:1]
	layout := newDeferredLayout(second, providerRules{deferredNative: true})
	var labels []string
	for _, tool := range layout.tools {
		label := tool.spec.ID
		if tool.deferred {
			label += "*"
		}
		labels = append(labels, label)
	}
	if got := strings.Join(labels, " "); got != "orders.lookup search_tools orders.refund*" {
		t.Fatalf("layout = %s", got)
	}
}
