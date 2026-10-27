package workload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// This file checks G-G07 and G-G09 on the tool_search task, a Session whose Tools
// load and unload (GOALS.md → G-G07 → Tools that load).

// hasCatalog says the request is a Session's with a catalog of loadable Tools.
func hasCatalog(request gotato.ModelRequest) bool {
	return len(request.LoadedTools) > 0 || len(request.UnloadedTools) > 0
}

// catalogOf returns, by ID, the JSON of every catalog Tool of a request,
// loaded or not.
func catalogOf(t *testing.T, request gotato.ModelRequest) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, spec := range request.Tools {
		if slices.Contains(request.LoadedTools, spec.ID) {
			out[spec.ID] = string(jsonOf(t, spec))
		}
	}
	for _, spec := range request.UnloadedTools {
		out[spec.ID] = string(jsonOf(t, spec))
	}
	return out
}

// loadsBetween returns the IDs the Tool Results of the Messages next adds to
// prev list as loaded.
func loadsBetween(prev, next gotato.ModelRequest) []string {
	var ids []string
	for _, message := range next.Messages[min(len(prev.Messages), len(next.Messages)):] {
		if message.ToolResult != nil {
			for _, id := range message.ToolResult.Loaded {
				if !slices.Contains(prev.LoadedTools, id) && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// checkToolLoading checks the tools of two consecutive requests of a Session
// that does not compact between them. What the Model can reach is the same:
// every catalog Tool, loaded or not, has the same spec in both. The Tools that
// are not in the catalog are the same. What differs is which catalog Tools are
// loaded, and the loaded ones of the later request are the earlier ones and
// those the Tool Results added between the two request list as loaded, no more.
func checkToolLoading(t *testing.T, pair string, prev, next gotato.ModelRequest) {
	t.Helper()
	if before, after := catalogOf(t, prev), catalogOf(t, next); fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("%s: the catalog changed\nbefore: %v\n after: %v", pair, before, after)
	}
	plain := func(request gotato.ModelRequest) []string {
		var out []string
		for _, spec := range request.Tools {
			if !slices.Contains(request.LoadedTools, spec.ID) {
				out = append(out, string(jsonOf(t, spec)))
			}
		}
		return out
	}
	if !slices.Equal(plain(prev), plain(next)) {
		t.Errorf("%s: a Tool outside the catalog changed", pair)
	}
	want := append(slices.Clone(prev.LoadedTools), loadsBetween(prev, next)...)
	slices.Sort(want)
	got := slices.Clone(next.LoadedTools)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%s: loaded Tools %v, want the earlier ones and the new loads: %v", pair, got, want)
	}
}

// wireBodies encodes every request with the gateway for one wire API and
// deferred form, as the gateway would send it, and returns the JSON bodies and the form the Client used.
func wireBodies(t *testing.T, api, form string, requests []gotato.ModelRequest) ([]map[string]json.RawMessage, string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer server.Close()
	client, err := gateway.New(gateway.Config{API: api, Model: "m", BaseURL: server.URL, APIKey: "k", DeferredTools: form})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		stream, err := client.Stream(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != len(requests) {
		t.Fatalf("%d bodies for %d requests", len(bodies), len(requests))
	}
	return bodies, client.DeferredTools()
}

// stripCacheControl removes the cache_control marks, which move to the end of
// the request by design, from decoded JSON.
func stripCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "cache_control")
		for key, value := range x {
			x[key] = stripCacheControl(value)
		}
	case []any:
		for i := range x {
			x[i] = stripCacheControl(x[i])
		}
	}
	return v
}

// elementsOf decodes a JSON array into its elements, without cache_control.
func elementsOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var items []any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = string(jsonOf(t, stripCacheControl(item)))
	}
	return out
}

// TestToolSearchRequestOnlyGrowsAtItsEnd checks the tool loading part of
// GOALS.md G-G07 as the core builds the requests. The tool_search task is played
// as several Runs across instances and reloads. In every pair of consecutive
// requests the catalog is the same, and the loaded Tools are the earlier ones
// and those the Tool Results between the two list as loaded (checkToolLoading).
func TestToolSearchRequestOnlyGrowsAtItsEnd(t *testing.T) {
	_, sent := playAcross(t, session.NewMemoryStore(), reftasks.ToolSearchTask(), playOptions{})
	requests := decodeRequests(t, sent)
	loads := 0
	for n := 1; n < len(requests); n++ {
		checkToolLoading(t, fmt.Sprintf("requests %d and %d", n, n+1), requests[n-1], requests[n])
		if len(loadsBetween(requests[n-1], requests[n])) > 0 {
			loads++
		}
	}
	if loads < 5 {
		t.Fatalf("only %d pairs with a load", loads)
	}
	t.Logf("%d request pairs, %d of them across a load", len(requests)-1, loads)
}

