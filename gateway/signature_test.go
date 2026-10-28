package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

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
