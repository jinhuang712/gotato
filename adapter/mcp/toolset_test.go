package mcp_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithToolSet(down), gotato.WithToolSet(good))
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
			session, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithFoldedToolSet(set), gotato.WithMaxEffect(gotato.ToolEffectRead))
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
		session, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.NewFakeModel(testkit.Text("x"))), gotato.WithToolSet(set))
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

// silentServer accepts connections and never answers them.
func silentServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for _, conn := range held {
			_ = conn.Close()
		}
		mu.Unlock()
	})
	return "http://" + listener.Addr().String()
}

// A server that accepts the connection and never answers must not stall the
// ToolSet: the connect is bounded, a second caller waits only as long as its
// own context, and Close does not wait for the connect.
func TestASilentServerDoesNotStallTheToolSet(t *testing.T) {
	set := newSet(t, gmcp.StreamableHTTP(silentServer(t)), gmcp.WithConnectTimeout(300*time.Millisecond))

	first := make(chan error, 1)
	go func() {
		_, err := set.Tools(context.Background())
		first <- err
	}()
	time.Sleep(50 * time.Millisecond) // the first call is connecting

	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := set.Tools(ctx)
	cancel()
	if err == nil || time.Since(began) > 250*time.Millisecond {
		t.Fatalf("a second caller returned %v after %v; want its own context to end the wait", err, time.Since(began))
	}

	closed := make(chan struct{})
	go func() { _ = set.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close waited for the connect")
	}

	select {
	case err := <-first:
		if err == nil {
			t.Fatal("the first call succeeded against a silent server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connect was not bounded")
	}
}

// With no option the connect has a default bound.
func TestTheDefaultConnectBoundEndsASilentConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the default bound")
	}
	set := newSet(t, gmcp.StreamableHTTP(silentServer(t)))
	began := time.Now()
	_, err := set.Tools(context.Background())
	if err == nil || time.Since(began) > 15*time.Second {
		t.Fatalf("err = %v after %v; want the default bound (10 s) to end it", err, time.Since(began))
	}
}

// The description of a ToolSet is what the server said at its first connect.
// A server that restarts with other instructions does not change it, since a
// changing description is a different prompt prefix between Turns.
func TestTheDescriptionIsStableAcrossReconnects(t *testing.T) {
	generation := func(n int) http.Handler {
		server := sdk.NewServer(&sdk.Implementation{Name: "crm", Version: "1"}, &sdk.ServerOptions{Instructions: fmt.Sprintf("Version %d.", n)})
		sdk.AddTool(server, &sdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest, map[string]any) (*sdk.CallToolResult, any, error) {
			return textResult("ok"), nil, nil
		})
		return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	}
	var current atomic.Pointer[http.Handler]
	set1 := generation(1)
	current.Store(&set1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*current.Load()).ServeHTTP(w, r) }))
	t.Cleanup(httpServer.Close)
	set := newSet(t, gmcp.StreamableHTTP(httpServer.URL))
	if _, err := set.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := set.Spec().Description
	if first != "Version 1." {
		t.Fatalf("description = %q", first)
	}
	// A restart: a new process that knows none of the old sessions.
	set2 := generation(2)
	current.Store(&set2)
	if _, err := set.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := set.Spec().Description; got != first {
		t.Fatalf("description changed from %q to %q after a reconnect", first, got)
	}
}

// A server that lists a tool twice, or one with a blank name, loses only that
// entry: the core refuses a ToolSet with a duplicate Tool ID whole, so the
// adapter leaves the bad entries out and reports them with Skipped.
func TestABadEntryInTheToolListLosesOnlyThatEntry(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "s", Version: "1"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, name := range []string{"a", "b"} {
		server.AddTool(&sdk.Tool{Name: name, InputSchema: schema}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return textResult(name), nil
		})
	}
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			result, err := next(ctx, method, req)
			if list, ok := result.(*sdk.ListToolsResult); ok && err == nil {
				list.Tools = append(list.Tools, list.Tools[0], &sdk.Tool{Name: " ", InputSchema: schema})
			}
			return result, err
		}
	})
	set, err := gmcp.NewToolSet("srv", inMemory(t, server))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	model := testkit.NewFakeModel(testkit.Text("ok"))
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithFoldedToolSet(set))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err := s.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	// With a good tool in the catalog the model is offered search_tools; a
	// ToolSet the core refused leaves it nothing.
	request, _ := model.LastRequest()
	if len(request.Tools) == 0 {
		t.Fatal("the model was offered no Tool: the server's good tools are lost with its bad entries")
	}
	tools, err := set.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Spec().Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"a", "b"}) {
		t.Fatalf("tools = %v, want a and b", names)
	}
	skipped := set.Skipped()
	if len(skipped) != 2 || skipped[0].Name != "a" || skipped[1].Name != "" || skipped[0].Reason == "" || skipped[1].Reason == "" {
		t.Fatalf("Skipped = %+v, want the duplicate a and the blank name, each with a reason", skipped)
	}
}
