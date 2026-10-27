package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/adapter/mcp"
	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"

	"github.com/jinhuang712/gotato/examples/larkbot/internal/fakemcp"
)

func TestParseTools(t *testing.T) {
	got, err := parseTools("export,mcp", "http://x/mcp")
	if err != nil || !got.export || !got.mcpActive || got.mcpSearch {
		t.Fatalf("parseTools = %+v, %v", got, err)
	}
	if got, err := parseTools("", ""); err != nil || got != (toolSpec{}) {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	for _, bad := range []struct{ list, url string }{{"nope", ""}, {"mcp", ""}, {"mcp,mcp-search", "http://x"}} {
		if _, err := parseTools(bad.list, bad.url); err == nil {
			t.Errorf("parseTools(%q, %q): want an error", bad.list, bad.url)
		}
	}
}

// runner builds a Runner with the scripted model over the tools, on store, as one bot instance would.
func scriptedRunner(t *testing.T, store session.Store, instance, kinds, mcpURL string, with ...func(*service.Agent)) *service.Runner {
	t.Helper()
	agent, err := buildAgent("echo")
	if err != nil {
		t.Fatal(err)
	}
	agent.Model = scriptModel{agent.Model}
	for _, w := range with {
		w(&agent)
	}
	tools, err := parseTools(kinds, mcpURL)
	if err != nil {
		t.Fatal(err)
	}
	closeTools, err := tools.apply(&agent, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if closeTools != nil {
		t.Cleanup(func() { _ = closeTools() })
	}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{agent}, Instance: instance})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRunner(runner) })
	return runner
}

// lastReply runs prompt in the Session and returns the text of the last assistant message.
func lastReply(t *testing.T, r *service.Runner, id, prompt string) string {
	t.Helper()
	if _, err := r.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt}); err != nil {
		t.Fatal(err)
	}
	rec, err := r.Session(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	msgs := rec.Messages()
	return gotato.TextOf(msgs[len(msgs)-1])
}

// The export script calls the 1 MB export tool, whose result the core spills, and says it is done; a message
// with no directive goes to the wrapped model.
func TestScriptExport(t *testing.T) {
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := scriptedRunner(t, store, "pod-a", "export", "")
	if _, err := a.CreateSessionExclusive(context.Background(), "echo", "s1", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, a, "s1", "T[a] [script:export]"); got != ansExported {
		t.Fatalf("export reply = %q", got)
	}
	if got := lastReply(t, a, "s1", "just talk"); !strings.Contains(got, "just talk") {
		t.Fatalf("a message with no directive goes to the wrapped model: %q", got)
	}
}

func TestScriptMCPCallAndSearch(t *testing.T) {
	srv := httptest.NewServer(fakemcp.New(slog.New(slog.DiscardHandler), true))
	t.Cleanup(srv.Close) // runs after the ToolSets' Close, which ends their connections
	ctx := context.Background()

	active := scriptedRunner(t, session.NewMemoryStore(), "pod-a", "mcp", srv.URL+"/mcp")
	if _, err := active.CreateSessionExclusive(ctx, "echo", "s1", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, active, "s1", "T[a] [script:mcp=10ms]"); got != "mcp-ok: order ord-1: status paid, total 42.50" {
		t.Fatalf("mcp reply = %q", got)
	}

	store := session.NewMemoryStore()
	first := scriptedRunner(t, store, "pod-a", "mcp-search", srv.URL+"/mcp")
	if _, err := first.CreateSessionExclusive(ctx, "echo", "s2", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, first, "s2", "T[a] [script:loaded]"); got != "loaded: false" {
		t.Fatalf("before the search: %q", got)
	}
	if got := lastReply(t, first, "s2", "T[b] [script:search]"); !strings.HasPrefix(got, ansMCPOK+" invoice inv-1") {
		t.Fatalf("search reply = %q", got)
	}
	second := scriptedRunner(t, store, "pod-b", "mcp-search", srv.URL+"/mcp")
	if got := lastReply(t, second, "s2", "T[c] [script:loaded]"); got != "loaded: true" {
		t.Fatalf("on the other instance: %q", got)
	}
}

