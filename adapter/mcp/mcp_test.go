package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
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

func toolIDs(set *gmcp.ToolSet, t *testing.T) []string {
	var ids []string
	for id := range specs(t, set) {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func TestListChangedIsSeenWhenTheCacheIsKept(t *testing.T) {
	for _, tc := range []transportCase{{"in-memory", inMemory}, {"streamable-http", overHTTP}} {
		t.Run(tc.name, func(t *testing.T) {
			server := newServer()
			// The cache is long, so only the server's notification can refresh it.
			set := newSet(t, tc.make(t, server), gmcp.WithListCache(time.Hour))
			before := toolIDs(set, t)
			server.AddTool(&sdk.Tool{Name: "late", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					return textResult("late"), nil
				})
			deadline := time.Now().Add(10 * time.Second)
			for {
				after := toolIDs(set, t)
				if slices.Contains(after, "late") {
					if len(after) != len(before)+1 {
						t.Fatalf("tools = %v, was %v", after, before)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("list_changed never reached the ToolSet")
				}
				time.Sleep(20 * time.Millisecond)
			}
			server.RemoveTools("late")
			deadline = time.Now().Add(10 * time.Second)
			for slices.Contains(toolIDs(set, t), "late") {
				if time.Now().After(deadline) {
					t.Fatal("removal never reached the ToolSet")
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

func TestWithoutACacheEveryResolutionAsksTheServer(t *testing.T) {
	server := newServer()
	set := newSet(t, inMemory(t, server))
	if slices.Contains(toolIDs(set, t), "late") {
		t.Fatal("unexpected tool")
	}
	server.AddTool(&sdk.Tool{Name: "late", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return textResult("late"), nil
		})
	if !slices.Contains(toolIDs(set, t), "late") {
		t.Fatal("a new tool is not seen at the next resolution")
	}
}

func TestACanceledContextEndsTheCall(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "slow", Version: "1"}, nil)
	entered := make(chan struct{})
	server.AddTool(&sdk.Tool{Name: "hang", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	set := newSet(t, inMemory(t, server))
	tools, err := set.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tools[0].Execute(ctx, gotato.ToolUse{Arguments: []byte(`{}`)}, nil)
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the canceled call returned no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not end the call")
	}
}

func TestAServerThatIsDownIsSkippedWithAWarning(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	endpoint := dead.URL
	dead.Close()
	down := newSet(t, gmcp.StreamableHTTP(endpoint), gmcp.WithDescription("CRM"))
	good, err := gmcp.NewToolSet("mail", inMemory(t, newServer()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = good.Close() })

	model := testkit.NewFakeModel(testkit.Text("done"))
	session, err := gotato.NewSession(gotato.WithModel(model), gotato.WithToolSet(down), gotato.WithToolSet(good))
	if err != nil {
		t.Fatalf("a server that is down failed NewSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	recorder := testkit.RecordEvents(session)
	if _, err := session.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, event := range recorder.OfKind(gotato.EventKindExtensionFailed) {
		stages = append(stages, event.(gotato.ExtensionFailed).Stage)
	}
	if !slices.Equal(stages, []string{"toolset:crm"}) {
		t.Fatalf("warnings = %v", stages)
	}
	request := model.Requests()[0]
	var search gotato.ToolSpec
	for _, spec := range request.Tools {
		if spec.ID == gotato.SearchToolName {
			search = spec
		}
	}
	if !strings.Contains(search.Description, "crm — CRM (unavailable now; no tools)") || !strings.Contains(search.Description, "mail.get_order") {
		t.Fatalf("catalog:\n%s", search.Description)
	}
}

// A full Run: the model searches for a tool of the MCP server, calls it, and
// reads the result.
func TestARunCallsAnMCPToolThroughSearch(t *testing.T) {
	for _, tc := range allTransports {
		t.Run(tc.name, func(t *testing.T) {
			set := newSet(t, tc.make(t, newServer()))
			model := testkit.NewFakeModel(
				testkit.ToolCalls(gotato.ToolCall{ID: "s1", ToolID: gotato.SearchToolName, Arguments: []byte(`{"queries":["look up an order"]}`)}),
				testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "crm.get_order", Arguments: []byte(`{"id":"1"}`)}),
				testkit.Text("the order is 1"),
			)
			session, err := gotato.NewSession(gotato.WithModel(model), gotato.WithFoldedToolSet(set), gotato.WithMaxEffect(gotato.ToolEffectRead))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), gotato.UserMessage("what is order 1?"))
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != gotato.RunStatusCompleted {
				t.Fatalf("status = %s", result.Status)
			}
			requests := model.Requests()
			if len(requests) != 3 {
				t.Fatalf("model calls = %d", len(requests))
			}
			var offered []string
			for _, spec := range requests[1].Tools {
				offered = append(offered, spec.ID)
			}
			if !slices.Contains(offered, "crm.get_order") {
				t.Fatalf("the loaded tool is not offered: %v", offered)
			}
			// The Effect cap hides what annotations make write or destructive.
			for _, hidden := range []string{"crm.send_mail", "crm.delete_all", "crm.bare"} {
				if slices.Contains(offered, hidden) {
					t.Fatalf("%s is offered under a read cap", hidden)
				}
			}
			var output string
			for _, message := range requests[2].Messages {
				if message.ToolResult != nil && message.ToolResult.CallID == "c1" {
					output = gotato.TextOf(message)
				}
			}
			if output != "order 1" {
				t.Fatalf("tool result = %q", output)
			}
		})
	}
}

func TestOneConnectionServesManySessions(t *testing.T) {
	connects := 0
	server := newServer()
	base := inMemory(t, server)
	set := newSet(t, func() sdk.Transport { connects++; return base() })
	for range 3 {
		session, err := gotato.NewSession(gotato.WithModel(testkit.NewFakeModel(testkit.Text("x"))), gotato.WithToolSet(set))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Prompt(context.Background(), gotato.UserMessage("go")); err != nil {
			t.Fatal(err)
		}
		_ = session.Close(context.Background())
	}
	if connects != 1 {
		t.Fatalf("connections = %d, want 1 shared by every Session", connects)
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

func TestNewToolSetRefusesABadName(t *testing.T) {
	if _, err := gmcp.NewToolSet("a.b", gmcp.StdioCommand(func() *exec.Cmd { return exec.Command("x") })); err == nil {
		t.Fatal("a dotted name was accepted")
	}
	if _, err := gmcp.NewToolSet("", gmcp.StdioCommand(func() *exec.Cmd { return exec.Command("x") })); err == nil {
		t.Fatal("an empty name was accepted")
	}
	if _, err := gmcp.NewToolSet("a", gmcp.StdioCommand(func() *exec.Cmd { return exec.Command("x") }), gmcp.WithToolEffect("t", "bogus")); err == nil {
		t.Fatal("an unknown Effect was accepted")
	}
}

func TestWithoutNotificationsAKeptCacheIsNotRefreshedByTheServer(t *testing.T) {
	server := newServer()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	set := newSet(t, gmcp.StreamableHTTP(httpServer.URL, gmcp.WithoutNotifications()), gmcp.WithListCache(time.Hour))
	before := toolIDs(set, t)
	server.AddTool(&sdk.Tool{Name: "late", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return textResult("late"), nil
		})
	// The same wait that TestListChangedIsSeenWhenTheCacheIsKept needs far less than.
	time.Sleep(500 * time.Millisecond)
	if after := toolIDs(set, t); !slices.Equal(after, before) {
		t.Fatalf("tools = %v, want the cached %v", after, before)
	}
}

func TestAnHTTPServerThatRestartsIsReachedAgain(t *testing.T) {
	server := newServer()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	first := httptest.NewServer(handler)
	address := first.Listener.Addr().String()
	set := newSet(t, gmcp.StreamableHTTP(first.URL))
	if !slices.Contains(toolIDs(set, t), "echo") {
		t.Fatal("the first resolution did not see the tools")
	}
	first.CloseClientConnections()
	first.Close()

	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Skipf("the address was taken before the server could restart: %v", err)
	}
	// A new process knows none of the old sessions.
	fresh := newServer()
	second := httptest.NewUnstartedServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return fresh }, nil))
	second.Listener = listener
	second.Start()
	t.Cleanup(func() { second.CloseClientConnections(); second.Close() })

	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tools, err := set.Tools(ctx)
		cancel()
		if err == nil && len(tools) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ToolSet never reached the restarted server: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// restartedHTTPServer returns a ToolSet that has resolved its tools against a
// server, and restarts that server on the same address as a new process, which
// knows none of the old sessions.
func restartedHTTPServer(t *testing.T) (*gmcp.ToolSet, []gotato.Tool) {
	t.Helper()
	server := newServer()
	first := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	address := first.Listener.Addr().String()
	set := newSet(t, gmcp.StreamableHTTP(first.URL))
	tools, err := set.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first.CloseClientConnections()
	first.Close()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Skipf("the address was taken before the server could restart: %v", err)
	}
	fresh := newServer()
	second := httptest.NewUnstartedServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return fresh }, nil))
	second.Listener = listener
	second.Start()
	t.Cleanup(func() { second.CloseClientConnections(); second.Close() })
	return set, tools
}

// The server answers 404 to the old session: it did not process the request,
// so the ToolSet starts a new session at once, and the first resolution after
// the restart succeeds.
func TestTheFirstResolutionAfterAServerRestartSucceeds(t *testing.T) {
	set, _ := restartedHTTPServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := set.Tools(ctx)
	if err != nil {
		t.Fatalf("the first resolution after the restart failed: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("no tools after the restart")
	}
}

// A call on the old session got a 404, so the server never ran it: the ToolSet
// sends it again on a new session, and the caller sees one result, even for a
// tool that writes.
func TestTheFirstCallAfterAServerRestartSucceeds(t *testing.T) {
	_, tools := restartedHTTPServer(t)
	for _, tool := range tools {
		if tool.Spec().ID != "echo" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := tool.Execute(ctx, gotato.ToolUse{Arguments: []byte(`{"text":"hi"}`)}, nil); err != nil {
			t.Fatalf("the first call after the restart failed: %v", err)
		}
		return
	}
	t.Fatal("no echo tool")
}

// headerTransport adds a header to every request, as a team's gateway or auth would.
type headerTransport struct{ key, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.key, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

// clientInfoSeenBy serves a streamable HTTP MCP server and records the
// clientInfo of the last initialize request it receives.
func clientInfoSeenBy(t *testing.T) (gmcp.Transport, func() (name, version string)) {
	t.Helper()
	var mu sync.Mutex
	var name, version string
	server := newServer()
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
			Params struct {
				ClientInfo struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"clientInfo"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &msg) == nil && msg.Method == "initialize" {
			mu.Lock()
			name, version = msg.Params.ClientInfo.Name, msg.Params.ClientInfo.Version
			mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { httpServer.CloseClientConnections(); httpServer.Close() })
	return gmcp.StreamableHTTP(httpServer.URL), func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		return name, version
	}
}

func TestTheClientTellsTheServerWhoItIs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		options     []gmcp.ToolSetOption
		wantName    string
		wantVersion string
	}{
		{"default", nil, "gotato", "0"},
		{"WithClientInfo", []gmcp.ToolSetOption{gmcp.WithClientInfo("billing-svc", "1.2")}, "billing-svc", "1.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport, seen := clientInfoSeenBy(t)
			set := newSet(t, transport, tc.options...)
			specs(t, set) // the first resolution connects and initializes
			if name, version := seen(); name != tc.wantName || version != tc.wantVersion {
				t.Fatalf("server saw client %q %q, want %q %q", name, version, tc.wantName, tc.wantVersion)
			}
		})
	}
}

func TestACustomHTTPClientCarriesItsHeaderOnEveryRequest(t *testing.T) {
	server := newServer()
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var mu sync.Mutex
	var seen, missing int
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Header.Get("Authorization") == "Bearer secret" {
			seen++
		} else {
			missing++
		}
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { httpServer.CloseClientConnections(); httpServer.Close() })
	client := &http.Client{Transport: headerTransport{"Authorization", "Bearer secret"}}
	set := newSet(t, gmcp.StreamableHTTP(httpServer.URL, gmcp.WithHTTPClient(client)))
	result := execute(t, set, "echo", `{"text":"hi"}`)
	if got := gotato.TextOf(gotato.Message{Parts: result.Content}); got != "echo: hi" {
		t.Fatalf("got %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen == 0 || missing != 0 {
		t.Fatalf("requests with the header = %d, without = %d", seen, missing)
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
