package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// docTool is a Tool with a description that counts its runs.
type docTool struct {
	id, description string
	effect          ToolEffect
	runs            atomic.Int32
}

func (t *docTool) Spec() ToolSpec {
	return ToolSpec{
		ID: t.id, Description: t.description, Effect: t.effect,
		InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (t *docTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	t.runs.Add(1)
	return ToolResult{Content: []ContentPart{{Kind: ContentText, Text: "ran " + t.id}}}, nil
}

type describedSet struct {
	name, description string
	tools             []Tool
}

func (s *describedSet) Spec() ToolSetSpec {
	return ToolSetSpec{Name: s.name, Description: s.description}
}
func (s *describedSet) Tools(context.Context) ([]Tool, error) { return s.tools, nil }

func ordersSet() (*describedSet, map[string]*docTool) {
	tools := map[string]*docTool{
		"lookup": {id: "lookup", description: "Look up one order by its number", effect: EffectRead},
		"list":   {id: "list_open", description: "List the open orders of a customer", effect: EffectRead},
		"refund": {id: "refund", description: "Refund a payment for an order", effect: EffectWriteExternal},
	}
	return &describedSet{name: "orders", description: "Order lookup and refunds",
		tools: []Tool{tools["lookup"], tools["list"], tools["refund"]}}, tools
}

func searchCall(id string, arguments map[string]any) []ModelEvent {
	encoded, _ := json.Marshal(arguments)
	return []ModelEvent{
		{Kind: ModelToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: SearchToolName, Arguments: encoded}},
		{Kind: ModelDone, StopReason: StopToolCalls},
	}
}

func callTool(id, toolID string) []ModelEvent {
	return []ModelEvent{
		{Kind: ModelToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: toolID, Arguments: []byte(`{}`)}},
		{Kind: ModelDone, StopReason: StopToolCalls},
	}
}

func resultOf(t *testing.T, model *recordingModel, request int, callID string) ToolResult {
	t.Helper()
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, message := range model.requests[request] {
		if message.ToolResult != nil && string(message.ToolResult.CallID) == callID {
			result := message.ToolResult.Clone()
			result.Content = cloneContent(message.Parts)
			return result
		}
	}
	t.Fatalf("request %d holds no result for %s", request, callID)
	return ToolResult{}
}

func textOfResult(result ToolResult) string { return TextOf(Message{Parts: result.Content}) }

func descriptionOf(model *recordingModel, request int, id string) string {
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, spec := range model.offered[request] {
		if spec.ID == id {
			return spec.Description
		}
	}
	return ""
}

// runCollecting plays one Prompt and returns the ToolLoaded and ToolUnloaded
// Events of the Run.
func runCollecting(t *testing.T, agent *Session, text string) (loaded []ToolLoaded) {
	t.Helper()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()
	if _, err := agent.Prompt(context.Background(), UserMessage(text)); err != nil {
		t.Fatal(err)
	}
	for {
		event, err := events.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := event.(ToolLoaded); ok {
			loaded = append(loaded, e)
		}
		if event.Kind() == KindRunEnded {
			return loaded
		}
	}
}

