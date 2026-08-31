package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

func TestCodexResponsesStreamNormalizesTextReasoningAndUsage(t *testing.T) {
	token := testCodexToken("account-test")
	var request struct {
		Model        string            `json:"model"`
		Store        bool              `json:"store"`
		Stream       bool              `json:"stream"`
		Instructions string            `json:"instructions"`
		Input        []json.RawMessage `json:"input"`
		Include      []string          `json:"include"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "account-test" {
			t.Errorf("account id = %q", got)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "responses=experimental" {
			t.Errorf("OpenAI-Beta = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`)
		writeSSE(t, w, `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thinking"}`)
		writeSSE(t, w, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"opaque"}}`)
		writeSSE(t, w, `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant"}}`)
		writeSSE(t, w, `{"type":"response.output_text.delta","output_index":1,"delta":"hello"}`)
		writeSSE(t, w, `{"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`)
		writeSSE(t, w, `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`)
	}))
	defer server.Close()

	client, err := New(Config{API: "openai-codex-responses", Endpoint: server.URL, APIKey: token, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{
		SystemInstructions: "system",
		Messages:           []gotato.Message{gotato.UserMessage("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var events []gotato.ModelEvent
	for {
		event, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			break
		}
		events = append(events, event)
	}
	if len(events) != 5 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Kind != gotato.ModelReasoningDelta || events[0].Text != "thinking" {
		t.Fatalf("reasoning event = %+v", events[0])
	}
	if events[1].Kind != gotato.ModelReasoningDone || string(events[1].ReasoningArtifact) == "" {
		t.Fatalf("reasoning done event = %+v", events[1])
	}
	if events[2].Kind != gotato.ModelTextDelta || events[2].Text != "hello" {
		t.Fatalf("text event = %+v", events[2])
	}
	if events[3].Kind != gotato.ModelUsage || events[3].Usage.TotalTokens != 5 {
		t.Fatalf("usage event = %+v", events[3])
	}
	if events[4].Kind != gotato.ModelDone || events[4].StopReason != gotato.StopEndTurn {
		t.Fatalf("done event = %+v", events[4])
	}
	if request.Model != "gpt-test" || request.Store || !request.Stream || request.Instructions != "system" {
		t.Fatalf("request = %+v", request)
	}
	if len(request.Input) != 1 || len(request.Include) != 1 || request.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("input/include = %+v / %+v", request.Input, request.Include)
	}
}

func TestCodexResponsesReadsPiOAuthFile(t *testing.T) {
	token := testCodexToken("account-from-file")
	authFile := map[string]any{
		"openai-codex": map[string]any{
			"type": "oauth", "access": token, "refresh": "unused", "expires": time.Now().Add(time.Hour).UnixMilli(),
		},
	}
	data, err := json.Marshal(authFile)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/auth.json"
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("chatgpt-account-id") != "account-from-file" {
			t.Errorf("account id = %q", r.Header.Get("chatgpt-account-id"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, `{"type":"response.completed","response":{"status":"completed"}}`)
	}))
	defer server.Close()

	client, err := New(Config{API: "openai-codex-responses", Endpoint: server.URL, Model: "gpt-test", Auth: AuthConfig{Type: "pi_oauth", File: path}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCodexResponsesReplaysReasoningArtifact(t *testing.T) {
	artifact := []byte(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}`)
	body, _, err := encodeCodexRequest("gpt-test", gotato.ModelRequest{Messages: []gotato.Message{{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{{Kind: gotato.ContentReasoning, Signature: artifact}}}, gotato.UserMessage("next")}})
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 2 || string(request.Input[0]) != string(artifact) {
		t.Fatalf("input = %s", request.Input)
	}
}

func TestCodexResponsesStreamReassemblesToolCall(t *testing.T) {
	token := testCodexToken("account-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		name := gatewayFunctionName("demo.echo")
		writeSSE(t, w, `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"`+name+`"}}`)
		writeSSE(t, w, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"value\":"}`)
		writeSSE(t, w, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"x\"}"}`)
		writeSSE(t, w, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"`+name+`","arguments":"{\"value\":\"x\"}"}}`)
		writeSSE(t, w, `{"type":"response.completed","response":{"status":"completed"}}`)
	}))
	defer server.Close()

	client, err := New(Config{API: "openai-codex-responses", Endpoint: server.URL, APIKey: token, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{Tools: []gotato.ToolSpec{{ID: "demo.echo", InputSchema: []byte(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	toolEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if toolEvent.Kind != gotato.ModelToolCall || toolEvent.ToolCall == nil {
		t.Fatalf("tool event = %+v", toolEvent)
	}
	if toolEvent.ToolCall.ID != "call_1|fc_1" || toolEvent.ToolCall.ToolID != "demo.echo" || string(toolEvent.ToolCall.Arguments) != `{"value":"x"}` {
		t.Fatalf("tool call = %+v", toolEvent.ToolCall)
	}
	doneEvent, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if doneEvent.Kind != gotato.ModelDone || doneEvent.StopReason != gotato.StopToolCalls {
		t.Fatalf("done event = %+v", doneEvent)
	}
}

func writeSSE(t *testing.T, w http.ResponseWriter, data string) {
	t.Helper()
	if _, err := w.Write([]byte("data: " + data + "\n\n")); err != nil {
		t.Fatal(err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func testCodexToken(accountID string) string {
	encode := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID},
	})
	return encode(`{"alg":"none"}`) + "." + encode(string(payload)) + ".signature"
}
