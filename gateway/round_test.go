package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

var wireEncoders = map[API]func(string, gotato.ModelRequest, providerRules) ([]byte, map[string]string, error){
	APIChatCompletions:   encodeChatRequest,
	APIResponses:         encodeResponsesRequest,
	APIAnthropicMessages: encodeAnthropicRequest,
}

// reasoningPart is a reasoning Part as each wire API keeps it: its text, and
// the provider's own artifact where the wire API has one.
func reasoningPart(api API, label string) gotato.ContentPart {
	part := gotato.ContentPart{Kind: gotato.ContentKindReasoning, Text: "think-" + label}
	switch api {
	case APIAnthropicMessages:
		part.Signature = []byte(`{"type":"thinking","thinking":"think-` + label + `","signature":"sig-` + label + `"}`)
	case APIResponses:
		part.Signature = []byte(`{"type":"reasoning","id":"rs_` + label + `","encrypted_content":"enc-` + label + `","summary":[]}`)
	}
	return part
}

// sentReasoning is how a reasoning Part shows in an encoded request.
func sentReasoning(api API, label string) string {
	switch api {
	case APIAnthropicMessages:
		return "sig-" + label
	case APIResponses:
		return "enc-" + label
	default:
		return "think-" + label
	}
}

func reply(api API, label, text string, calls ...gotato.ToolCall) gotato.Message {
	message := gotato.Message{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{reasoningPart(api, label)}, ToolCalls: calls}
	if text != "" {
		message.Parts = append(message.Parts, gotato.ContentPart{Kind: gotato.ContentKindText, Text: text})
	}
	return message
}

func echoCall(id string) gotato.ToolCall {
	return gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: "echo", Arguments: []byte(`{}`)}
}

func echoResult(id string) gotato.Message {
	return gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: gotato.ToolCallID(id), Status: gotato.ToolResultStatusOK},
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "echoed"}}}
}

// twoRounds is a history in its second Round's tool loop. The first Round, a
// tool call and its answer, came before the last user Message; the second
// Round's tool call after it.
func twoRounds(api API) []gotato.Message {
	return []gotato.Message{
		gotato.UserMessage("q1"),
		reply(api, "r1", "", echoCall("c1")),
		echoResult("c1"),
		reply(api, "r2", "first answer"),
		gotato.UserMessage("q2"),
		reply(api, "r3", "", echoCall("c3")),
		echoResult("c3"),
	}
}

