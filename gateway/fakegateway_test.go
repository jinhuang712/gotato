package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/testkit"
)

// These tests point the gateway at internal/fakegateway, which speaks each wire
// API from the providers' public documentation, so they check the gateway's
// adaptation against the format and not against its own decoder.

type fakeWire struct {
	name   string
	config Config
}

var fakeWires = []fakeWire{
	{fakegateway.APIAnthropic, Config{API: APIAnthropicMessages, Provider: ProviderAnthropic, Model: "claude-sonnet-5-5"}},
	{fakegateway.APIResponses, Config{API: APIResponses, Provider: ProviderOpenAI, Model: "gpt-6.1-sol"}},
	{fakegateway.APIChat, Config{API: APIChatCompletions, Provider: ProviderDeepSeek, Model: "deepseek-reasoner"}},
}

func newFake(t *testing.T, wire fakeWire, opts ...fakegateway.Option) (*fakegateway.Gateway, *Client) {
	t.Helper()
	fake := fakegateway.New(opts...)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	config := wire.config
	config.BaseURL = server.URL
	config.APIKey = "k"
	config.RetryBackoff = time.Millisecond
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return fake, client
}

func newFakeSession(t *testing.T, client *Client, opts ...gotato.Option) (*gotato.Session, *testkit.EventRecorder) {
	t.Helper()
	session, err := gotato.NewSession(append([]gotato.Option{gotato.WithModel(client), gotato.WithTools(testkit.DemoEchoTool())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, testkit.RecordEvents(session)
}

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
		if event.Kind == gotato.ModelDone {
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
			if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "fake: hello" {
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
			if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "tool said: from-tool" {
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
				if event.Kind == gotato.ModelUsage {
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
			if !gerr.ContextOverflow() || gerr.Retryable || gerr.StatusCode != 400 {
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
				Messages: []gotato.Message{gotato.UserMessage("hi")}, Options: gotato.ModelOptions{MaxTokens: 99999}})
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
			if !gotato.IsCode(err, gotato.ErrContextOverflow) || result.Status != gotato.RunFailed {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if len(recorder.OfKind(gotato.KindReplyCutOff)) != 1 {
				t.Errorf("the text before the overflow is not kept as a cut-off reply: %v", recorder.Kinds())
			}
			if len(recorder.OfKind(gotato.KindModelRetried)) != 0 || len(fake.Requests()) != 1 {
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
				if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != tc.want {
					t.Fatalf("result = %+v, err = %v", result, err)
				}
				if got := len(recorder.OfKind(gotato.KindModelRetried)); got != 1 {
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
			if err != nil || result.Status != gotato.RunCompleted || gotato.TextOf(*result.FinalMessage) != "tool said: from-tool" {
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
					if part.Kind != gotato.ContentReasoning {
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
			notice := gotato.NewNotice(gotato.NoticeFromService, "time_away", "You were away for an hour.")
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