// TestWireRequestOnlyGrowsAtItsEnd checks G-G07 on the bytes the gateway
// encodes, for every reference task, on each wire API and each form of deferred
// tools. Each task is played as in TestRequestOnlyGrowsAtItsEnd, and the
// gateway encodes its requests. Between two consecutive requests of a pair:
//
//   - the instruction keeps its bytes;
//   - the Messages of the earlier request come first in the later one, byte for
//     byte, but for the cache marks, which move to the end by design;
//   - in the native form of deferred tools the tool list keeps its bytes. In
//     the list form (the form of openai-chat-completions) it changes in exactly
//     the pairs where a Tool loaded, and only by adding Tools.
//
// A task without a catalog never changes its tool list, in either form.
func TestWireRequestOnlyGrowsAtItsEnd(t *testing.T) {
	wires := []struct {
		api, system, tools, messages string
	}{
		{gateway.APIAnthropicMessages, "system", "tools", "messages"},
		{gateway.APIResponses, "instructions", "tools", "input"},
		{gateway.APIChatCompletions, "", "tools", "messages"},
	}
	for _, task := range reftasks.ReferenceTasks() {
		_, sent := playAcross(t, session.NewMemoryStore(), task, playOptions{})
		requests := decodeRequests(t, sent)
		for _, wire := range wires {
			for _, form := range []string{gateway.DeferredNative, gateway.DeferredList} {
				t.Run(task.Name+"/"+wire.api+"/"+form, func(t *testing.T) {
					bodies, used := wireBodies(t, wire.api, form, requests)
					changed := 0
					for n := 1; n < len(bodies); n++ {
						prev, next := bodies[n-1], bodies[n]
						pair := fmt.Sprintf("requests %d and %d", n, n+1)
						if wire.system != "" && !bytes.Equal(prev[wire.system], next[wire.system]) {
							t.Errorf("%s: the instruction changed", pair)
						}
						loaded := len(loadsBetween(requests[n-1], requests[n])) > 0
						same := bytes.Equal(prev[wire.tools], next[wire.tools])
						switch {
						case used == gateway.DeferredNative && !same:
							t.Errorf("%s: the tool list changed in the native form\nbefore: %s\n after: %s", pair, prev[wire.tools], next[wire.tools])
						case used == gateway.DeferredList && same == loaded:
							t.Errorf("%s: the tool list unchanged = %t, but a Tool loaded = %t", pair, same, loaded)
						}
						if !same {
							changed++
							before, after := elementsOf(t, prev[wire.tools]), elementsOf(t, next[wire.tools])
							for _, tool := range before {
								if !slices.Contains(after, tool) {
									t.Errorf("%s: a Tool left the list: %.100s", pair, tool)
								}
							}
						}
						before, after := elementsOf(t, prev[wire.messages]), elementsOf(t, next[wire.messages])
						if len(after) < len(before) || !slices.Equal(before, after[:len(before)]) {
							t.Errorf("%s: the Messages of the earlier request do not come first, byte for byte", pair)
						}
					}
					if task.Name == reftasks.ToolSearchName && used == gateway.DeferredList && changed == 0 {
						t.Errorf("the list form never changed its tool list while Tools loaded")
					}
				})
			}
		}
	}
}

// compactingToolSearch plays the tool_search task with an explicit compaction
// after Run 5, and returns the Session's ID, the store and the requests.
func compactingToolSearch(t *testing.T) (session.Store, string, []gotato.ModelRequest) {
	t.Helper()
	store := session.NewMemoryStore()
	id, sent := playAcross(t, store, reftasks.ToolSearchTask(), playOptions{compactAfter: 5})
	return store, id, decodeRequests(t, sent)
}

