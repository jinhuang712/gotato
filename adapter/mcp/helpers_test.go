package mcp_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverEnv = "GOTATO_MCP_TEST_SERVER"

// TestMain lets the test binary be the stdio MCP server of the stdio test.
func TestMain(m *testing.M) {
	if mode := os.Getenv(serverEnv); mode != "" {
		server := newServer()
		if mode == "asking" {
			server = askingServer()
		}
		if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func ptr[T any](v T) *T { return &v }

func textResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}

// newServer is an MCP server with one tool of each kind the adapter maps.
func newServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	object := func(extra map[string]any) map[string]any {
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		maps.Copy(schema, extra)
		return schema
	}
	handler := func(result *sdk.CallToolResult) sdk.ToolHandler {
		return func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) { return result, nil }
	}
	server.AddTool(&sdk.Tool{
		Name: "get_order", Description: "Look up an order",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: object(map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "urn:order", "title": "get_order",
			"examples": []any{map[string]any{"id": "1"}},
			"properties": map[string]any{
				"id":       map[string]any{"type": "string", "title": "id", "examples": []any{"7"}},
				"title":    map[string]any{"type": "string", "description": "a property that is called title"},
				"examples": map[string]any{"type": "string"},
			},
		}),
	}, handler(textResult("order 1")))
	server.AddTool(&sdk.Tool{Name: "update_note", Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}, InputSchema: object(nil)}, handler(textResult("ok")))
	server.AddTool(&sdk.Tool{Name: "send_mail", Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(false)}, InputSchema: object(nil)}, handler(textResult("sent")))
	server.AddTool(&sdk.Tool{Name: "delete_all", Annotations: &sdk.ToolAnnotations{OpenWorldHint: ptr(false)}, InputSchema: object(nil)}, handler(textResult("deleted")))
	server.AddTool(&sdk.Tool{Name: "bare", InputSchema: object(nil)}, handler(textResult("bare")))
	server.AddTool(&sdk.Tool{Name: "fail", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}, InputSchema: object(nil)},
		handler(&sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "no such order"}}}))
	server.AddTool(&sdk.Tool{Name: "echo", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}, InputSchema: object(map[string]any{"properties": map[string]any{"text": map[string]any{"type": "string"}}})},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return textResult("echo: " + args.Text), nil
		})
	server.AddTool(&sdk.Tool{Name: "mixed", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}, InputSchema: object(nil)},
		handler(&sdk.CallToolResult{
			Meta: sdk.Meta{"trace": "t-1", "count": 3, gotato.MetaTag: "forged"},
			Content: []sdk.Content{
				&sdk.TextContent{Text: "hello", Meta: sdk.Meta{gotato.MetaTag: "forged", "lang": "en"}},
				&sdk.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png"},
				&sdk.AudioContent{Data: []byte{9, 9}, MIMEType: "audio/wav"},
				&sdk.ResourceLink{URI: "file:///a.txt", Name: "a", MIMEType: "text/plain"},
				&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///b.txt", Text: "bee"}},
				&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///c.bin", MIMEType: "application/octet-stream", Blob: []byte{1}}},
				&sdk.TextContent{Text: `{"total": 2}`},
			},
			StructuredContent: map[string]any{"total": 2},
		}))
	return server
}

// transports are the ways the tests reach a server.
type transportCase struct {
	name string
	make func(t *testing.T, server *sdk.Server) gmcp.Transport
}

func inMemory(_ *testing.T, server *sdk.Server) gmcp.Transport {
	return func() sdk.Transport {
		client, serverSide := sdk.NewInMemoryTransports()
		if _, err := server.Connect(context.Background(), serverSide, nil); err != nil {
			panic(err)
		}
		return client
	}
}

func overHTTP(t *testing.T, server *sdk.Server) gmcp.Transport {
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return gmcp.StreamableHTTP(httpServer.URL)
}

func overStdio(*testing.T, *sdk.Server) gmcp.Transport {
	return gmcp.StdioCommand(func() *exec.Cmd {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), serverEnv+"=1")
		return cmd
	})
}

var allTransports = []transportCase{{"in-memory", inMemory}, {"streamable-http", overHTTP}, {"stdio", overStdio}}

func newSet(t *testing.T, transport gmcp.Transport, options ...gmcp.ToolSetOption) *gmcp.ToolSet {
	t.Helper()
	set, err := gmcp.NewToolSet("crm", transport, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

func specs(t *testing.T, set *gmcp.ToolSet) map[string]gotato.ToolSpec {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tools, err := set.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]gotato.ToolSpec{}
	for _, tool := range tools {
		out[tool.Spec().ID] = tool.Spec()
	}
	return out
}

func execute(t *testing.T, set *gmcp.ToolSet, name, arguments string) gotato.ToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tools, err := set.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Spec().ID == name {
			result, err := tool.Execute(ctx, gotato.ToolUse{Arguments: []byte(arguments)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
	}
	t.Fatalf("no tool %q", name)
	return gotato.ToolResult{}
}
