package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

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
