package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

type userKey struct{}

// userTransport sends the token of the end user found in the request's context,
// as a service does that calls a server for the person who started the Run.
type userTransport struct{}

func (userTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if user, ok := r.Context().Value(userKey{}).(string); ok {
		r.Header.Set("Authorization", "Bearer "+user)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// seenRequest is one request as the server received it.
type seenRequest struct{ http, rpc, token string }

// recordingServer serves newServer over streamable HTTP and records every
// request with its JSON-RPC method and bearer token, in arrival order.
func recordingServer(t *testing.T) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	server := newServer()
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	var mu sync.Mutex
	var seen []seenRequest
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := seenRequest{http: r.Method, token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var rpc struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &rpc)
			entry.rpc = rpc.Method
		}
		mu.Lock()
		seen = append(seen, entry)
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { httpServer.CloseClientConnections(); httpServer.Close() })
	return httpServer, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

func TestEachToolCallCarriesTheTokenOfItsOwnCaller(t *testing.T) {
	httpServer, requests := recordingServer(t)
	client := &http.Client{Transport: userTransport{}}
	set := newSet(t, gmcp.StreamableHTTP(httpServer.URL, gmcp.WithHTTPClient(client)))
	as := func(user string) context.Context {
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), userKey{}, user), 20*time.Second)
		t.Cleanup(cancel)
		return ctx
	}
	// The ToolSet connects on a request of whoever resolves first, and that
	// context ends before the other users call. The connection is shared, so
	// it must keep nothing of that user: its requests carry no user token.
	first, endFirst := context.WithCancel(context.WithValue(context.Background(), userKey{}, "alice"))
	if _, err := set.Tools(first); err != nil {
		t.Fatal(err)
	}
	endFirst()
	tools, err := set.Tools(as("bob"))
	if err != nil {
		t.Fatal(err)
	}
	var echo gotato.Tool
	for _, tool := range tools {
		if tool.Spec().ID == "echo" {
			echo = tool
		}
	}
	if echo == nil {
		t.Fatal("no echo tool")
	}
	for _, user := range []string{"bob", "alice", "bob", "carol"} {
		if _, err := echo.Execute(as(user), gotato.ToolUse{Arguments: []byte(`{"text":"hi"}`)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	_ = set.Close()
	// A tools/call and a tools/list carry the token of the context they were
	// sent in. The requests of the connection (initialize, the standalone
	// stream, DELETE on close) carry the token of no user, not even of the one
	// whose request made the connection.
	tokens := map[string][]string{}
	for _, r := range requests() {
		t.Logf("%-6s %-28s token=%q", r.http, r.rpc, r.token)
		key := r.http + " " + r.rpc
		tokens[key] = append(tokens[key], r.token)
	}
	for key, want := range map[string][]string{
		"POST tools/call":                {"bob", "alice", "bob", "carol"},
		"POST tools/list":                {"alice", "bob"},
		"POST initialize":                {""},
		"POST notifications/initialized": {""},
		"GET ":                           {""},
		"DELETE ":                        {""},
	} {
		if !slices.Equal(tokens[key], want) {
			t.Errorf("tokens of %q = %v, want %v", key, tokens[key], want)
		}
	}
}

// Through Sessions: the Run's context reaches the tool, so each Run's call
// carries the token of the user who started that Run, on one shared connection.
func TestARunsToolCallCarriesTheTokenOfItsUser(t *testing.T) {
	httpServer, requests := recordingServer(t)
	client := &http.Client{Transport: userTransport{}}
	set := newSet(t, gmcp.StreamableHTTP(httpServer.URL, gmcp.WithHTTPClient(client)))
	for _, user := range []string{"alice", "bob", "alice"} {
		model := testkit.NewFakeModel(
			testkit.ToolCalls(gotato.ToolCall{ID: "s1", ToolID: gotato.SearchToolName, Arguments: []byte(`{"queries":["echo"]}`)}),
			testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "crm.echo", Arguments: []byte(`{"text":"hi"}`)}),
			testkit.Text("done"),
		)
		session, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithFoldedToolSet(set), gotato.WithMaxEffect(gotato.ToolEffectRead))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), userKey{}, user), 20*time.Second)
		result, err := session.Prompt(ctx, gotato.UserMessage("echo"))
		cancel()
		_ = session.Close(context.Background())
		if err != nil || result.Status != gotato.RunStatusCompleted {
			t.Fatalf("%s: status %v, err %v", user, result.Status, err)
		}
	}
	// A Session resolves its tools in a context that holds no user, so
	// initialize and tools/list reach the server with no user token. The
	// server must answer them for a token of the service (the RoundTripper's
	// fallback) or for none.
	var calls, others []string
	for _, r := range requests() {
		t.Logf("%-6s %-28s token=%q", r.http, r.rpc, r.token)
		if r.rpc == "tools/call" {
			calls = append(calls, r.token)
		} else {
			others = append(others, r.token)
		}
	}
	if want := []string{"alice", "bob", "alice"}; !slices.Equal(calls, want) {
		t.Fatalf("tokens of the tools/call requests = %v, want %v", calls, want)
	}
	for _, token := range others {
		if token != "" {
			t.Fatalf("a request other than tools/call carried the token %q: %v", token, others)
		}
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
