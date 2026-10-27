package gateway

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

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