func newSearchSession(t *testing.T, model Model, options ...Option) *Session {
	t.Helper()
	agent, err := NewSession(append([]Option{WithModel(model)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	return agent
}

func TestSearchToolLoadsByNameAndKeepsTheCatalogSteady(t *testing.T) {
	set, tools := ordersSet()
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s", map[string]any{"names": []string{"orders.lookup"}}),
		callTool("c", "orders.lookup"),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set))
	loads := runCollecting(t, agent, "find order 7")

	if got := model.toolIDs(0); !slices.Equal(got, []string{SearchToolName}) {
		t.Fatalf("first request tools = %v, want only search_tools", got)
	}
	if got := model.toolIDs(1); !slices.Equal(got, []string{"orders.lookup", SearchToolName}) {
		t.Fatalf("second request tools = %v", got)
	}
	// Only the loaded tool joined: the list grows at its end of the sort order,
	// and the request holds no other orders.* tool.
	if tools["lookup"].runs.Load() != 1 {
		t.Fatalf("the loaded tool ran %d times", tools["lookup"].runs.Load())
	}
	if text := textOfResult(resultOf(t, model, 1, "s")); text != "Loaded: orders.lookup." {
		t.Fatalf("search result = %q", text)
	}
	if len(loads) != 1 || loads[0].Tool != "orders.lookup" || loads[0].Why != ToolLoadedNames || loads[0].Number == 0 {
		t.Fatalf("tool_loaded = %+v", loads)
	}
	// The description is the catalog, and it is the same bytes while tools load.
	first := descriptionOf(model, 0, SearchToolName)
	for request := 1; request < 3; request++ {
		if got := descriptionOf(model, request, SearchToolName); got != first {
			t.Fatalf("the catalog changed at request %d:\n%q\n%q", request, first, got)
		}
	}
	for _, want := range []string{"orders — Order lookup and refunds", "orders.list_open, orders.lookup, orders.refund"} {
		if !strings.Contains(first, want) {
			t.Fatalf("catalog lacks %q:\n%s", want, first)
		}
	}
}

func TestSearchToolLoadsTheTopThreeOfAQueryAndListsFiveMore(t *testing.T) {
	var tools []Tool
	for i := range 10 {
		tools = append(tools, &docTool{id: fmt.Sprintf("t%02d", i), description: "reads the ledger entry"})
	}
	tools = append(tools, &docTool{id: "other", description: "sends mail"})
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s", map[string]any{"queries": []string{"ledger"}}),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(&describedSet{name: "ops", tools: tools}))
	loads := runCollecting(t, agent, "go")

	text := textOfResult(resultOf(t, model, 1, "s"))
	want := "Loaded: ops.t00, ops.t01, ops.t02. Also found, not loaded: ops.t03, ops.t04, ops.t05, ops.t06, ops.t07."
	if text != want {
		t.Fatalf("search result = %q, want %q", text, want)
	}
	if len(loads) != 3 || loads[0].Why != ToolLoadedQuery {
		t.Fatalf("tool_loaded = %+v", loads)
	}
	if got := model.toolIDs(1); !slices.Equal(got, []string{"ops.t00", "ops.t01", "ops.t02", SearchToolName}) {
		t.Fatalf("tools after the query = %v", got)
	}
}

func TestACallToAnUnloadedCatalogToolLoadsItAndDoesNotRunIt(t *testing.T) {
	set, tools := ordersSet()
	model := &recordingModel{scripts: [][]ModelEvent{
		callTool("c1", "orders.refund"),
		callTool("c2", "orders.refund"),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set))
	loads := runCollecting(t, agent, "refund it")

	result := resultOf(t, model, 1, "c1")
	if result.Status != ToolResultFailed || result.Executed {
		t.Fatalf("result = %+v, want a failed result that did not run", result)
	}
	if want := "orders.refund was not loaded, so this call did not run. It is loaded now; call it again."; !strings.Contains(textOfResult(result), want) {
		t.Fatalf("result text = %q", textOfResult(result))
	}
	if len(loads) != 1 || loads[0].Tool != "orders.refund" || loads[0].Why != ToolLoadedCall {
		t.Fatalf("tool_loaded = %+v", loads)
	}
	if !slices.Contains(model.toolIDs(1), "orders.refund") {
		t.Fatalf("the tool is not offered after the call: %v", model.toolIDs(1))
	}
	// Calling it again runs it.
	if tools["refund"].runs.Load() != 1 {
		t.Fatalf("refund ran %d times, want once (the second call)", tools["refund"].runs.Load())
	}
	// A name no ToolSet carries is an unknown tool, and loads nothing.
	model2 := &recordingModel{scripts: [][]ModelEvent{callTool("c", "orders.nothing"), finalScript("done")}}
	agent2 := newSearchSession(t, model2, WithToolSet(set))
	if loads := runCollecting(t, agent2, "x"); len(loads) != 0 {
		t.Fatalf("an unknown tool loaded %+v", loads)
	}
	if text := resultOf(t, model2, 1, "c").SafeError; !strings.Contains(text, "unknown tool") {
		t.Fatalf("unknown tool result = %q", text)
	}
}

func TestTwoLoadsOfOneToolInABatchEmitOneFact(t *testing.T) {
	set, _ := ordersSet()
	call := func(id string) ModelEvent {
		return ModelEvent{Kind: ModelToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: SearchToolName, Arguments: []byte(`{"names":["orders.lookup"]}`)}}
	}
	model := &recordingModel{scripts: [][]ModelEvent{
		{call("a"), call("b"), {Kind: ModelDone, StopReason: StopToolCalls}},
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set))
	if loads := runCollecting(t, agent, "x"); len(loads) != 1 {
		t.Fatalf("tool_loaded = %+v, want one", loads)
	}
}