// A restarted MCP server: a stateless one costs the bot nothing; a stateful one (the SDK's default) has forgotten
// the bot's Mcp-Session-Id, so the first resolution after the restart fails ("session not found", which s7 shows
// as a Run without the MCP tools) and the next one connects again.
func TestMCPServerRestart(t *testing.T) {
	for _, stateless := range []bool{true, false} {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		start := func(ln net.Listener) *http.Server {
			srv := &http.Server{Handler: fakemcp.New(slog.New(slog.DiscardHandler), stateless), ReadHeaderTimeout: time.Second}
			go func() { _ = srv.Serve(ln) }()
			return srv
		}
		srv := start(ln)
		set, err := mcp.NewToolSet(mcpToolSet, mcp.StreamableHTTP("http://"+addr+"/mcp"))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := set.Tools(ctx); err != nil {
			t.Fatalf("stateless=%v: first resolution: %v", stateless, err)
		}
		_ = srv.Close()
		ln2, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		srv2 := start(ln2)
		failures := 0
		for range 3 {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			if _, err := set.Tools(c); err != nil {
				failures++
				t.Logf("stateless=%v: after the restart: %v", stateless, err)
			}
			cancel()
		}
		if failures != 0 {
			t.Errorf("stateless=%v: %d of 3 resolutions after the restart failed", stateless, failures)
		}
		_ = set.Close()
		_ = srv2.Close()
	}
}

// A tool loaded and never called is unloaded by a compaction; the next message sees it not loaded, and loading it
// again works. A Segment closed on one instance is read back with read_segment on another.
func TestScriptCompactionUnloadsAndSegmentIsReadOnAnotherInstance(t *testing.T) {
	srv := httptest.NewServer(fakemcp.New(slog.New(slog.DiscardHandler), true))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	store := session.NewMemoryStore()
	recap := func(a *service.Agent) { a.Compact.Recap = modelctx.TruncateRecap{} }
	a := scriptedRunner(t, store, "pod-a", "mcp-search", srv.URL+"/mcp", recap)
	b := scriptedRunner(t, store, "pod-b", "mcp-search", srv.URL+"/mcp", recap)
	if _, err := a.CreateSessionExclusive(ctx, "echo", "s1", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, a, "s1", "T[a] [script:load]"); got != ansLoadDone {
		t.Fatalf("load reply = %q", got)
	}
	if got := lastReply(t, b, "s1", "T[b] [script:loaded]"); got != "loaded: true" {
		t.Fatalf("before the compaction: %q", got)
	}
	res, err := b.Compact(ctx, "s1", modelctx.CompactOptions{})
	if err != nil || !res.Replaced {
		t.Fatalf("compact = %+v, %v", res, err)
	}
	if got := lastReply(t, a, "s1", "T[c] [script:loaded]"); got != "loaded: false" {
		t.Fatalf("after the compaction: %q", got)
	}
	if got := lastReply(t, a, "s1", "T[d] [script:readseg]"); !strings.HasPrefix(got, ansSegOK) || !strings.Contains(got, "T[a]") {
		t.Fatalf("read_segment on the other instance = %q", got)
	}
	if got := lastReply(t, a, "s1", "T[e] [script:search]"); !strings.HasPrefix(got, ansMCPOK+" invoice inv-1") {
		t.Fatalf("loading again: %q", got)
	}
	rec, err := a.Session(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, unloaded := 0, 0
	for _, ev := range rec.Events() {
		if ev.Kind() == gotato.KindToolLoaded {
			loaded++
		} else if ev.Kind() == gotato.KindToolUnloaded {
			unloaded++
		}
	}
	if loaded != 2 || unloaded != 1 {
		t.Fatalf("tool_loaded %d (want 2), tool_unloaded %d (want 1)", loaded, unloaded)
	}
}
