package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
)

// wireElements returns the elements of a request's conversation as JSON, one
// per message (Chat Completions), per item (Responses) or per content block
// with its role (Anthropic), without the cache_control marks, which move to the
// end by design.
func wireElements(t *testing.T, api string, body map[string]any) []string {
	t.Helper()
	var out []string
	add := func(v any) {
		b, err := json.Marshal(dropKey(v, "cache_control"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	switch api {
	case fakegateway.APIResponses:
		items, _ := body["input"].([]any)
		for _, item := range items {
			add(item)
		}
	case fakegateway.APIChat:
		messages, _ := body["messages"].([]any)
		for _, m := range messages {
			add(m)
		}
	default:
		messages, _ := body["messages"].([]any)
		for _, m := range messages {
			message, _ := m.(map[string]any)
			blocks, _ := message["content"].([]any)
			for _, block := range blocks {
				add(map[string]any{"role": message["role"], "block": block})
			}
		}
	}
	return out
}

func dropKey(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			if k != key {
				out[k] = dropKey(value, key)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = dropKey(x[i], key)
		}
		return out
	}
	return v
}

// reasoningElements counts the elements that carry the model's reasoning, in
// the wire API's own form: a thinking block, a reasoning item, a
// reasoning_content field.
func reasoningElements(api string, elements []string) int {
	n := 0
	for _, e := range elements {
		switch api {
		case fakegateway.APIAnthropic:
			if strings.Contains(e, `"type":"thinking"`) || strings.Contains(e, `"type":"redacted_thinking"`) {
				n++
			}
		case fakegateway.APIResponses:
			if strings.Contains(e, `"type":"reasoning"`) {
				n++
			}
		default:
			if strings.Contains(e, `"reasoning_content":"`) {
				n++
			}
		}
	}
	return n
}

// withoutReasoning is what the gateway leaves of an earlier Round's elements
// when a new Round begins on a provider that takes the current Round's reasoning
// only: the thinking blocks and reasoning items are gone, and so is the ID of a
// Responses function call, which names the output its reasoning came with.
func withoutReasoning(t *testing.T, api string, elements []string) []string {
	t.Helper()
	var out []string
	for _, e := range elements {
		var v map[string]any
		if err := json.Unmarshal([]byte(e), &v); err != nil {
			t.Fatal(err)
		}
		switch api {
		case fakegateway.APIAnthropic:
			if block, _ := v["block"].(map[string]any); block["type"] == "thinking" || block["type"] == "redacted_thinking" {
				continue
			}
		case fakegateway.APIResponses:
			if v["type"] == "reasoning" {
				continue
			}
			if v["type"] == "function_call" {
				delete(v, "id")
			}
		}
		b, _ := json.Marshal(v)
		out = append(out, string(b))
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFakeRequestOnlyGrowsAtItsEndWithReasoning checks GOALS.md G-G07 on the
// bytes the gateway sends when the replies carry reasoning, with the reasoning
// as each wire API's fake signs it. One Session plays four Runs, three of them
// with a tool loop, and every pair of consecutive requests is compared:
//
//   - inside a Round (a tool loop), the earlier request's elements come first
//     in the later one, byte for byte, reasoning included, and the reasoning is
//     still there;
//   - at a new Round, on a provider that takes the current Round's reasoning
//     only (Anthropic, OpenAI), the one change is that the earlier reasoning
//     is gone: what is left of the earlier elements comes first, byte for byte,
//     and no reasoning is sent. DeepSeek's wire API takes all of it back, so its
//     pairs never change;
//   - everything else in the body (the instruction, the tool list, the options,
//     the cache_control marks on both) is the same bytes in every pair.
func TestFakeRequestOnlyGrowsAtItsEndWithReasoning(t *testing.T) {
	roundOnly := map[string]bool{fakegateway.APIAnthropic: true, fakegateway.APIResponses: true}
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			session, _ := newFakeSession(t, client)
			prompts := []string{"use-tool [quirk:reasoning]", "hello [quirk:reasoning]", "use-tool [quirk:reasoning]", "use-tool [quirk:reasoning]"}
			newRound := map[int]bool{}
			for i, prompt := range prompts {
				if i > 0 {
					newRound[len(fake.Requests())] = true
				}
				result, err := session.Prompt(context.Background(), gotato.UserMessage(prompt))
				if err != nil || result.Status != gotato.RunCompleted {
					t.Fatalf("%q: %+v, %v", prompt, result, err)
				}
			}
			requests := fake.Requests()
			if len(requests) != 7 {
				t.Fatalf("%d requests, want 7", len(requests))
			}
			inLoop, atRound := 0, 0
			for n := 1; n < len(requests); n++ {
				prev, next := requests[n-1], requests[n]
				pair := fmt.Sprintf("requests %d and %d", n, n+1)
				if prev.Outcome != "ok" || next.Outcome != "ok" {
					t.Fatalf("%s: outcomes %s, %s", pair, prev.Outcome, next.Outcome)
				}
				for key, value := range prev.JSON {
					if key == "messages" && wire.name != fakegateway.APIResponses || key == "input" {
						continue
					}
					if a, b := mustJSON(t, value), mustJSON(t, next.JSON[key]); a != b {
						t.Errorf("%s: %q changed\nbefore: %.200s\n after: %.200s", pair, key, a, b)
					}
				}
				before, after := wireElements(t, wire.name, prev.JSON), wireElements(t, wire.name, next.JSON)
				if !newRound[n] {
					inLoop++
					if len(after) <= len(before) || !slices.Equal(before, after[:len(before)]) {
						t.Errorf("%s: inside a Round the earlier elements do not come first, byte for byte", pair)
					}
					if reasoningElements(wire.name, after) == 0 || reasoningElements(wire.name, after[:len(before)]) != reasoningElements(wire.name, before) {
						t.Errorf("%s: the Round's reasoning is not replayed as it was", pair)
					}
					continue
				}
				atRound++
				want := before
				if roundOnly[wire.name] {
					want = withoutReasoning(t, wire.name, before)
				}
				if len(after) <= len(want) || !slices.Equal(want, after[:len(want)]) {
					t.Errorf("%s: at a new Round the earlier elements, less their reasoning, do not come first\nwant: %v\n got: %v", pair, want, after)
				}
				if roundOnly[wire.name] && reasoningElements(wire.name, after) != 0 {
					t.Errorf("%s: a new Round still sends reasoning of an earlier Round", pair)
				}
				if !roundOnly[wire.name] && reasoningElements(wire.name, after) < reasoningElements(wire.name, before) {
					t.Errorf("%s: reasoning left the request", pair)
				}
			}
			if inLoop != 3 || atRound != 3 {
				t.Fatalf("%d pairs inside a Round and %d at a new Round, want 3 and 3", inLoop, atRound)
			}
		})
	}
}