func TestAFoldedToolSetShowsACountAndExpandsWithoutLoading(t *testing.T) {
	set, _ := ordersSet()
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("e", map[string]any{"toolset": "orders"}),
		searchCall("l", map[string]any{"names": []string{"orders.list_open"}}),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithFoldedToolSet(set))
	loads := runCollecting(t, agent, "x")

	catalog := descriptionOf(model, 0, SearchToolName)
	if !strings.Contains(catalog, `orders — Order lookup and refunds (3 tools; pass toolset="orders" to list them)`) || strings.Contains(catalog, "orders.lookup") {
		t.Fatalf("folded catalog = %q", catalog)
	}
	expansion := textOfResult(resultOf(t, model, 1, "e"))
	for _, want := range []string{"orders has 3 tools:", "orders.lookup — Look up one order by its number", "orders.refund — Refund a payment for an order"} {
		if !strings.Contains(expansion, want) {
			t.Fatalf("expansion lacks %q:\n%s", want, expansion)
		}
	}
	// The expansion loaded nothing; the load by name did.
	if got := model.toolIDs(1); !slices.Equal(got, []string{SearchToolName}) {
		t.Fatalf("tools after the expansion = %v, want only search_tools", got)
	}
	if len(loads) != 1 || loads[0].Tool != "orders.list_open" {
		t.Fatalf("tool_loaded = %+v", loads)
	}
	if got := descriptionOf(model, 2, SearchToolName); got != catalog {
		t.Fatalf("catalog changed after a load:\n%q\n%q", catalog, got)
	}
	// An unknown ToolSet is reported, not an error.
	if text := textOfResult(resultOf(t, model, 2, "l")); text != "Loaded: orders.list_open." {
		t.Fatalf("load result = %q", text)
	}
}

func TestAnActiveToolSetIsNeverInTheCatalog(t *testing.T) {
	set, _ := ordersSet()
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent := newSearchSession(t, model, WithActiveToolSet(set))
	runCollecting(t, agent, "x")
	if got := model.toolIDs(0); !slices.Equal(got, []string{"orders.list_open", "orders.lookup", "orders.refund"}) {
		t.Fatalf("tools = %v, want every tool and no search_tools", got)
	}
}

func TestAToolSwitchedOffForTheSessionIsNeverIndexedOrLoaded(t *testing.T) {
	set, tools := ordersSet()
	keep := func(id string) bool { return id != "orders.refund" }
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s", map[string]any{"names": []string{"orders.refund"}, "queries": []string{"refund payment"}}),
		callTool("c", "orders.refund"),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set), WithToolFilter(keep))
	loads := runCollecting(t, agent, "x")

	if catalog := descriptionOf(model, 0, SearchToolName); strings.Contains(catalog, "refund") && strings.Contains(catalog, "orders.refund") {
		t.Fatalf("the catalog lists a switched-off tool: %s", catalog)
	}
	text := textOfResult(resultOf(t, model, 1, "s"))
	if !strings.Contains(text, "Not found: orders.refund.") || strings.Contains(text, "Loaded: orders.refund") {
		t.Fatalf("search result = %q", text)
	}
	if len(loads) != 0 || tools["refund"].runs.Load() != 0 {
		t.Fatalf("a switched-off tool was loaded (%+v) or ran", loads)
	}
	if text := resultOf(t, model, 2, "c").SafeError; !strings.Contains(text, "unknown tool") {
		t.Fatalf("call to a switched-off tool = %q", text)
	}
	// A Record that remembers the tool as loaded does not bring it back.
	restored := newSearchSession(t, &recordingModel{scripts: [][]ModelEvent{finalScript("x")}},
		WithToolSet(set), WithToolFilter(keep), WithTranscript(&loadedNames{names: []string{"orders.refund", "orders.lookup"}}))
	var ids []string
	for _, spec := range restored.registry.visibleSpecs() {
		ids = append(ids, spec.ID)
	}
	if !slices.Equal(ids, []string{"orders.lookup", SearchToolName}) {
		t.Fatalf("restored tools = %v", ids)
	}
}

// loadedNames is a Transcript that remembers loaded tools, as a session.Record does.
type loadedNames struct {
	memoryTranscript
	names []string
}

func (l *loadedNames) LoadedTools() []string { return l.names }

