package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/testkit"
)

func collectEvents(t *testing.T, stream gotato.ModelStream) ([]gotato.ModelEvent, error) {
	t.Helper()
	var events []gotato.ModelEvent
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

// API names a wire in the tests, so a table can be keyed by it.
type API string

const (
	APIChatCompletions   API = "chat"
	APIResponses         API = "responses"
	APIAnthropicMessages API = "anthropic"
)

var wireAPIs = []API{APIChatCompletions, APIResponses, APIAnthropicMessages}

// wireOf is the shipped wire value for an API name, with its default settings.
func wireOf(api API) WireAPI {
	switch api {
	case APIResponses:
		return Responses{}
	case APIAnthropicMessages:
		return AnthropicMessages{}
	}
	return ChatCompletions{}
}

// modelOf is a model named "m" with a stated window and the given facts.
func modelOf(facts gotato.ModelFacts) gotato.ModelConfig {
	if !facts.Window.Stated() {
		facts.Window = gotato.WindowTokens(200_000)
	}
	return gotato.ModelConfig{Name: "m", Facts: facts}
}

// endpointPaths are the paths the fake gateway serves each wire on.
var endpointPaths = map[API]string{
	APIChatCompletions:   "/v1/chat/completions",
	APIResponses:         "/v1/responses",
	APIAnthropicMessages: "/v1/messages",
}

// newClientFor builds a Client for a wire over a test endpoint, with a key and
// a short retry backoff unless opts say otherwise.
func newClientFor(t *testing.T, wire WireAPI, endpoint string, model gotato.ModelConfig, opts ...Option) *Client {
	t.Helper()
	client, err := New(wire, Connection{Endpoint: endpoint, APIKey: "k"}, model, append([]Option{WithRetry(Retry{Max: 2, Backoff: time.Millisecond, MaxWait: time.Second})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// textReply scripts, per wire API, a reply that answers "ok" and stops.
var textReply = map[API][]string{
	APIChatCompletions: {
		`{"choices":[{"delta":{"content":"ok"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	},
	APIResponses: {
		`{"type":"response.output_text.delta","output_index":0,"delta":"ok"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	},
	APIAnthropicMessages: {
		`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	},
}

func writeReply(t *testing.T, w http.ResponseWriter, lines []string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, line := range lines {
		writeSSE(t, w, line)
	}
}

// fakeWires are the three wires as the fake gateway serves them. The Anthropic
// and Responses models take back only the current Round's reasoning; the chat
// model takes all of it.
var fakeWires = []fakeWire{
	{fakegateway.APIAnthropic, AnthropicMessages{}, gotato.ModelConfig{Name: "claude-sonnet-5-5", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(200_000), RoundReasoning: true}}, endpointPaths[APIAnthropicMessages]},
	{fakegateway.APIResponses, Responses{}, gotato.ModelConfig{Name: "gpt-6.1-sol", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(200_000), RoundReasoning: true}}, endpointPaths[APIResponses]},
	{fakegateway.APIChat, ChatCompletions{}, gotato.ModelConfig{Name: "deepseek-reasoner", Facts: gotato.ModelFacts{Window: gotato.WindowTokens(200_000)}}, endpointPaths[APIChatCompletions]},
}

func newFake(t *testing.T, wire fakeWire, opts ...fakegateway.Option) (*fakegateway.Gateway, *Client) {
	t.Helper()
	fake := fakegateway.New(opts...)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, newClientFor(t, wire.wire, server.URL+wire.path, wire.model)
}

func newFakeSession(t *testing.T, client *Client, opts ...gotato.SessionOption) (*gotato.Session, *testkit.EventRecorder) {
	t.Helper()
	session, err := gotato.NewSession(context.Background(), append([]gotato.SessionOption{gotato.WithModel(client), gotato.WithTools(testkit.DemoEchoTool())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, testkit.RecordEvents(session)
}

type fakeWire struct {
	name  string
	wire  WireAPI
	model gotato.ModelConfig
	path  string
}

func drain(t *testing.T, stream gotato.ModelStream) []gotato.ModelEvent {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []gotato.ModelEvent
	for {
		event, err := stream.Recv(context.Background())
		if err != nil {
			return events
		}
		events = append(events, event)
	}
}

// dropConnection writes a chunked SSE response head and the given raw bytes,
// then closes the TCP connection without the terminating chunk, so the client
// reads io.ErrUnexpectedEOF: what a provider or proxy resetting mid-stream
// looks like.
func dropConnection(t *testing.T, w http.ResponseWriter, raw string) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer cannot hijack")
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
	if raw != "" {
		fmt.Fprintf(buf, "%x\r\n%s\r\n", len(raw), raw)
	}
	_ = buf.Flush()
}

func textOf(events []gotato.ModelEvent) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == gotato.ModelEventKindTextDelta {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

const roleChunk = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"

func writeSSE(t *testing.T, w http.ResponseWriter, data string) {
	t.Helper()
	// This runs on the httptest handler goroutine, where t.Fatal is invalid.
	if _, err := w.Write([]byte("data: " + data + "\n\n")); err != nil {
		t.Errorf("write SSE: %v", err)
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
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
func encodeWith(t *testing.T, api API, facts gotato.ModelFacts, messages []gotato.Message) string {
	t.Helper()
	request, _ := gotato.AssembleRequest(gotato.ModelContext{SystemInstructions: "sys", Messages: messages},
		[]gotato.ToolSpec{{ID: "echo", Name: "echo", InputSchema: []byte(`{"type":"object"}`)}})
	body, _, err := wireEncoders[api](modelOf(facts), request)
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

// wireEncoders encode a request as each wire does by default (native deferred
// tools where the wire has them).
var wireEncoders = map[API]func(gotato.ModelConfig, gotato.ModelRequest) ([]byte, map[string]string, error){
	APIChatCompletions: encodeChatRequest,
	APIResponses: func(model gotato.ModelConfig, request gotato.ModelRequest) ([]byte, map[string]string, error) {
		return encodeResponsesRequest(model, request, true)
	},
	APIAnthropicMessages: func(model gotato.ModelConfig, request gotato.ModelRequest) ([]byte, map[string]string, error) {
		return encodeAnthropicRequest(model, request, true)
	},
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

func echoCall(id string) gotato.ToolCall {
	return gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: "echo", Arguments: []byte(`{}`)}
}