// encodeWith encodes messages as a Turn lays them out, with the cache
// markers and the tool a Session would send.
func encodeWith(t *testing.T, api API, rules providerRules, messages []gotato.Message) string {
	t.Helper()
	request, _ := gotato.AssembleRequest(gotato.ModelContext{SystemInstructions: "sys", Messages: messages},
		[]gotato.ToolSpec{{ID: "echo", Name: "echo", InputSchema: []byte(`{"type":"object"}`)}})
	body, _, err := wireEncoders[api]("m", request, rules)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func topLevel(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

// cacheControls returns every cache_control marker in an encoded request.
func cacheControls(t *testing.T, body string) []map[string]any {
	t.Helper()
	var document any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for key, child := range value {
				if marker, ok := child.(map[string]any); ok && key == "cache_control" {
					found = append(found, marker)
				}
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(document)
	return found
}

// Claude and OpenAI need back only the reasoning of the tool loop in
// progress. Sending every earlier Round's reasoning would keep it in the
// window, and on the bill, for as long as the Session lives, which can be
// weeks. Only the replies after the last user Message keep their reasoning.
func TestRecognizedProvidersSendOnlyTheCurrentRoundsReasoning(t *testing.T) {
	for _, tc := range []struct {
		provider Provider
		api      API
	}{
		{ProviderAnthropic, APIAnthropicMessages},
		{ProviderOpenAI, APIResponses},
		{ProviderOpenAI, APIChatCompletions},
	} {
		t.Run(string(tc.provider)+"/"+string(tc.api), func(t *testing.T) {
			rules := newProviderRules(tc.provider, tc.api, "m", 0, false)
			body := encodeWith(t, tc.api, rules, twoRounds(tc.api))
			for _, earlier := range []string{"r1", "r2"} {
				if strings.Contains(body, sentReasoning(tc.api, earlier)) {
					t.Errorf("a new Round still sends the earlier Round's reasoning %s:\n%s", earlier, body)
				}
			}
			if !strings.Contains(body, sentReasoning(tc.api, "r3")) {
				t.Errorf("the current Round's tool loop lost its reasoning:\n%s", body)
			}
			if !strings.Contains(body, "first answer") {
				t.Errorf("the earlier Round's answer is gone:\n%s", body)
			}
			loop := encodeWith(t, tc.api, rules, twoRounds(tc.api)[:3])
			if !strings.Contains(loop, sentReasoning(tc.api, "r1")) {
				t.Errorf("the first Round's tool loop lost its reasoning:\n%s", loop)
			}
		})
	}
}

// On Responses, an earlier Round's function call loses its item ID with its
// reasoning item, so no item names an output whose reasoning is gone.
func TestResponsesLeavesOutTheItemIDsOfEarlierRounds(t *testing.T) {
	messages := twoRounds(APIResponses)
	messages[1].ToolCalls = []gotato.ToolCall{{ID: "c1|fc_1", ToolID: "echo", Arguments: []byte(`{}`)}}
	messages[5].ToolCalls = []gotato.ToolCall{{ID: "c3|fc_3", ToolID: "echo", Arguments: []byte(`{}`)}}
	openai := encodeWith(t, APIResponses, newProviderRules(ProviderOpenAI, APIResponses, "m", 0, false), messages)
	if strings.Contains(openai, "fc_1") || !strings.Contains(openai, `"call_id":"c1"`) {
		t.Errorf("the earlier Round's call keeps its item ID, or lost its call:\n%s", openai)
	}
	if !strings.Contains(openai, "fc_3") {
		t.Errorf("the current Round's call lost its item ID:\n%s", openai)
	}
	if other := encodeWith(t, APIResponses, providerRules{}, messages); !strings.Contains(other, "fc_1") {
		t.Errorf("an unrecognized provider lost an item ID:\n%s", other)
	}
}

// DeepSeek refuses a request that leaves out any earlier reasoning_content
// once tools are in play, and a provider the gateway cannot recognize may do
// the same. Both get every reasoning artifact back, and no field or header the
// gateway adds only for a recognized provider.
func TestDeepSeekAndUnrecognizedProvidersGetEveryReasoningAndNothingElse(t *testing.T) {
	for _, provider := range []Provider{ProviderDeepSeek, ""} {
		for _, api := range wireAPIs {
			t.Run(string(provider)+"/"+string(api), func(t *testing.T) {
				for _, model := range []string{"claude-opus-5-5", "gpt-6.1-sol"} {
					body := encodeWith(t, api, newProviderRules(provider, api, model, 0, false), twoRounds(api))
					for _, label := range []string{"r1", "r2", "r3"} {
						if !strings.Contains(body, sentReasoning(api, label)) {
							t.Errorf("%s: reasoning %s is missing:\n%s", model, label, body)
						}
					}
					fields := topLevel(t, body)
					for _, field := range []string{"thinking", "prompt_cache_options", "prompt_cache_retention"} {
						if _, ok := fields[field]; ok {
							t.Errorf("%s: the request carries %s:\n%s", model, field, body)
						}
					}
					if strings.Contains(body, `"ttl"`) {
						t.Errorf("%s: the request carries a cache ttl:\n%s", model, body)
					}
				}
			})
		}
	}
}

// thinkingToolCall is an Anthropic reply that thinks, signs its thinking
// with signature, and calls echo.
func thinkingToolCall(signature string) []string {
	return []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"echo","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}
}

// failAfterOneTurn is a TurnPreparer that fails every Turn but the first, so a
// Run stops after one Turn with the Steer of that Turn already committed.
type failAfterOneTurn struct{}

func (failAfterOneTurn) PrepareTurn(_ context.Context, pending gotato.PendingTurn) (gotato.TurnPrepared, error) {
	if pending.Turn > 1 {
		return gotato.TurnPrepared{}, errors.New("stop after one Turn")
	}
	return gotato.TurnPrepared{}, nil
}

// A Steer is the user speaking, so it starts a new Round: the request after
// it no longer carries the reasoning before it. A Continue without a new
// prompt stays in the Round it resumes, so the tool loop it picks up keeps its
// reasoning. The first Run stops after one Turn, with the Steer committed at
// that Turn's end, and a Continue makes the next request.
func TestASteerStartsANewRoundAndAContinueDoesNot(t *testing.T) {
	for _, steer := range []bool{false, true} {
		var (
			mu     sync.Mutex
			bodies []string
			live   *gotato.Session
		)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(data))
			calls, target := len(bodies), live
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if calls == 1 {
				if steer {
					if err := target.Steer(gotato.UserMessage("also check b")); err != nil {
						t.Errorf("steer: %v", err)
					}
				}
				for _, event := range thinkingToolCall("sig-r1") {
					writeSSE(t, w, event)
				}
				return
			}
			for _, event := range textReply[APIAnthropicMessages] {
				writeSSE(t, w, event)
			}
		}))
		client, err := New(Config{API: APIAnthropicMessages, Provider: ProviderAnthropic, BaseURL: server.URL, APIKey: "k", Model: "claude-opus-4-6"})
		if err != nil {
			t.Fatal(err)
		}
		created, err := gotato.NewSession(context.Background(), gotato.WithModel(client), gotato.WithExtension(failAfterOneTurn{}), gotato.WithTools(testkit.NewFakeTool("echo", "echoed")))
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		live = created
		mu.Unlock()
		ctx := context.Background()
		first, _ := created.Prompt(ctx, gotato.UserMessage("check a"))
		if first.Status != gotato.RunStatusFailed {
			t.Fatalf("steer=%v: the first Run settled %q, want failed at its second Turn", steer, first.Status)
		}
		if _, err := created.Continue(ctx); err != nil {
			t.Fatal(err)
		}
		_ = created.Close(ctx)
		server.Close()
		if len(bodies) != 2 {
			t.Fatalf("steer=%v: %d requests, want 2", steer, len(bodies))
		}
		next := bodies[1]
		has := strings.Contains(next, "sig-r1")
		if steer && has {
			t.Errorf("the request after a Steer still sends the reasoning before it:\n%s", next)
		}
		if !steer && !has {
			t.Errorf("a Continue in the same Round lost its reasoning:\n%s", next)
		}
		if steer && !strings.Contains(next, "also check b") {
			t.Errorf("the Steer was not sent:\n%s", next)
		}
	}
}

