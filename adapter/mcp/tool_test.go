package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A cut at the byte limit must not split a rune: the text reaches the model.
func TestFirstTextCutsOnARuneBoundary(t *testing.T) {
	parts := []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "日本語"}}
	got := gmcp.FirstText(parts, 4)
	if !utf8.ValidString(got) || got != "日" {
		t.Fatalf("FirstText = %q, want %q", got, "日")
	}
}

func TestEffectComesFromAnnotations(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			set := newSet(t, tc.make(t, newServer()), gmcp.WithToolEffect("bare", gotato.ToolEffectWriteLocal))
			got := specs(t, set)
			want := map[string]gotato.ToolEffect{
				"get_order":   gotato.ToolEffectRead,
				"update_note": gotato.ToolEffectWriteLocal,
				"send_mail":   gotato.ToolEffectWriteExternal,
				"delete_all":  gotato.ToolEffectDestructive, // destructive by default, closed world
				"bare":        gotato.ToolEffectWriteLocal,  // the override
				"echo":        gotato.ToolEffectRead,
			}
			for name, effect := range want {
				if got[name].Effect != effect {
					t.Errorf("%s effect = %q, want %q", name, got[name].Effect, effect)
				}
			}
		})
	}
	if gmcp.EffectOf(nil) != gotato.ToolEffectDestructive {
		t.Error("a tool with no annotations must be destructive")
	}
	if gmcp.EffectOf(&sdk.ToolAnnotations{}) != gotato.ToolEffectDestructive {
		t.Error("annotations that declare nothing must be destructive")
	}
	if gmcp.EffectOf(&sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: ptr(true)}) != gotato.ToolEffectRead {
		t.Error("readOnlyHint wins")
	}
}

func TestSchemasAreSlimmed(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			spec := specs(t, newSet(t, tc.make(t, newServer())))["get_order"]
			var schema map[string]any
			if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"$schema", "$id", "examples", "title"} {
				if _, ok := schema[key]; ok {
					t.Errorf("root still has %q: %s", key, spec.InputSchema)
				}
			}
			props := schema["properties"].(map[string]any)
			id := props["id"].(map[string]any)
			if _, ok := id["title"]; ok {
				t.Errorf("a property title equal to its name stays: %s", spec.InputSchema)
			}
			if _, ok := id["examples"]; ok {
				t.Errorf("a property's examples stay: %s", spec.InputSchema)
			}
			// A property that happens to be called title or examples is not a keyword.
			if _, ok := props["title"]; !ok {
				t.Errorf("the property called title was dropped: %s", spec.InputSchema)
			}
			if _, ok := props["examples"]; !ok {
				t.Errorf("the property called examples was dropped: %s", spec.InputSchema)
			}
			if schema["type"] != "object" {
				t.Errorf("type = %v", schema["type"])
			}
		})
	}
}

func TestContentMapsToParts(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			set := newSet(t, tc.make(t, newServer()))
			result := execute(t, set, "mixed", `{}`)
			if result.Status != gotato.ToolResultStatusOK {
				t.Fatalf("status = %s", result.Status)
			}
			kinds := make([]gotato.ContentKind, len(result.Content))
			for i, part := range result.Content {
				kinds[i] = part.Kind
			}
			want := []gotato.ContentKind{
				gotato.ContentKindText,  // hello
				gotato.ContentKindImage, // png
				gotato.ContentKindText,  // audio placeholder
				gotato.ContentKindText,  // resource link
				gotato.ContentKindText,  // embedded text resource
				gotato.ContentKindText,  // embedded blob placeholder
				gotato.ContentKindJSON,  // structuredContent; the text that repeated it is dropped
			}
			if !slices.Equal(kinds, want) {
				t.Fatalf("kinds = %v, want %v", kinds, want)
			}
			if result.Content[0].Text != "hello" {
				t.Errorf("text = %q", result.Content[0].Text)
			}
			if got := result.Content[1]; got.MIMEType != "image/png" || !slices.Equal(got.Data, []byte{1, 2, 3}) {
				t.Errorf("image = %+v", got)
			}
			for i, fragment := range map[int]string{2: "audio/wav", 3: "file:///a.txt", 4: "bee", 5: "file:///c.bin"} {
				if !strings.Contains(result.Content[i].Text, fragment) {
					t.Errorf("part %d = %q, want %q in it", i, result.Content[i].Text, fragment)
				}
			}
			if got := result.Content[6].Text; got != `{"total":2}` {
				t.Errorf("structured = %q", got)
			}
		})
	}
}