// TestToolSearchRequestAcrossACompaction checks the other half of G-G07 for
// Tools that load: when the Session compacts, some of its Tools unload, and
// that is the one pair where the loaded Tools may lose some. Every other pair
// follows checkToolLoading. At the compaction the catalog is the same, the
// loaded Tools are fewer, and the Segment's read_segment may join the Tools.
func TestToolSearchRequestAcrossACompaction(t *testing.T) {
	ctx := context.Background()
	store, id, requests := compactingToolSearch(t)
	final, err := session.Open(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Compactions()) != 1 {
		t.Fatalf("%d compactions, want 1", len(final.Compactions()))
	}
	events, err := session.StoredEvents(ctx, store, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var unloaded []string
	for _, event := range events {
		if e, ok := event.(gotato.ToolUnloaded); ok {
			if e.Why != gotato.ToolUnloadedWhyCompaction {
				t.Errorf("%s unloaded for %q, want the compaction", e.Tool, e.Why)
			}
			unloaded = append(unloaded, e.Tool)
		}
	}
	if len(unloaded) == 0 {
		t.Fatal("the compaction unloaded nothing")
	}
	breaks := 0
	for n := 1; n < len(requests); n++ {
		prev, next := requests[n-1], requests[n]
		pair := fmt.Sprintf("requests %d and %d", n, n+1)
		if len(next.LoadedTools) >= len(prev.LoadedTools) && !slices.ContainsFunc(prev.LoadedTools, func(id string) bool { return !slices.Contains(next.LoadedTools, id) }) {
			checkToolLoading(t, pair, prev, next)
			continue
		}
		breaks++
		for _, id := range unloaded {
			if slices.Contains(next.LoadedTools, id) {
				t.Errorf("%s: %s is still loaded after the compaction unloaded it", pair, id)
			}
		}
		withoutSegment := func(request gotato.ModelRequest) map[string]string {
			catalog := catalogOf(t, request)
			delete(catalog, modelctx.ReadSegmentID)
			return catalog
		}
		if before, after := withoutSegment(prev), withoutSegment(next); fmt.Sprint(before) != fmt.Sprint(after) {
			t.Errorf("%s: the catalog changed at the compaction", pair)
		}
	}
	if breaks != 1 {
		t.Fatalf("%d pairs lose a loaded Tool, want the one at the compaction", breaks)
	}
	t.Logf("compaction unloaded %v", unloaded)
}

// TestSameRecordSameRequestAcrossLoadsAndUnloads checks G-G09 on the tool_search
// task, with a compaction after Run 5. At the end of every Run, the Session is
// inspected by the instance that ran it, by the other instance, and by a new
// Runner after a reload; the three build the same request. The Runs load Tools
// by name, by query and by listing a folded ToolSet, and the compaction unloads
// some, and every Run begins by resolving the ToolSets again.
func TestSameRecordSameRequestAcrossLoadsAndUnloads(t *testing.T) {
	ctx := context.Background()
	task := reftasks.ToolSearchTask()
	store := session.NewMemoryStore()
	compared, loadedAtRun := 0, []int{}
	stages := []string{"a", "a", "b", "a"}
	runner := taskRunner(t, store, task, "a")
	s, err := runner.CreateSession(ctx, task.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, prompt := range task.Prompts {
		if i%len(stages) != 0 {
			if err := runner.Close(ctx); err != nil {
				t.Fatal(err)
			}
			runner = taskRunner(t, store, task, stages[i%len(stages)])
		}
		if result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt}); err != nil || result.Result.Status != gotato.RunStatusCompleted {
			t.Fatalf("Run %d: %v (%s)", i+1, err, result.Result.Status)
		}
		if i+1 == 5 {
			if _, err := runner.Compact(ctx, s.ID(), modelctx.CompactOptions{Keep: 2}); err != nil {
				t.Fatal(err)
			}
		}
		live, err := runner.Inspect(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		want := jsonOf(t, live.Request)
		loadedAtRun = append(loadedAtRun, len(live.Request.LoadedTools))
		for name, view := range map[string]*service.Runner{
			"the other instance":      taskRunner(t, store, task, "other"),
			"a new Runner (a reload)": taskRunner(t, store, task, runner.Instance()),
		} {
			report, err := view.Inspect(ctx, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			if got := jsonOf(t, report.Request); !bytes.Equal(got, want) {
				t.Fatalf("Run %d: %s builds another request\n got: %.300s\nwant: %.300s", i+1, name, got, want)
			}
		}
		compared++
	}
	if compared != len(task.Prompts) {
		t.Fatalf("compared %d Runs of %d", compared, len(task.Prompts))
	}
	// Run 5 loads shop.address and the compaction after it unloads
	// carrier.track, which Run 2 loaded and no Run called, so the count does
	// not grow at Run 5; Run 6 loads carrier.track again.
	if loadedAtRun[4] != loadedAtRun[3] || loadedAtRun[5] != loadedAtRun[4]+1 {
		t.Errorf("loaded Tools at the end of each Run: %v; want the compaction to unload one, and Run 6 to load it again", loadedAtRun)
	}
	t.Logf("loaded Tools at the end of each Run: %v", loadedAtRun)
}