// Only Fable 5.1, Opus 5.5, Sonnet 5.5 and Haiku 5.5 check the prefix a
// thinking block was written under, so only they are asked to drop a block
// whose prefix changed instead of refusing the request. block_binding lives
// in the thinking object, and "adaptive" is their default, so it changes
// nothing else. Another Claude model gets no thinking object: "adaptive"
// would turn thinking on for Opus 4.6 and be refused by Opus 4.5.
func TestAnthropicBindsThinkingOnlyWhereThePrefixIsChecked(t *testing.T) {
	const binding = `{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}`
	for _, tc := range []struct {
		model       string
		noDropBlock bool
		want        bool
	}{
		{"claude-fable-5-1", false, true},
		{"claude-opus-5-5", false, true},
		{"claude-sonnet-5-5", false, true},
		{"claude-haiku-5-5", false, true},
		{"claude-opus-5-5-20261001", false, true},
		{"claude-opus-5", false, false},
		{"claude-opus-4-6", false, false},
		{"claude-sonnet-4-5", false, false},
		{"an-alias", false, false},
		{"claude-opus-5-5", true, false},
	} {
		body := encodeWith(t, APIAnthropicMessages, newProviderRules(ProviderAnthropic, APIAnthropicMessages, tc.model, 0, tc.noDropBlock), twoRounds(APIAnthropicMessages))
		thinking, ok := topLevel(t, body)["thinking"]
		if ok != tc.want {
			t.Errorf("%s (no_drop_block=%v): thinking sent = %v, want %v", tc.model, tc.noDropBlock, ok, tc.want)
			continue
		}
		if ok && string(thinking) != binding {
			t.Errorf("%s: thinking = %s, want %s", tc.model, thinking, binding)
		}
	}
}

