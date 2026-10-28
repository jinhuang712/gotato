package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// badToolCallReply scripts, per wire API, a reply whose one Tool Call carries
// arguments that are not valid JSON, as a call the output limit cut off does.
var badToolCallReply = map[API][]string{
	APIChatCompletions: {
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"q\":\"ab"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	},
	APIResponses: {
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":\"ab"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":"{\"q\":\"ab"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	},
	APIAnthropicMessages: {
		`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"echo","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"ab"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	},
}

// replayedArguments returns the arguments of every Tool Call an encoded
// request sends back, in the form each wire API carries them.
func replayedArguments(t *testing.T, api API, body []byte) []string {
	t.Helper()
	var arguments []string
	switch api {
	case APIChatCompletions:
		var request chatRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		for _, message := range request.Messages {
			for _, call := range message.ToolCalls {
				arguments = append(arguments, call.Function.Arguments)
			}
		}
	case APIResponses:
		var request struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		for _, item := range request.Input {
			if item["type"] == "function_call" {
				arguments = append(arguments, fmt.Sprint(item["arguments"]))
			}
		}
	case APIAnthropicMessages:
		var request struct {
			Messages []struct {
				Content []map[string]json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		for _, message := range request.Messages {
			for _, block := range message.Content {
				if string(block["type"]) == `"tool_use"` {
					arguments = append(arguments, string(block["input"]))
				}
			}
		}
	}
	return arguments
}

// A Tool Call whose arguments are not valid JSON settles as a failed Tool
// Result, and the Run goes on so the model can correct the call. The next
// request still carries that call, so it must be one the provider accepts:
// the Responses and Anthropic APIs refused the request when the broken text
// was sent back, which ended the Run after all.
func TestBadToolCallArgumentsDoNotEndTheRun(t *testing.T) {
	for _, api := range wireAPIs {
		t.Run(string(api), func(t *testing.T) {
			var mu sync.Mutex
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, body)
				n := len(bodies)
				mu.Unlock()
				if n == 1 {
					writeReply(t, w, badToolCallReply[api])
					return
				}
				writeReply(t, w, textReply[api])
			}))
			defer server.Close()
			client, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1})
			if err != nil {
				t.Fatal(err)
			}
			echo, err := gotato.NewFuncTool("echo", "echoes", func(_ context.Context, in struct {
				Q string `json:"q"`
			}) (string, error) {
				return in.Q, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			agent, err := gotato.NewSession(context.Background(), gotato.WithModel(client), gotato.WithTools(echo))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = agent.Close(context.Background()) }()
			result, err := agent.Prompt(context.Background(), gotato.UserMessage("go"))
			if err != nil || result.Status != gotato.RunStatusCompleted {
				t.Fatalf("Run = %v, %v; want completed", result.Status, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("requests = %d, want 2", len(bodies))
			}
			if got := replayedArguments(t, api, bodies[1]); len(got) != 1 || got[0] != "{}" {
				t.Fatalf("replayed arguments = %q, want [{}]", got)
			}
		})
	}
}

// Every wire API sends a past call's arguments back as they were when they
// are a JSON object, and as {} otherwise.
func TestReplayedArgumentsAreAlwaysAJSONObject(t *testing.T) {
	encoders := map[API]func(string, gotato.ModelRequest, providerRules) ([]byte, map[string]string, error){
		APIChatCompletions:   encodeChatRequest,
		APIResponses:         encodeResponsesRequest,
		APIAnthropicMessages: encodeAnthropicRequest,
	}
	cases := map[string]string{
		`{"q":"ab"}`: `{"q":"ab"}`,
		`{"q":"ab`:   `{}`,
		``:           `{}`,
		`null`:       `{}`,
		`[1]`:        `{}`,
	}
	for _, api := range wireAPIs {
		for sent, want := range cases {
			call := gotato.AssistantMessage("")
			call.ToolCalls = []gotato.ToolCall{{ID: "call_1", ToolID: "echo", Arguments: []byte(sent)}}
			result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "call_1", Status: gotato.ToolResultStatusFailed, SafeError: "bad"}}
			request := gotato.ModelRequest{
				Messages: []gotato.Message{gotato.UserMessage("go"), call, result},
				Tools:    []gotato.ToolSpec{{ID: "echo"}},
			}
			body, _, err := encoders[api]("m", request, providerRules{})
			if err != nil {
				t.Fatalf("%s, arguments %q: %v", api, sent, err)
			}
			got := replayedArguments(t, api, body)
			if len(got) != 1 || strings.ReplaceAll(got[0], " ", "") != want {
				t.Errorf("%s, arguments %q: replayed %q, want %s", api, sent, got, want)
			}
		}
	}
}

