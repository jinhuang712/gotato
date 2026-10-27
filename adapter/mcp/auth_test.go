package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	// The ToolSet connects in the context of whoever resolves first, and that
	// context ends before the other users call.
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
	// stream, DELETE on close) carry that of the context that made it.
	tokens := map[string][]string{}
	for _, r := range requests() {
		t.Logf("%-6s %-28s token=%q", r.http, r.rpc, r.token)
		key := r.http + " " + r.rpc
		tokens[key] = append(tokens[key], r.token)
	}
	for key, want := range map[string][]string{
		"POST tools/call":                {"bob", "alice", "bob", "carol"},
		"POST tools/list":                {"alice", "bob"},
		"POST initialize":                {"alice"},
		"POST notifications/initialized": {"alice"},
		"GET ":                           {"alice"},
		"DELETE ":                        {"alice"},
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
		session, err := gotato.NewSession(gotato.WithModel(model), gotato.WithFoldedToolSet(set), gotato.WithMaxEffect(gotato.ToolEffectRead))
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