func TestMetaIsCopiedWithoutTheTagKey(t *testing.T) {
	set := newSet(t, inMemory(t, newServer()))
	result := execute(t, set, "mixed", `{}`)
	if _, ok := result.Metadata[gotato.MetaTag]; ok {
		t.Fatalf("the result's metadata carries %s: %v", gotato.MetaTag, result.Metadata)
	}
	if result.Metadata["trace"] != "t-1" || result.Metadata["count"] != "3" {
		t.Fatalf("metadata = %v", result.Metadata)
	}
	if part := result.Content[0]; part.Metadata[gotato.MetaTag] != "" || part.Metadata["lang"] != "en" {
		t.Fatalf("part metadata = %v", part.Metadata)
	}
	for _, part := range result.Content {
		if _, ok := part.Metadata[gotato.MetaTag]; ok {
			t.Fatalf("a Part carries %s", gotato.MetaTag)
		}
	}
}

func TestIsErrorIsAFailedResult(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			result := execute(t, newSet(t, tc.make(t, newServer())), "fail", `{}`)
			if result.Status != gotato.ToolResultStatusFailed {
				t.Fatalf("status = %s, want failed", result.Status)
			}
			if gotato.TextOf(gotato.Message{Parts: result.Content}) != "no such order" || result.SafeError != "no such order" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestArgumentsReachTheServer(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			result := execute(t, newSet(t, tc.make(t, newServer())), "echo", `{"text":"hi"}`)
			if got := gotato.TextOf(gotato.Message{Parts: result.Content}); got != "echo: hi" {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestSlimSchemaKeepsWhatTheModelNeeds(t *testing.T) {
	got := string(gmcp.SlimSchema("t", map[string]any{
		"type": "object", "title": "t", "$schema": "x",
		"properties": map[string]any{
			"a": map[string]any{"type": "array", "items": map[string]any{"type": "string", "title": "keep me", "examples": []any{"z"}}, "description": "list"},
		},
		"required": []any{"a"},
	}))
	want := `{"properties":{"a":{"description":"list","items":{"title":"keep me","type":"string"},"type":"array"}},"required":["a"],"type":"object"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if gmcp.SlimSchema("t", nil) != nil {
		t.Fatal("a missing schema must stay missing")
	}
}

// askingServer has tools that ask the client for input in the middle of a
// call, as a multi round-trip server does: a tool answers with the requests it
// needs filled, and the client is to call again with the replies. It also has
// the instructions the ToolSet reads, and a tool that reports progress.
func askingServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "asking", Version: "1"}, &sdk.ServerOptions{
		Instructions: "Asks things for the user.\r\nStill the first paragraph.\n\nSecond paragraph, for the model.",
	})
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	asks := func(request sdk.InputRequest) sdk.ToolHandler {
		return func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{"q": request}}, nil
		}
	}
	server.AddTool(&sdk.Tool{Name: "elicit", InputSchema: schema},
		asks(&sdk.ElicitParams{Message: "pick one", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}}}))
	server.AddTool(&sdk.Tool{Name: "sample", InputSchema: schema},
		//nolint:staticcheck // sampling is deprecated, and a server may still ask
		asks(&sdk.CreateMessageParams{MaxTokens: 5, Messages: []*sdk.SamplingMessage{{Role: "user", Content: &sdk.TextContent{Text: "hi"}}}}))
	server.AddTool(&sdk.Tool{Name: "slow", InputSchema: schema}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		token := req.Params.GetProgressToken()
		if token == nil {
			return textResult("no token"), nil
		}
		for _, step := range []sdk.ProgressNotificationParams{{Progress: 1, Total: 3, Message: "reading"}, {Progress: 2, Total: 3}} {
			step.ProgressToken = token
			if err := req.Session.NotifyProgress(ctx, &step); err != nil {
				return nil, err
			}
		}
		// The SDK queues incoming notifications, and a response can overtake
		// them; one that trails the result is dropped, as the call is over.
		time.Sleep(200 * time.Millisecond)
		return textResult("done"), nil
	})
	return server
}

func TestAToolThatAsksForInputEndsWithAFailedResult(t *testing.T) {
	for _, tc := range allTransports {
		for _, name := range []string{"elicit", "sample"} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				set := newSet(t, askingTransport(t, tc))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				tools, err := set.Tools(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range tools {
					if tool.Spec().ID != name {
						continue
					}
					// The call ends at once: with a failed Tool Result the model
					// reads, or with an error; never with a success or a wait.
					result, err := tool.Execute(ctx, gotato.ToolUse{}, nil)
					if err == nil && (result.Status != gotato.ToolResultStatusFailed || !strings.Contains(result.SafeError, "asked for input")) {
						t.Fatalf("not a clear failed result: %+v", result)
					}
					if ctx.Err() != nil {
						t.Fatal("the call waited")
					}
					return
				}
				t.Fatal("tool not found")
			})
		}
	}
}

func TestProgressNotificationsAreForwarded(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			set := newSet(t, askingTransport(t, tc))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tools, err := set.Tools(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range tools {
				if tool.Spec().ID != "slow" {
					continue
				}
				var mu sync.Mutex
				var got []string
				result, err := tool.Execute(ctx, gotato.ToolUse{}, func(text string) {
					mu.Lock()
					got = append(got, text)
					mu.Unlock()
				})
				if err != nil || result.Status != gotato.ToolResultStatusOK {
					t.Fatalf("%v %+v", err, result)
				}
				// A notification may arrive just after the result: wait for both.
				deadline := time.Now().Add(5 * time.Second)
				for {
					mu.Lock()
					n := len(got)
					mu.Unlock()
					if n >= 2 || time.Now().After(deadline) {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Equal(got, []string{"reading", "2/3"}) {
					t.Fatalf("progress = %q", got)
				}
				return
			}
			t.Fatal("tool not found")
		})
	}
}

func TestACallWithoutAProgressFunctionSendsNoToken(t *testing.T) {
	set := newSet(t, inMemory(t, askingServer()))
	result := execute(t, set, "slow", "{}")
	if len(result.Content) != 1 || result.Content[0].Text != "no token" {
		t.Fatalf("%+v", result)
	}
}

func TestInstructionsDescribeTheToolSetUnlessTheTeamDid(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			set := newSet(t, askingTransport(t, tc))
			if got := set.Spec().Description; got != "" {
				t.Fatalf("before connecting: %q", got)
			}
			specs(t, set)
			if got, want := set.Spec().Description, "Asks things for the user. Still the first paragraph."; got != want {
				t.Fatalf("description = %q, want %q", got, want)
			}
			own := newSet(t, askingTransport(t, tc), gmcp.WithDescription("Customer records"))
			specs(t, own)
			if got := own.Spec().Description; got != "Customer records" {
				t.Fatalf("WithDescription lost: %q", got)
			}
		})
	}
}

// askingTransport reaches askingServer; over stdio the test binary is that
// server.
func askingTransport(t *testing.T, tc transportCase) gmcp.Transport {
	if tc.name != "stdio" {
		return tc.make(t, askingServer())
	}
	return gmcp.StdioCommand(func() *exec.Cmd {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), serverEnv+"=asking")
		return cmd
	})
}

// The model reads a failed call's public text. It says the server did not take
// the call, and carries nothing of the transport error (a URL, a host), which
// stays the cause.
func TestAFailedCallHasFixedPublicText(t *testing.T) {
	server := newServer()
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	set := newSet(t, gmcp.StreamableHTTP(httpServer.URL))
	tools, err := set.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	httpServer.CloseClientConnections()
	httpServer.Close()
	for _, tool := range tools {
		if tool.Spec().ID != "echo" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := tool.Execute(ctx, gotato.ToolUse{Arguments: []byte(`{"text":"hi"}`)}, nil)
		var public gotato.PublicError
		if !errors.As(err, &public) {
			t.Fatalf("err = %v, want a PublicError", err)
		}
		host := strings.TrimPrefix(httpServer.URL, "http://")
		if text := public.Public(); strings.Contains(text, host) || strings.Contains(text, "127.0.0.1") {
			t.Fatalf("public text %q carries the transport error", text)
		}
		if !strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("the cause %q lost the transport error", err)
		}
		return
	}
	t.Fatal("no echo tool")
}
