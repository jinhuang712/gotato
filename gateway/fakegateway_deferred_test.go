package gateway

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/internal/reftasks"
)

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
			wire.config.DeferredTools = DeferredFormNative
			fake, client := newFake(t, wire)
			if client.DeferredTools() != DeferredFormNative {
				t.Fatalf("form = %q", client.DeferredTools())
			}
			options := append([]gotato.SessionOption{gotato.WithModel(client), gotato.WithTools(reftasks.OrdersLookup())}, reftasks.ToolSearchOptions()...)
			session, err := gotato.NewSession(options...)
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
