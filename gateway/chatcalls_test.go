package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// streamedCalls plays chunks as a chat stream and returns the Tool calls the
// gateway made of them.
func streamedCalls(t *testing.T, chunks ...string) []gotato.ToolCall {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			writeSSE(t, w, chunk)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("q")}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := collectEvents(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	var calls []gotato.ToolCall
	for _, event := range events {
		if event.Kind == gotato.ModelEventKindToolCall {
			calls = append(calls, *event.ToolCall)
		}
	}
	return calls
}

// Gemini's chat API streams parallel calls whole, each with its own id, but
// without an index, with a null one, or all at index 0. Merged by index, two
// calls became one with the second's id, both names and both argument
// strings, which is not JSON. A delta that names another id than the call at
// its index starts a new call, and one without an index continues the last
// call. A provider that keeps to the chat API's indexes is read as before.
func TestChatSplitsParallelCallsThatShareOrLackAnIndex(t *testing.T) {
	const done = `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`
	signed := `"extra_content":{"google":{"thought_signature":"sig-A"}}`
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{"no index, one chunk each", []string{
			`{"choices":[{"delta":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"echo","arguments":"{\"q\":\"a\"}"},` + signed + `}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"call_b","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"b\"}"}}]}}]}`,
			done, `[DONE]`,
		}},
		{"null index, one chunk", []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":null,"id":"call_a","type":"function","function":{"name":"echo","arguments":"{\"q\":\"a\"}"},` + signed + `},{"index":null,"id":"call_b","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"b\"}"}}]}}]}`,
			done, `[DONE]`,
		}},
		{"every call at index 0", []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"echo","arguments":"{\"q\":\"a\"}"},` + signed + `}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"b\"}"}}]}}]}`,
			done, `[DONE]`,
		}},
		{"indexes kept, arguments in pieces", []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"echo","arguments":""},` + signed + `}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"b\"}"}}]}}]}`,
			done, `[DONE]`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := streamedCalls(t, tc.chunks...)
			if len(calls) != 2 {
				t.Fatalf("calls = %+v, want 2", calls)
			}
			want := []struct{ id, tool, arguments, signature string }{
				{"call_a", "echo", `{"q":"a"}`, `{"google":{"thought_signature":"sig-A"}}`},
				{"call_b", "lookup", `{"q":"b"}`, ""},
			}
			for i, call := range calls {
				if string(call.ID) != want[i].id || call.ToolID != want[i].tool || string(call.Arguments) != want[i].arguments || string(call.Signature) != want[i].signature {
					t.Errorf("call %d = {%s %s %s %s}, want %+v", i, call.ID, call.ToolID, call.Arguments, call.Signature, want[i])
				}
			}
		})
	}
}
