package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// Observed in Mow: a DeepSeek Worker failed after 70 turns with HTTP 400
// "The `reasoning_content` in the thinking mode must be passed back to the
// API." The chat-completions stream carried reasoning_content that the
// gateway dropped, so the next request could not send it back. Reasoning
// deltas become reasoning events, and an assistant message's reasoning is
// replayed as reasoning_content, never mixed into content.
func TestChatReasoningContentIsKeptAndPassedBack(t *testing.T) {
	var second wireRequest
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req wireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if calls == 2 {
			second = req
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hard\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := gotato.NewSession(gotato.WithModel(client))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	first, err := agent.Prompt(context.Background(), gotato.UserMessage("q1"))
	if err != nil {
		t.Fatal(err)
	}
	if gotato.TextOf(*first.FinalMessage) != "answer" || gotato.ReasoningOf(*first.FinalMessage) != "think hard" {
		t.Fatalf("stored reply: text %q, reasoning %q", gotato.TextOf(*first.FinalMessage), gotato.ReasoningOf(*first.FinalMessage))
	}
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("q2")); err != nil {
		t.Fatal(err)
	}
	var assistant *wireMessage
	for i := range second.Messages {
		if second.Messages[i].Role == "assistant" {
			assistant = &second.Messages[i]
		}
	}
	if assistant == nil || assistant.ReasoningContent != "think hard" || assistant.Content != "answer" {
		t.Fatalf("replayed assistant message = %+v", assistant)
	}
}

// Reasoning is omitted from requests to providers that never produced it,
// so a strict provider sees no unknown field.
func TestChatRequestOmitsEmptyReasoning(t *testing.T) {
	body, _, err := encodeRequest("m", gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("q"), gotato.AssistantMessage("a")}}, providerRules{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "reasoning_content") {
		t.Fatalf("request carries reasoning_content without reasoning: %s", body)
	}
}
