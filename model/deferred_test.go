package model

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/profile"
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
			{After: gotato.CacheAnchorAfterTools}, {After: gotato.CacheAnchorAfterMessage, Index: 0},
		},
	}
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "call_1", ToolID: gotato.SearchToolName, Arguments: []byte(`{"names":["orders.lookup"]}`)}}
	result := gotato.Message{
		Role:  gotato.RoleToolResult,
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "Loaded: orders.lookup."}},
		ToolResult: &gotato.ToolResult{
			CallID: "call_1", Status: gotato.ToolResultStatusOK, LoadedTools: []string{"orders.lookup"},
		},
	}
	second = gotato.ModelRequest{
		Messages:      []gotato.Message{gotato.UserMessage("find order 7"), call, result},
		Tools:         []gotato.ToolSpec{lookupSpec, searchSpec},
		LoadedTools:   []string{"orders.lookup"},
		UnloadedTools: []gotato.ToolSpec{refundSpec},
		CacheBreakpoints: []gotato.CacheBreakpoint{
			{After: gotato.CacheAnchorAfterTools}, {After: gotato.CacheAnchorAfterMessage, Index: 2},
		},
	}
	return first, second
}

type bodyEncoder func(*Client, gotato.ModelRequest) ([]byte, error)

// encodersByAPI encode a request through the Client's own wire, as Stream does
// before it sends.
func encodersByAPI() map[API]bodyEncoder {
	encode := func(c *Client, r gotato.ModelRequest) ([]byte, error) {
		encoded, err := c.wire.Encode(Call{Model: c.Config(), Request: r, APIKey: "k", Header: http.Header{}})
		if err != nil {
			return nil, err
		}
		return encoded.Body, nil
	}
	return map[API]bodyEncoder{APIAnthropicMessages: encode, APIResponses: encode, APIChatCompletions: encode}
}