func TestEveryInstanceOffersTheSameToolsFromTheSameRecord(t *testing.T) {
	build := func() ModelRequest {
		set, _ := ordersSet()
		other := &describedSet{name: "mail", tools: []Tool{&docTool{id: "send", description: "send mail"}}}
		agent := newSearchSession(t, &recordingModel{scripts: [][]ModelEvent{finalScript("x")}},
			WithToolSet(set), WithToolSet(other),
			// A name nothing carries, and a tool of an active ToolSet, are left out.
			WithActiveToolSet(&describedSet{name: "base", tools: []Tool{&docTool{id: "ping"}}}),
			WithTranscript(&loadedNames{names: []string{"orders.refund", "gone.tool", "base.ping", "mail.send", "orders.refund"}}))
		inspection, err := agent.InspectContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return inspection.Request
	}
	a, b := build(), build()
	left, _ := json.Marshal(a.Tools)
	right, _ := json.Marshal(b.Tools)
	if string(left) != string(right) {
		t.Fatalf("two instances built different tool lists:\n%s\n%s", left, right)
	}
	var ids []string
	for _, spec := range a.Tools {
		ids = append(ids, spec.ID)
	}
	if want := []string{"base.ping", "mail.send", "orders.refund", SearchToolName}; !slices.Equal(ids, want) {
		t.Fatalf("tools = %v, want %v", ids, want)
	}
}

func TestACompactionNarrowsTheLoadedTools(t *testing.T) {
	set, _ := ordersSet()
	transcript := &loadedNames{names: []string{"orders.lookup", "orders.refund"}}
	agent := newSearchSession(t, &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}, WithToolSet(set), WithTranscript(transcript))
	if got := agent.registry.loaded; !slices.Equal(got, []string{"orders.lookup", "orders.refund"}) {
		t.Fatalf("loaded = %v", got)
	}
	transcript.names = []string{"orders.refund"}
	if err := agent.syncLoadedTools(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, spec := range agent.registry.visibleSpecs() {
		ids = append(ids, spec.ID)
	}
	if !slices.Equal(ids, []string{"orders.refund", SearchToolName}) {
		t.Fatalf("tools after the sync = %v", ids)
	}
}

type reverseSearcher struct{ queries []string }

func (r *reverseSearcher) Search(_ context.Context, query string, tools []ToolSpec) ([]string, error) {
	r.queries = append(r.queries, query)
	ids := make([]string, 0, len(tools))
	for _, spec := range tools {
		ids = append(ids, spec.ID)
	}
	slices.Reverse(ids)
	return append(ids, "not.a.candidate"), nil
}

func TestACustomToolSearcherDecidesWhatLoads(t *testing.T) {
	set, _ := ordersSet()
	searcher := &reverseSearcher{}
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s", map[string]any{"queries": []string{"anything"}}),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set), WithToolSearcher(searcher))
	runCollecting(t, agent, "x")
	if !slices.Equal(searcher.queries, []string{"anything"}) {
		t.Fatalf("queries = %v", searcher.queries)
	}
	if text := textOfResult(resultOf(t, model, 1, "s")); text != "Loaded: orders.refund, orders.lookup, orders.list_open." {
		t.Fatalf("search result = %q (the custom order, without the unknown ID)", text)
	}
	if _, err := NewSession(WithModel(model), WithToolSearcher(nil)); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("nil searcher = %v", err)
	}
}

func TestBM25RanksTheBestMatchFirst(t *testing.T) {
	specs := []ToolSpec{
		{ID: "mail.send", Description: "Send an email message to a recipient"},
		{ID: "orders.refund", Description: "Refund a payment for an order"},
		{ID: "orders.lookup", Description: "Look up one order by its number"},
		{ID: "files.read", Description: "Read a file from disk"},
	}
	rank := func(query string) []string {
		ids, err := bm25Searcher{}.Search(context.Background(), query, specs)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := rank("refund a payment"); len(got) == 0 || got[0] != "orders.refund" {
		t.Errorf("refund a payment = %v", got)
	}
	if got := rank("email"); !slices.Equal(got, []string{"mail.send"}) {
		t.Errorf("email = %v", got)
	}
	// A term in the ID outweighs the same term in a description.
	if got := rank("order"); len(got) < 2 || got[0] == "mail.send" {
		t.Errorf("order = %v", got)
	}
	if got := rank("lookup"); len(got) == 0 || got[0] != "orders.lookup" {
		t.Errorf("lookup = %v", got)
	}
	if got := rank("zebra"); len(got) != 0 {
		t.Errorf("zebra = %v, want no match", got)
	}
	if got := rank("   "); len(got) != 0 {
		t.Errorf("a blank query = %v", got)
	}
	// The same input ranks the same way every time.
	if a, b := rank("order payment"), rank("order payment"); !slices.Equal(a, b) {
		t.Errorf("rank is not deterministic: %v %v", a, b)
	}
}