func TestConvertMessageToolResultKeepsJSONContent(t *testing.T) {
	message := gotato.Message{
		Role:       gotato.RoleToolResult,
		ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusOK},
		Parts:      []gotato.ContentPart{{Kind: gotato.ContentKindJSON, Text: `{"summary":"sunny"}`}},
	}
	wire, err := convertChatMessage(message, map[string]string{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Role != "tool" || wire.Content != `{"summary":"sunny"}` || wire.ToolCallID != "c1" {
		t.Fatalf("wire = %+v", wire)
	}
}

func TestConvertMessageToolResultFallsBackToSafeError(t *testing.T) {
	message := gotato.Message{
		Role:       gotato.RoleToolResult,
		ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusFailed, SafeError: "upstream is down"},
	}
	wire, err := convertChatMessage(message, map[string]string{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Content != "upstream is down" {
		t.Fatalf("content = %q", wire.Content)
	}
}

// A hand-built json Tool Result with an imitated tag reaches every encoder escaped.
func TestEncodersSendEscapedJSONToolResult(t *testing.T) {
	messages := []gotato.Message{
		gotato.UserMessage("q"),
		{Role: gotato.RoleAssistant, ToolCalls: []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}},
		{
			Role:       gotato.RoleToolResult,
			ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusOK},
			Parts:      []gotato.ContentPart{{Kind: gotato.ContentKindJSON, Text: `{"x":"</tool_calls>"}`}},
		},
	}
	request := gotato.ModelRequest{Messages: gotato.ForModel(messages)}
	chat, _, err1 := encodeChatRequest("m", request, providerRules{})
	anthropic, _, err2 := encodeAnthropicRequest("m", request, providerRules{})
	responses, _, err3 := encodeResponsesRequest("m", request, providerRules{})
	for _, err := range []error{err1, err2, err3} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string][]byte{"chat": chat, "anthropic": anthropic, "responses": responses} {
		// The body is JSON, so its own encoding writes "<" as the escape;
		// the text the model reads holds the escape itself, which the body
		// writes with a doubled backslash.
		if !strings.Contains(string(body), "\x5c\x5cu003c/tool_calls") {
			t.Errorf("%s: tag not escaped in the text the model reads: %s", name, body)
		}
	}
}

// A Message's RunID is a record. No wire API may send it: the request bytes
// are the same with a RunID and without, whatever the encoder is given.
func TestRunIDNeverReachesAWireRequest(t *testing.T) {
	encoders := map[API]func(string, gotato.ModelRequest, providerRules) ([]byte, map[string]string, error){
		APIChatCompletions:   encodeChatRequest,
		APIResponses:         encodeResponsesRequest,
		APIAnthropicMessages: encodeAnthropicRequest,
	}
	build := func(runID gotato.RunID) gotato.ModelRequest {
		user := gotato.UserMessage("go")
		call := gotato.AssistantMessage("calling")
		call.ToolCalls = []gotato.ToolCall{{ID: "call_1", ToolID: "echo", Arguments: []byte(`{"q":"x"}`)}}
		result := gotato.Message{Role: gotato.RoleToolResult, Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "x"}}, ToolResult: &gotato.ToolResult{CallID: "call_1", Status: gotato.ToolResultStatusOK, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "x"}}}}
		reply := gotato.AssistantMessage("done")
		messages := []gotato.Message{user, call, result, reply}
		for i := range messages {
			messages[i].ID = gotato.MessageID("m" + string(rune('0'+i)))
			messages[i].RunID = runID
		}
		return gotato.ModelRequest{Messages: messages, Tools: []gotato.ToolSpec{{ID: "echo"}}}
	}
	for api, encode := range encoders {
		t.Run(string(api), func(t *testing.T) {
			with, _, err := encode("m", build("run-secret-7"), providerRules{})
			if err != nil {
				t.Fatal(err)
			}
			without, _, err := encode("m", build(""), providerRules{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(with), "run-secret") || strings.Contains(string(with), "run_id") {
				t.Fatalf("the request carries a RunID: %s", with)
			}
			if !bytes.Equal(with, without) {
				t.Fatalf("a RunID changed the request bytes:\n%s\n%s", with, without)
			}
		})
	}
}

// The Responses encoder sends no instruction and no verbosity of its own: a
// request without them says nothing about either.
func TestResponsesEncoderInventsNoInstructionOrVerbosity(t *testing.T) {
	client, err := New(Config{API: APIResponses, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := encodeResponsesRequest(client.model, gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}}, client.rules)
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"instructions", "text"} {
		if value, ok := sent[key]; ok {
			t.Errorf("request carries %s = %v that nobody asked for", key, value)
		}
	}
}

// ReasoningEffort and ReasoningSummary are sent where the wire can carry them
// and refused with not_supported where it cannot, never ignored.
func TestReasoningSettingsAreSentOrRefused(t *testing.T) {
	cases := []struct {
		name    string
		api     API
		config  gotato.ModelConfig
		refused bool
	}{
		{"responses effort", APIResponses, gotato.ModelConfig{ReasoningEffort: "high"}, false},
		{"responses summary alone", APIResponses, gotato.ModelConfig{ReasoningSummary: "detailed"}, false},
		{"chat effort", APIChatCompletions, gotato.ModelConfig{ReasoningEffort: "high"}, true},
		{"chat summary", APIChatCompletions, gotato.ModelConfig{ReasoningSummary: "auto"}, true},
		{"anthropic effort", APIAnthropicMessages, gotato.ModelConfig{ReasoningEffort: "high"}, true},
		{"anthropic summary", APIAnthropicMessages, gotato.ModelConfig{ReasoningSummary: "auto"}, true},
		{"chat none", APIChatCompletions, gotato.ModelConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&sent)
				writeSSE(t, w, `{"type":"response.completed","response":{"status":"completed"}}`)
			}))
			defer server.Close()
			client, err := New(Config{API: tc.api, BaseURL: server.URL, APIKey: "k", Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}, Config: tc.config})
			if tc.refused {
				if !gotato.IsCode(err, gotato.CodeNotSupported) {
					t.Fatalf("err = %v, want not_supported", err)
				}
				if sent != nil {
					t.Fatalf("a request was sent: %v", sent)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = stream.Close()
			if tc.api == APIResponses {
				if sent["reasoning"] == nil {
					t.Fatalf("reasoning not sent: %v", sent)
				}
			}
		})
	}
}