// deferredWire is the wire for an API with a Deferred form. Chat Completions
// has no such setting: it has only the list form.
func deferredWire(api API, form DeferredForm) WireAPI {
	switch api {
	case APIResponses:
		return Responses{Deferred: form}
	case APIAnthropicMessages:
		return AnthropicMessages{Deferred: form}
	}
	return ChatCompletions{}
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

func newDeferredClient(t *testing.T, api API, form DeferredForm) *Client {
	t.Helper()
	return newClientFor(t, deferredWire(api, form), "https://gw.example.com"+endpointPaths[api], modelOf(gotato.ModelFacts{}))
}

// A deferred Tool whose InputSchema is not JSON is refused by every encoder,
// the Responses one included, and is not sent with a substitute schema.
func TestDeferredToolWithInvalidSchemaIsRefused(t *testing.T) {
	for api, encode := range encodersByAPI() {
		_, second := loadingTurns()
		bad := lookupSpec
		bad.InputSchema = []byte(`{not json`)
		second.Tools = []gotato.ToolSpec{bad, searchSpec}
		client := newDeferredClient(t, api, DeferredFormNative)
		if _, err := encode(client, second); err == nil || !strings.Contains(err.Error(), "invalid InputSchema") {
			t.Errorf("%s: err = %v, want an invalid InputSchema refusal", api, err)
		}
	}
}

// A load changes the tool list of the list form, and leaves it byte for byte
// the same in the native form (G-G07).
func TestEachWireAPIKeepsTheToolListWhileAToolLoads(t *testing.T) {
	first, second := loadingTurns()
	for api, encode := range encodersByAPI() {
		for _, form := range []DeferredForm{DeferredFormNative, DeferredFormList, ""} {
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
			// Chat Completions has only the list form, whatever the setting.
			if want := api != APIChatCompletions && form != DeferredFormList; same != want {
				t.Errorf("%s with %q: tool list unchanged = %v, want %v\n%s\n%s", api, form, same, want, rawTools(t, a), rawTools(t, b))
			}
		}
	}
}

func TestDeferredToolsDefaultsPerWireAPI(t *testing.T) {
	// Chat Completions has no Deferred setting: it has only the list form.
	cases := []struct {
		form   DeferredForm
		native bool
	}{
		{"", true},
		{"native", true},
		{"list", false},
	}
	for _, c := range cases {
		for _, api := range []API{APIAnthropicMessages, APIResponses} {
			native, err := deferredForm(deferredWire(api, c.form))
			if err != nil || native != c.native {
				t.Errorf("%s with %q: native = %v, err = %v, want %v", api, c.form, native, err, c.native)
			}
		}
	}
	for _, api := range []API{APIAnthropicMessages, APIResponses} {
		if _, err := New(deferredWire(api, "hosted"), Connection{Endpoint: "https://gw.example.com" + endpointPaths[api]}, modelOf(gotato.ModelFacts{})); err == nil {
			t.Fatalf("%s: an unknown form must be refused", api)
		}
	}
}

func deferredForm(wire WireAPI) (bool, error) {
	switch w := wire.(type) {
	case Responses:
		return w.Deferred.native()
	case AnthropicMessages:
		return w.Deferred.native()
	}
	return false, nil
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
	stream := newResponsesDecoder(map[string]string{"search_tools": gotato.SearchToolName})
	stream.finishResponsesItem(0, item, nil)
	if len(stream.queue) != 1 || stream.queue[0].Kind != gotato.ModelEventKindToolCall {
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
	layout := newDeferredLayout(second, true)
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

// toolsField returns the bytes of the tools array of a request body.
func toolsField(t *testing.T, body []byte) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	return string(fields["tools"])
}

// TestFakeNativeDeferredToolsEndToEnd runs the tool_search task's tools (a base
// tool and three ToolSets, one of them folded) through the native form of
// deferred tools on each of the two wire APIs that have one, against the fake
// gateway, which plays the documented shape of each (fakegateway, "Deferred
// tools"). It is not a real provider. The Session plays three Runs: the model
// loads a tool and calls it, loads another one and calls it, and calls the first
// again with no search. The fake refuses a request that is not shaped as the
// documentation says, so a Run that completes was accepted by it. On top of that:
//
//   - the tool list sent is the same bytes in all eight requests while tools load;
//   - the first request declares the catalog's tools deferred, and the search
//     tool and the base tool not;
//   - the load appears in the history as a tool_reference (Anthropic) or as a
//     tool_search_call and a tool_search_output (Responses), not in the tool list.
func TestFakeNativeDeferredToolsEndToEnd(t *testing.T) {
	for _, wire := range fakeWires[:2] {
		t.Run(wire.name, func(t *testing.T) {
			// The wires of fakeWires take the default, which is native.
			fake, client := newFake(t, wire)
			options := []gotato.SessionOption{gotato.WithModel(client), gotato.WithTools(reftasks.OrdersLookup())}
			for _, set := range reftasks.ToolSearchToolSets() {
				if set.Mode == profile.ToolSetFolded {
					options = append(options, gotato.WithFoldedToolSet(set.Set))
				} else {
					options = append(options, gotato.WithToolSet(set.Set))
				}
			}
			session, err := gotato.NewSession(context.Background(), options...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close(context.Background()) })
			prompts := []struct{ text, tool string }{
				{"invoice [quirk:load=shop.invoice]", "shop.invoice"},
				{"track [quirk:load=carrier.track]", "carrier.track"},
				{"invoice again [quirk:load=shop.invoice]", "shop.invoice"},
			}
			for _, prompt := range prompts {
				result, err := session.Prompt(context.Background(), gotato.UserMessage(prompt.text))
				if err != nil || result.Status != gotato.RunStatusCompleted {
					t.Fatalf("%q: %+v, %v", prompt.text, result, err)
				}
				if answer := gotato.TextOf(*result.FinalMessage); !strings.HasPrefix(answer, "tool said: ") || !strings.Contains(answer, prompt.tool+" done") {
					t.Fatalf("%q answered %q", prompt.text, answer)
				}
			}
			ids := []string{}
			for _, spec := range session.Tools() {
				ids = append(ids, spec.ID)
			}
			for _, want := range []string{"shop.invoice", "carrier.track"} {
				if !slices.Contains(ids, want) {
					t.Errorf("%s is not loaded: %v", want, ids)
				}
			}
			searches := 0
			for _, message := range session.Transcript().Messages() {
				for _, call := range message.ToolCalls {
					if call.ToolID == gotato.SearchToolName {
						searches++
					}
				}
			}
			if searches != 2 {
				t.Errorf("%d searches, want 2: the third Run calls a tool that is already loaded", searches)
			}

			requests := fake.Requests()
			if len(requests) != 8 {
				t.Fatalf("%d requests, want 8 (3, 3 and 2)", len(requests))
			}
			first := toolsField(t, requests[0].Body)
			for n, request := range requests {
				if request.Outcome != "ok" {
					t.Errorf("request %d: %s", n+1, request.Outcome)
				}
				if got := toolsField(t, request.Body); got != first {
					t.Errorf("request %d: the tool list changed while tools loaded\nfirst: %.300s\n  now: %.300s", n+1, first, got)
				}
			}
			var tools []map[string]any
			if err := json.Unmarshal([]byte(first), &tools); err != nil {
				t.Fatal(err)
			}
			deferred := map[string]bool{}
			for _, tool := range tools {
				name, _ := tool["name"].(string)
				if tool["type"] == "tool_search" {
					if tool["execution"] != "client" {
						t.Errorf("tool_search = %v", tool)
					}
					name = "tool_search"
				}
				deferred[name], _ = tool["defer_loading"].(bool)
			}
			for name, want := range map[string]bool{"orders_lookup": false, "shop_invoice": true, "carrier_track": true, "warehouse_stock": true} {
				if got, ok := deferred[name]; !ok || got != want {
					t.Errorf("%s: defer_loading = %v (listed %v), want %v", name, got, ok, want)
				}
			}
			if wire.name == fakegateway.APIAnthropic && deferred["search_tools"] || wire.name == fakegateway.APIResponses && deferred["tool_search"] {
				t.Errorf("the search tool is deferred: %v", deferred)
			}

			// The load shows in the second request's history, in the wire API's own form.
			second := string(requests[1].Body)
			switch wire.name {
			case fakegateway.APIAnthropic:
				if !strings.Contains(second, `"type":"tool_reference"`) && !strings.Contains(second, `"type": "tool_reference"`) {
					t.Errorf("no tool_reference in the second request: %.600s", second)
				}
				if !strings.Contains(second, `"tool_name":"shop_invoice"`) {
					t.Errorf("the reference does not name shop_invoice: %.600s", second)
				}
			case fakegateway.APIResponses:
				for _, want := range []string{`"type":"tool_search_call"`, `"type":"tool_search_output"`} {
					if !strings.Contains(second, want) {
						t.Errorf("no %s in the second request: %.600s", want, second)
					}
				}
			}
		})
	}
}
