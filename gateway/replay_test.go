package gateway

import (
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

var wireAPIs = []string{APIChatCompletions, APIResponses, APIAnthropicMessages}

// badToolCallReply scripts, per wire API, a reply whose one Tool Call carries
// arguments that are not valid JSON, as a call the output limit cut off does.
var badToolCallReply = map[string][]string{
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

// textReply scripts, per wire API, a reply that answers "ok" and stops.
var textReply = map[string][]string{
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

// replayedArguments returns the arguments of every Tool Call an encoded
// request sends back, in the form each wire API carries them.
func replayedArguments(t *testing.T, api string, body []byte) []string {
	t.Helper()
	var arguments []string
	switch api {
	case APIChatCompletions:
		var request wireRequest
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
		t.Run(api, func(t *testing.T) {
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
			client, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "m", NoRetries: true})
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
			agent, err := gotato.NewSession(gotato.WithModel(client), gotato.WithTools(echo))
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
	encoders := map[string]func(string, gotato.ModelRequest, providerRules) ([]byte, map[string]string, error){
		APIChatCompletions:   encodeRequest,
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
