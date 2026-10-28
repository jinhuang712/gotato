package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/testkit"
)

// fallbackReply scripts, per wire API, a reply in which the provider reports
// another model than the one requested, as a gateway with fallbacks does.
var fallbackReply = map[API][]string{
	APIChatCompletions: {
		`{"model":"fallback-model","choices":[{"delta":{"content":"ok"}}]}`,
		`{"model":"fallback-model","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	},
	APIResponses: {
		`{"type":"response.created","response":{"id":"r1","status":"in_progress","model":"fallback-model"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"ok"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		`{"type":"response.completed","response":{"id":"r1","status":"completed","model":"fallback-model"}}`,
	},
	APIAnthropicMessages: {
		`{"type":"message_start","message":{"model":"fallback-model","usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	},
}

// The model a provider reports is recorded on the reply, so a Session behind
// a gateway knows which model answered, and it is never sent back.
func TestReplyRecordsTheModelThatAnswered(t *testing.T) {
	for _, api := range wireAPIs {
		t.Run(string(api), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeReply(t, w, fallbackReply[api])
			}))
			defer server.Close()
			client, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "requested-model", MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			session, err := gotato.NewSession(context.Background(), gotato.WithModel(client))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), gotato.UserMessage("go"))
			if err != nil {
				t.Fatal(err)
			}
			if result.FinalMessage == nil || result.FinalMessage.Model != "fallback-model" {
				t.Fatalf("reply = %+v, want Model fallback-model", result.FinalMessage)
			}
		})
	}
}

// Gemini 3 and later refuse a function call of the current Round that comes
// back without its thought signature. On the chat API the signature is in
// the call's extra_content, which here arrives in a later delta than the
// call's ID, and only the first of parallel calls has one. The call keeps
// it, and sends it back as it came.
func TestChatKeepsAndSendsBackACallsExtraContent(t *testing.T) {
	const extra = `{"google":{"thought_signature":"sig-A"}}`
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
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			for _, chunk := range []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"},"extra_content":` + extra + `}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"echo","arguments":"{}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			} {
				writeSSE(t, w, chunk)
			}
			return
		}
		for _, chunk := range textReply[APIChatCompletions] {
			writeSSE(t, w, chunk)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "gemini-3-pro"})
	if err != nil {
		t.Fatal(err)
	}
	live, err := gotato.NewSession(context.Background(), gotato.WithModel(client), gotato.WithTools(testkit.NewFakeTool("echo", "echoed")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close(context.Background()) }()
	if _, err := live.Prompt(context.Background(), gotato.UserMessage("q")); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d requests, want 2", len(bodies))
	}
	var second struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID           string          `json:"id"`
				ExtraContent json.RawMessage `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatal(err)
	}
	var calls []struct {
		ID           string          `json:"id"`
		ExtraContent json.RawMessage `json:"extra_content"`
	}
	for _, message := range second.Messages {
		if message.Role == "assistant" {
			calls = message.ToolCalls
		}
	}
	if len(calls) != 2 {
		t.Fatalf("replayed calls = %+v\n%s", calls, bodies[1])
	}
	if string(calls[0].ExtraContent) != extra {
		t.Errorf("the first call's extra_content = %s, want %s", calls[0].ExtraContent, extra)
	}
	if calls[1].ExtraContent != nil {
		t.Errorf("the second call got extra_content %s it never had", calls[1].ExtraContent)
	}
}

// ToolCall.Signature belongs to the wire API that streamed the call. A
// Session that moves to another wire API must not send one wire API's
// artifact as the other's: a Responses item ID is not extra_content, and
// extra_content is not an item ID.
func TestACallsSignatureStaysWithItsWireAPI(t *testing.T) {
	request := func(signature string) gotato.ModelRequest {
		return gotato.ModelRequest{Messages: []gotato.Message{
			gotato.UserMessage("q"),
			{Role: gotato.RoleAssistant, ToolCalls: []gotato.ToolCall{{ID: "call_1", ToolID: "echo", Arguments: []byte(`{}`), Signature: []byte(signature)}}},
			echoResult("call_1"),
		}}
	}
	chat, _, err := encodeChatRequest("m", request("fc_1"), providerRules{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(chat), "extra_content") || strings.Contains(string(chat), "fc_1") {
		t.Errorf("a Responses item ID went out on the chat API:\n%s", chat)
	}
	responses, _, err := encodeResponsesRequest("m", request(`{"google":{"thought_signature":"sig-A"}}`), providerRules{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(responses), "sig-A") {
		t.Errorf("chat extra_content went out as a Responses item ID:\n%s", responses)
	}
}

// A finish reason the decoder does not know, and a call it cannot name, fail
// the stream with model_protocol_failure; neither is a clean end of turn.
func TestUnknownFinishAndUnnamedCallFailTheStream(t *testing.T) {
	cases := []struct {
		name   string
		api    API
		frames []string
	}{
		{"chat unknown finish_reason", APIChatCompletions, []string{
			`{"choices":[{"delta":{"content":"par"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"insufficient_system_resource"}]}`, `[DONE]`}},
		{"anthropic pause_turn", APIAnthropicMessages, []string{
			`{"type":"message_start","message":{"usage":{}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`}},
		{"anthropic unknown stop_reason", APIAnthropicMessages, []string{
			`{"type":"message_start","message":{"usage":{}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"brand_new"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`}},
		{"responses unknown status", APIResponses, []string{
			`{"type":"response.completed","response":{"status":"queued"}}`}},
		{"responses call without call_id", APIResponses, []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"echo"}}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{}"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`}},
		{"responses call without name", APIResponses, []string{
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, frame := range tc.frames {
					writeSSE(t, w, frame)
				}
			}))
			defer server.Close()
			client, err := New(Config{API: tc.api, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1, RetryBackoff: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			events, err := collectEvents(t, stream)
			if !gotato.IsCode(err, gotato.CodeModelProtocolFailure) {
				t.Fatalf("err = %v, want model_protocol_failure; events = %v", err, events)
			}
			for _, event := range events {
				if event.Kind == gotato.ModelEventKindDone {
					t.Fatalf("the stream ended cleanly: %+v", event)
				}
			}
		})
	}
}

// A server that sends "finish_reason": "" on a chunk that is not the last has
// not finished: a stream cut after such a chunk is cut, not a complete reply.
func TestEmptyFinishReasonIsNotAFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"The answer is fo"},"finish_reason":""}]}`+"\n\n")
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Model: "m", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(t, stream)
	var coded *Error
	if !errors.As(err, &coded) || !coded.Retryable() {
		t.Fatalf("err = %v after %d events, want a retryable cut-stream Error", err, len(events))
	}
	for _, event := range events {
		if event.Kind == gotato.ModelEventKindDone {
			t.Fatalf("a stream cut after an empty finish_reason ended as Done %q", event.StopReason)
		}
	}
}

// These tests point the gateway at internal/fakegateway, which speaks each wire
// API from the providers' public documentation, so they check the gateway's
// adaptation against the format and not against its own decoder.

// drainFake reads a stream to its end and returns what it said and how it ended.
func drainFake(t *testing.T, client *Client, request gotato.ModelRequest) ([]gotato.ModelEvent, error) {
	t.Helper()
	stream, err := client.Stream(context.Background(), request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	var events []gotato.ModelEvent
	for {
		event, err := stream.Recv(context.Background())
		if err != nil {
			return events, err
		}
		events = append(events, event)
		if event.Kind == gotato.ModelEventKindDone {
			return events, nil
		}
	}
}

func asGatewayError(t *testing.T, err error) *Error {
	t.Helper()
	var gerr *Error
	if !errors.As(err, &gerr) {
		t.Fatalf("err = %v, want a *gateway.Error", err)
	}
	return gerr
}

func TestFakePlainReplyOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			session, _ := newFakeSession(t, client)
			result, err := session.Prompt(context.Background(), gotato.UserMessage("hello"))
			if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != "fake: hello" {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if got := fake.Requests(); len(got) != 1 || got[0].Outcome != "ok" || got[0].UserText != "hello" {
				t.Fatalf("requests = %+v", got)
			}
		})
	}
}

func TestFakeToolRoundTripOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			session, _ := newFakeSession(t, client)
			result, err := session.Prompt(context.Background(), gotato.UserMessage("use-tool"))
			if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != "tool said: from-tool" {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if got := fake.Requests(); len(got) != 2 {
				t.Fatalf("%d requests, want the call and the answer", len(got))
			}
		})
	}
}

func TestFakeCachedTokensAreCountedFromEachProvidersField(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			_, client := newFake(t, wire)
			prompt := strings.Repeat("a long prompt ", 400) + "[quirk:cached=1200]"
			events, err := drainFake(t, client, gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage(prompt)}})
			if err != nil {
				t.Fatal(err)
			}
			var usage gotato.Usage
			for _, event := range events {
				if event.Kind == gotato.ModelEventKindUsage {
					usage = event.Usage
				}
			}
			if usage.CacheReadTokens != 1200 || usage.InputTokens < usage.CacheReadTokens || usage.OutputTokens == 0 {
				t.Fatalf("usage = %+v", usage)
			}
			t.Logf("%s: %+v", wire.name, usage)
		})
	}
}

func TestFakeRefusalAsTooLongIsContextOverflowOnEachWireAPI(t *testing.T) {
	limits := map[string]int{fakegateway.APIAnthropic: 200000, fakegateway.APIResponses: 0, fakegateway.APIChat: 200000}
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			_, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi [quirk:overflow]")}})
			gerr := asGatewayError(t, err)
			if !gerr.ContextOverflow() || gerr.Retryable() || gerr.StatusCode != 400 {
				t.Fatalf("error = %+v", gerr)
			}
			if gerr.ContextLimit() != limits[wire.name] {
				t.Errorf("ContextLimit = %d, want %d (%q)", gerr.ContextLimit(), limits[wire.name], gerr.Message)
			}
			if n := len(fake.Requests()); n != 1 {
				t.Fatalf("%d requests: a refusal is not retried", n)
			}
		})
	}
}

func TestFakeMaxTokensAloneTooLargeIsNotAnOverflow(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			_, client := newFake(t, wire, fakegateway.WithMaxOutput(1000))
			_, err := client.Stream(context.Background(), gotato.ModelRequest{
				Messages: []gotato.Message{gotato.UserMessage("hi")}, Config: gotato.ModelConfig{MaxTokens: 99999}})
			gerr := asGatewayError(t, err)
			if gerr.ContextOverflow() || gerr.StatusCode != 400 || !strings.Contains(gerr.Message, "99999") {
				t.Fatalf("error = %+v", gerr)
			}
		})
	}
}

func TestFakeRequestPastTheWindowIsContextOverflow(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			_, client := newFake(t, wire, fakegateway.WithWindow(200))
			_, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage(strings.Repeat("word ", 400))}})
			if gerr := asGatewayError(t, err); !gerr.ContextOverflow() {
				t.Fatalf("error = %+v", gerr)
			}
		})
	}
}

func TestFakeMidstreamOverflowEndsTheRunWithContextOverflowAndKeepsTheText(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			session, recorder := newFakeSession(t, client)
			result, err := session.Prompt(context.Background(), gotato.UserMessage("an answer that runs out [quirk:overflow-midstream]"))
			if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Status != gotato.RunStatusFailed {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if len(recorder.OfKind(gotato.EventKindReplyCutOff)) != 1 {
				t.Errorf("the text before the overflow is not kept as a cut-off reply: %v", recorder.Kinds())
			}
			if len(recorder.OfKind(gotato.EventKindModelRetried)) != 0 || len(fake.Requests()) != 1 {
				t.Errorf("an overflow was retried: %d requests", len(fake.Requests()))
			}
		})
	}
}

func TestFakeResponsesFailedEventWithAnOverflowCodeIsAnOverflow(t *testing.T) {
	_, client := newFake(t, fakeWires[1])
	_, err := drainFake(t, client, gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi [quirk:overflow-midstream=failed]")}})
	var overflow interface{ ContextOverflow() bool }
	if !errors.As(err, &overflow) || !overflow.ContextOverflow() {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeStreamCutAfterTextAndInArgumentsIsRetriedAndSucceeds(t *testing.T) {
	for _, wire := range fakeWires {
		for _, tc := range []struct{ name, prompt, want string }{
			{"after text", "a question [quirk:cut-after-text]", "fake: a question"},
			{"in tool arguments", "use-tool [quirk:cut-in-args]", "tool said: from-tool"},
		} {
			t.Run(wire.name+"/"+tc.name, func(t *testing.T) {
				fake, client := newFake(t, wire)
				session, recorder := newFakeSession(t, client)
				result, err := session.Prompt(context.Background(), gotato.UserMessage(tc.prompt))
				if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != tc.want {
					t.Fatalf("result = %+v, err = %v", result, err)
				}
				if got := len(recorder.OfKind(gotato.EventKindModelRetried)); got != 1 {
					t.Fatalf("model_retried Events = %d: %v", got, recorder.Kinds())
				}
				requests := fake.Requests()
				if requests[0].Outcome != "cut" || string(requests[0].Body) != string(requests[1].Body) {
					t.Fatalf("the cut request was not sent again unchanged: %v / %v", requests[0].Outcome, requests[1].Outcome)
				}
			})
		}
	}
}

// A reasoning reply is kept by the gateway and replayed in the tool loop, and
// the fake, which refuses a request that drops what the provider requires,
// accepts it. The same loop without the reasoning is refused.
func TestFakeReasoningIsReplayedAndTheFakeRefusesARequestThatDropsIt(t *testing.T) {
	refusals := map[string]string{
		fakegateway.APIAnthropic: "Expected `thinking` or `redacted_thinking`",
		fakegateway.APIResponses: "without its required 'reasoning' item",
		fakegateway.APIChat:      "`reasoning_content` in the thinking mode must be passed back",
	}
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			session, _ := newFakeSession(t, client)
			prompt := "use-tool [quirk:reasoning]"
			result, err := session.Prompt(context.Background(), gotato.UserMessage(prompt))
			if err != nil || result.Status != gotato.RunStatusCompleted || gotato.TextOf(*result.FinalMessage) != "tool said: from-tool" {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if !strings.Contains(gotato.ReasoningOf(session.Transcript().Messages()[1]), "thinking about: use-tool") {
				t.Fatalf("reasoning was not kept: %+v", session.Transcript().Messages()[1])
			}
			// The same loop, with the reasoning left out of the replay.
			messages := session.Transcript().Messages()
			var stripped []gotato.Message
			for _, message := range messages[:3] {
				kept := message
				kept.Parts = nil
				for _, part := range message.Parts {
					if part.Kind != gotato.ContentKindReasoning {
						kept.Parts = append(kept.Parts, part)
					}
				}
				stripped = append(stripped, kept)
			}
			_, err = client.Stream(context.Background(), gotato.ModelRequest{Messages: stripped, Tools: session.Tools()})
			if gerr := asGatewayError(t, err); gerr.StatusCode != 400 || !strings.Contains(gerr.Message, refusals[wire.name]) {
				t.Fatalf("a request without the reasoning: %v", err)
			}
			if n := len(fake.Requests()); n != 3 {
				t.Fatalf("%d requests", n)
			}
		})
	}
}

// Gotato sends a notice as a user turn on every wire API, so the fake sees
// no system or developer message in the middle, and accepts the user turns, two in a row on the way.
func TestFakeNoticeGoesAsAUserTurnOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeWires {
		t.Run(wire.name, func(t *testing.T) {
			fake, client := newFake(t, wire)
			notice := gotato.NewNotice(gotato.NoticeSourceService, "time_away", "You were away for an hour.")
			_, err := drainFake(t, client, gotato.ModelRequest{SystemInstructions: "be brief", Messages: gotato.ForModel([]gotato.Message{
				gotato.UserMessage("first"), gotato.AssistantMessage("answer"), gotato.UserMessage("second"), notice})})
			if err != nil {
				t.Fatal(err)
			}
			got := fake.Requests()[0]
			if len(got.MidSystem) != 0 || !strings.Contains(fmt.Sprint(got.JSON), `<notice kind="time_away">`) || got.UserText != "second" {
				t.Fatalf("mid-conversation %v, user text %q, body %s", got.MidSystem, got.UserText, got.Body)
			}
		})
	}
}

// An error the provider sends in place of a chunk is an error, not a stream
// that was cut: it is not sent again, and one that names an overflow says so.
func TestChatErrorInTheMiddleOfAStreamIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"The server had an error\",\"type\":\"server_error\",\"code\":null}}\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m", RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainFake(t, client, gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	var overflow interface{ ContextOverflow() bool }
	if err == nil || !strings.Contains(err.Error(), "The server had an error") || errors.As(err, &overflow) && overflow.ContextOverflow() || calls.Load() != 1 {
		t.Fatalf("err = %v after %d requests", err, calls.Load())
	}
}