// Without the beta header, block_binding is refused with a 400. The header
// joins any anthropic-beta value the configuration already sends.
func TestAnthropicBindingSendsItsBetaHeader(t *testing.T) {
	for _, tc := range []struct {
		provider                Provider
		model, configured, want string
	}{
		{ProviderAnthropic, "claude-opus-5-5", "", anthropicBindingBeta},
		{ProviderAnthropic, "claude-opus-5-5", "files-api-2025-04-14", "files-api-2025-04-14," + anthropicBindingBeta},
		{ProviderAnthropic, "claude-opus-4-6", "", ""},
		{ProviderDeepSeek, "claude-opus-5-5", "", ""},
		{"", "claude-opus-5-5", "", ""},
	} {
		var got string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("anthropic-beta")
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range textReply[APIAnthropicMessages] {
				writeSSE(t, w, event)
			}
		}))
		headers := map[string]string{}
		if tc.configured != "" {
			headers["anthropic-beta"] = tc.configured
		}
		client, err := New(Config{API: APIAnthropicMessages, Provider: tc.provider, BaseURL: server.URL, APIKey: "k", Model: tc.model, Headers: headers})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("q")}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := collectEvents(t, stream); err != nil {
			t.Fatal(err)
		}
		server.Close()
		if got != tc.want {
			t.Errorf("%q %s: anthropic-beta = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// Leaving the last Round's reasoning out changes the request from its first
// thinking block on, so the cache entry at the end of the last request no
// longer matches. A marker on the last user Message before the end keeps a
// match: the Round's start, whose prefix every request since has shared.
func TestAnthropicMarksTheRoundsStartForTheCache(t *testing.T) {
	marked := func(body, text string) bool {
		t.Helper()
		var request anthropicRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		for _, message := range request.Messages {
			for _, block := range message.Content {
				if block["text"] == text {
					_, ok := block["cache_control"]
					return ok
				}
			}
		}
		t.Fatalf("no block with text %q", text)
		return false
	}
	claude := newProviderRules(ProviderAnthropic, APIAnthropicMessages, "claude-opus-4-6", 0, false)
	newRound := twoRounds(APIAnthropicMessages)[:5]
	if !marked(encodeWith(t, APIAnthropicMessages, claude, newRound), "q1") {
		t.Error("the first request of a Round does not mark the start of the Round before it")
	}
	if !marked(encodeWith(t, APIAnthropicMessages, claude, twoRounds(APIAnthropicMessages)), "q2") {
		t.Error("a request in a tool loop does not mark the start of its Round")
	}
	if marked(encodeWith(t, APIAnthropicMessages, providerRules{}, newRound), "q1") {
		t.Error("an unrecognized provider got a marker it does not need: its prefix only grows")
	}
}

// The request depends only on the Record and the configuration, so another
// instance that loads the Session sends the same bytes.
func TestTheRequestIsTheSameAfterAReload(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(data))
		calls := len(bodies)
		mu.Unlock()
		switch calls {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range thinkingToolCall("sig-r1") {
				writeSSE(t, w, event)
			}
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range textReply[APIAnthropicMessages] {
				writeSSE(t, w, event)
			}
		default:
			// The second Run stops at its first call, so the Record ends
			// with the prompt that call carried.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"stop here"}}`))
		}
	}))
	defer server.Close()
	client, err := New(Config{API: APIAnthropicMessages, Provider: ProviderAnthropic, BaseURL: server.URL, APIKey: "k", Model: "claude-opus-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	agents := []service.Agent{{Name: "a", Model: client, Instruction: "sys", Tools: []gotato.Tool{testkit.NewFakeTool("echo", "echoed")}}}
	store := session.NewMemoryStore()
	first, err := service.New(service.Config{Store: store, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run, err := first.Run(ctx, service.RunRequest{Agent: "a", Prompt: "q1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(ctx, service.RunRequest{SessionID: run.SessionID, Prompt: "q2"}); err == nil {
		t.Fatal("the second Run did not stop")
	}
	if len(bodies) != 3 {
		t.Fatalf("%d requests, want 3", len(bodies))
	}
	sent := bodies[2]
	if strings.Contains(sent, "sig-r1") {
		t.Fatalf("the second Round still sends the first Round's reasoning:\n%s", sent)
	}
	second, err := service.New(service.Config{Store: store, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	for name, runner := range map[string]*service.Runner{"the writing instance": first, "another instance": second} {
		report, err := runner.Inspect(ctx, run.SessionID, service.InspectConfig{})
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := encodeAnthropicRequest(client.model, report.Request, client.rules)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != sent {
			t.Errorf("%s builds another request:\n got %s\nsent %s", name, body, sent)
		}
	}
}
