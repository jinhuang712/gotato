package gateway

import (
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

func TestFunctionNamesStayReadable(t *testing.T) {
	cases := map[string]string{
		"spawn_task":  "spawn_task",
		"read":        "read",
		"demo.echo":   "demo_echo",
		"fs/read-all": "fs_read-all",
	}
	for id, want := range cases {
		if got := gatewayFunctionName(id); got != want {
			t.Fatalf("gatewayFunctionName(%q) = %q, want %q", id, got, want)
		}
	}
	long := strings.Repeat("x", 100)
	if got := gatewayFunctionName(long); len(got) != maxFunctionName || !strings.HasPrefix(got, "xxxx") {
		t.Fatalf("long name = %q (%d)", got, len(got))
	}
	if got := gatewayFunctionName(""); got == "" || len(got) > maxFunctionName {
		t.Fatalf("empty id name = %q", got)
	}
}

func TestAssignFunctionNameResolvesCollisions(t *testing.T) {
	names := map[string]string{}
	a, err := assignFunctionName(names, "a_b")
	if err != nil || a != "a_b" {
		t.Fatalf("first = %q, %v", a, err)
	}
	b, err := assignFunctionName(names, "a.b")
	if err != nil || b == a || !strings.HasPrefix(b, "a_b_") || len(b) > maxFunctionName {
		t.Fatalf("colliding id got %q, %v", b, err)
	}
	// Stable within a request: the same ID maps to the same name again.
	if again, _ := assignFunctionName(names, "a.b"); again != b {
		t.Fatalf("second assignment = %q, want %q", again, b)
	}
	if names[a] != "a_b" || names[b] != "a.b" {
		t.Fatalf("reverse map = %v", names)
	}
}

// A Tool ID that providers accept goes out unchanged even when another ID
// rewrites to the same name: the rewritten ID takes the hash. Tools are sorted
// by ID and '.' sorts before '_', so a.b used to claim a_b first, and the
// chat and Responses adapters named the history's calls before the Tools.
func TestAcceptedToolIDsKeepTheirNamesInACollision(t *testing.T) {
	encoders := map[API]func(string, gotato.ModelRequest, providerRules) ([]byte, map[string]string, error){
		APIChatCompletions:   encodeChatRequest,
		APIResponses:         encodeResponsesRequest,
		APIAnthropicMessages: encodeAnthropicRequest,
	}
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "call_1", ToolID: "a.b", Arguments: []byte(`{}`)}}
	result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "call_1", Status: gotato.ToolResultStatusOK}}
	requests := map[string]gotato.ModelRequest{
		"tools only": {
			Messages: []gotato.Message{gotato.UserMessage("go")},
			Tools:    []gotato.ToolSpec{{ID: "a.b"}, {ID: "a_b"}},
		},
		"history first": {
			Messages: []gotato.Message{gotato.UserMessage("go"), call, result},
			Tools:    []gotato.ToolSpec{{ID: "a.b"}, {ID: "a_b"}},
		},
	}
	for _, api := range wireAPIs {
		for label, request := range requests {
			_, names, err := encoders[api]("m", request, providerRules{})
			if err != nil {
				t.Fatalf("%s, %s: %v", api, label, err)
			}
			if names["a_b"] != "a_b" {
				t.Errorf("%s, %s: the name a_b belongs to %q, want a_b", api, label, names["a_b"])
			}
			hashed := ""
			for name, id := range names {
				if id == "a.b" {
					hashed = name
				}
			}
			if !strings.HasPrefix(hashed, "a_b_") || !plainFunctionName(hashed) {
				t.Errorf("%s, %s: a.b goes out as %q, want a_b_ plus a hash", api, label, hashed)
			}
		}
	}
}

func TestFunctionNamesStayReadableWhenProvidersAcceptThem(t *testing.T) {
	long := strings.Repeat("a", 65)
	cases := []struct {
		id      string
		encoded bool
	}{
		{"bash", false},
		{"read_file", false},
		{"spawn-worker2", false},
		{"demo.echo", true},
		{"fs/read", true},
		{"gotato_bash", false},
		{long, true},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		name := gatewayFunctionName(tc.id)
		if got := name != tc.id; got != tc.encoded {
			t.Errorf("gatewayFunctionName(%q) = %q; encoded %v, want %v", tc.id, name, got, tc.encoded)
		}
		if !plainFunctionName(name) {
			t.Errorf("gatewayFunctionName(%q) = %q is not a valid function name", tc.id, name)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("%q and %q share the wire name %q", other, tc.id, name)
		}
		seen[name] = tc.id
	}
}
