package mcp_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	gmcp "github.com/jinhuang712/gotato/adapter/mcp"
)

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
