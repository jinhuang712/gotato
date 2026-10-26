package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// fallbackReply scripts, per wire API, a reply in which the provider reports
// another model than the one requested, as a gateway with fallbacks does.
var fallbackReply = map[string][]string{
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
		t.Run(api, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeReply(t, w, fallbackReply[api])
			}))
			defer server.Close()
			client, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "requested-model", NoRetries: true})
			if err != nil {
				t.Fatal(err)
			}
			session, err := gotato.NewSession(gotato.WithModel(client))
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
