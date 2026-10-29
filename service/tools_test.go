package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// shrinkingToolSet answers with the tools it is told to.
type shrinkingToolSet struct {
	mu    sync.Mutex
	names []string
}

func (s *shrinkingToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "files"} }

func (s *shrinkingToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tools []gotato.Tool
	for _, name := range s.names {
		tools = append(tools, testkit.NewFakeTool(name, "contents"))
	}
	return tools, nil
}

// A loaded Tool that its ToolSet no longer carries is unloaded by a stored
// Event, so a second instance reads the same loaded Tools from the Record
// whatever its own ToolSet answers.
func TestAGoneToolIsUnloadedInTheRecordForEveryInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	set := &shrinkingToolSet{names: []string{"read"}}
	model := testkit.NewFakeModel(loadFiles(), testkit.Text("opened"), testkit.Text("second"))
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "a", Model: model, Options: []gotato.SessionOption{gotato.WithToolSet(set)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "open the files"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := session.Open(ctx, store, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.LoadedTools(); !slices.Equal(got, []string{"files.read"}) {
		t.Fatalf("loaded after Run 1 = %v", got)
	}

	set.mu.Lock()
	set.names = []string{"stat"}
	set.mu.Unlock()
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if ids := toolIDsOf(model.Requests()[2]); slices.Contains(ids, "files.read") {
		t.Fatalf("a gone tool is still offered: %v", ids)
	}
	record, err = session.Open(ctx, store, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.LoadedTools(); len(got) != 0 {
		t.Fatalf("the Record still holds %v as loaded", got)
	}
}

// hangingToolSet never answers until its context ends.
type hangingToolSet struct{}

func (hangingToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "slow"} }

func (hangingToolSet) Tools(ctx context.Context) ([]gotato.Tool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func slowRunner(t *testing.T) (*service.Runner, string) {
	t.Helper()
	agent := service.Agent{
		Name:    "a",
		Model:   testkit.NewFakeModel(testkit.Text("ok")),
		Tools:   []gotato.Tool{testkit.NewFakeTool("own", "x")},
		Options: []gotato.SessionOption{gotato.WithToolSet(hangingToolSet{})},
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{agent}, ToolSetTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s, err := runner.CreateSession(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	return runner, s.ID()
}

// A ToolSet that does not answer cannot hold up the listing: Tools gives up
// after ToolSetTimeout and still lists the Agent's own tools, with the error.
func TestToolsReturnsWithinTheBoundWhenAToolSetHangs(t *testing.T) {
	runner, id := slowRunner(t)
	start := time.Now()
	entries, err := runner.Tools(context.Background(), "", id)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Tools took %v", time.Since(start))
	}
	if !gotato.IsCode(err, gotato.CodeDeadlineExceeded) {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
	if _, ok := entryOf(entries, "own"); !ok {
		t.Fatalf("entries = %+v, want the Agent's own tool", entries)
	}
}

// Switching an own tool never waits for a ToolSet, and switching a tool of a
// ToolSet that hangs is refused within the bound, so the Session lock is not
// held for a ToolSet's answer.
func TestSetToolActiveDoesNotWaitForAHangingToolSet(t *testing.T) {
	runner, id := slowRunner(t)
	ctx := context.Background()
	start := time.Now()
	entry, err := runner.SetToolActive(ctx, id, "own", false)
	if err != nil || entry.Active {
		t.Fatalf("own = %+v, %v", entry, err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("an own tool waited for a ToolSet: %v", time.Since(start))
	}
	start = time.Now()
	_, err = runner.SetToolActive(ctx, id, "slow.x", false)
	if !gotato.IsCode(err, gotato.CodeDeadlineExceeded) {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("SetToolActive took %v", time.Since(start))
	}
}

func loadFiles() testkit.Script {
	arguments, _ := json.Marshal(map[string][]string{"names": {"files.read"}})
	return testkit.ToolCalls(gotato.ToolCall{ID: "load-1", ToolID: gotato.SearchToolName, Arguments: arguments})
}

func toolSetRunner(t *testing.T, store session.Store, model gotato.Model) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "a", Model: model, Options: []gotato.SessionOption{gotato.WithToolSet(filesToolSet{})}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// TestLoadedToolStaysLoadedInLaterRuns is an exit test of tool search: a
// tool the model loads in one Run is in the first request of the
// next Run, also through another Runner on the same store, and through a
// fork and a compaction.
func TestLoadedToolStaysLoadedInLaterRuns(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(loadFiles(), testkit.Text("opened"), testkit.Text("second"),
		testkit.ToolCalls(gotato.ToolCall{ID: "read-1", ToolID: "files.read", Arguments: []byte(`{}`)}), testkit.Text("third"), testkit.Text("fourth"))
	runner := toolSetRunner(t, store, model)

	first, err := runner.Run(ctx, service.RunRequest{Prompt: "open the files"})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if slices.Contains(toolIDsOf(requests[0]), "files.read") || !slices.Contains(toolIDsOf(requests[0]), gotato.SearchToolName) {
		t.Fatalf("Run 1 first request tools = %v", toolIDsOf(requests[0]))
	}
	if !slices.Contains(toolIDsOf(requests[1]), "files.read") {
		t.Fatalf("Run 1 second request tools = %v", toolIDsOf(requests[1]))
	}

	// Run 2, same Runner.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	requests = model.Requests()
	run2 := toolIDsOf(requests[2])
	if !slices.Contains(run2, "files.read") || !slices.Contains(run2, gotato.SearchToolName) {
		t.Fatalf("Run 2 first request tools = %v", run2)
	}

	// Run 3, a second Runner (another process) on the same store.
	other := toolSetRunner(t, store, model)
	if _, err := other.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "third"}); err != nil {
		t.Fatal(err)
	}
	requests = model.Requests()
	if !slices.Contains(toolIDsOf(requests[3]), "files.read") {
		t.Fatalf("Run 3 first request tools = %v", toolIDsOf(requests[3]))
	}

	// A compaction writes a Checkpoint that keeps the load of a tool
	// the closed Segment called (Run 3 called files.read).
	if _, err := other.Compact(ctx, first.SessionID, modelctx.CompactConfig{KeepMessages: 2}); err != nil {
		t.Fatal(err)
	}
	// A fork starts with it too.
	fork, err := other.Fork(ctx, first.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := fork.LoadedTools(); !slices.Equal(got, []string{"files.read"}) {
		t.Fatalf("fork LoadedTools = %v", got)
	}
	if _, err := other.Run(ctx, service.RunRequest{SessionID: fork.ID(), Prompt: "in the fork"}); err != nil {
		t.Fatal(err)
	}
	requests = model.Requests()
	if !slices.Contains(toolIDsOf(requests[5]), "files.read") {
		t.Fatalf("the fork's first request tools = %v", toolIDsOf(requests[5]))
	}
	reopened, err := session.Open(ctx, store, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.LoadedTools(); !slices.Equal(got, []string{"files.read"}) {
		t.Fatalf("after a compaction LoadedTools = %v", got)
	}
}

// TestAToolSwitchedOffForASessionIsNeverInTheCatalogOrLoaded: the Session's
// switch (gotato.tool.<id>) wins over loading, for a tool of a ToolSet too.
func TestAToolSwitchedOffForASessionIsNeverInTheCatalogOrLoaded(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(loadFiles(), testkit.Text("done"))
	runner := toolSetRunner(t, session.NewMemoryStore(), model)
	result, err := runner.Run(ctx, service.RunRequest{
		Prompt:   "open the files",
		Metadata: map[string]string{service.MetaToolPrefix + "files.read": "inactive"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	// Nothing in the ToolSet may load, yet the search tool stays offered
	// while a catalog ToolSet is installed.
	if got := toolIDsOf(requests[0]); slices.Contains(got, "files.read") || !slices.Contains(got, gotato.SearchToolName) {
		t.Fatalf("tools = %v, want search_tools and no files.read", got)
	}
	record, err := session.View(ctx, runner.Store(), result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.LoadedTools(); len(got) != 0 {
		t.Fatalf("LoadedTools = %v", got)
	}
}

// twoToolSet is a ToolSet of a read tool and a write tool.
type twoToolSet struct{}

func (twoToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "files"} }

func (twoToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{
		gotato.ToolWithEffect(testkit.NewFakeTool("read", "contents"), gotato.ToolEffectRead),
		gotato.ToolWithEffect(testkit.NewFakeTool("write", "done"), gotato.ToolEffectWriteLocal),
	}, nil
}

// downToolSet never resolves.
type downToolSet struct{}

func (downToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "crm"} }

func (downToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return nil, errors.New("crm is down")
}

func entryOf(entries []service.ToolEntry, id string) (service.ToolEntry, bool) {
	i := slices.IndexFunc(entries, func(e service.ToolEntry) bool { return e.Spec.ID == id })
	if i < 0 {
		return service.ToolEntry{}, false
	}
	return entries[i], true
}

// A Session's tools can be listed and switched by ID for the tools of a
// ToolSet too: the listing says which are loaded and which are switched off,
// the switch is the metadata fact every instance reads, and a ToolSet that is
// down is shown as unavailable.
func TestAToolOfAToolSetIsListedAndSwitchedByID(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	arguments := `{"names":["files.write"]}`
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "load-1", ToolID: gotato.SearchToolName, Arguments: []byte(arguments)}),
		testkit.Text("loaded"))
	agent := service.Agent{Name: "a", Model: model, Options: []gotato.SessionOption{gotato.WithToolSet(twoToolSet{}), gotato.WithToolSet(downToolSet{})}}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()

	// Before anything: both tools are listed, unloaded and on; crm is unavailable.
	entries, err := runner.Tools(ctx, "", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"files.read", "files.write"} {
		entry, ok := entryOf(entries, want)
		if !ok || entry.ToolSet != "files" || entry.Loaded || !entry.Active {
			t.Fatalf("%s = %+v, %v; want a files tool, unloaded and active", want, entry, ok)
		}
	}
	if entry, _ := entryOf(entries, "files.write"); entry.Spec.Effect != gotato.ToolEffectWriteLocal {
		t.Fatalf("files.write effect = %q", entry.Spec.Effect)
	}
	if crm, ok := entryOf(entries, "crm"); !ok || !crm.Unavailable {
		t.Fatalf("crm = %+v, %v; want unavailable", crm, ok)
	}
	if _, ok := entryOf(entries, gotato.SearchToolName); !ok {
		t.Fatal("search_tools is not listed")
	}

	// Switch a ToolSet tool off by its ID.
	entry, err := runner.SetToolActive(ctx, id, "files.read", false)
	if err != nil || entry.Active || entry.ToolSet != "files" {
		t.Fatalf("SetToolActive off = %+v, %v", entry, err)
	}
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := record.Get(service.MetaToolPrefix + "files.read"); v != "inactive" {
		t.Fatalf("metadata = %q, want inactive", v)
	}
	// Unknown names and names in a ToolSet that is down are refused.
	for _, bad := range []string{"files.nope", "crm.lookup"} {
		if _, err := runner.SetToolActive(ctx, id, bad, false); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Fatalf("SetToolActive(%s) = %v, want invalid argument", bad, err)
		}
	}
	if _, err := runner.SetToolActive(ctx, id, "crm.lookup", false); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("crm.lookup error = %v, want it to say unavailable", err)
	}

	// A Run loads files.write; the catalog the model read leaves files.read out.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	for _, request := range model.Requests() {
		for _, spec := range request.Tools {
			if spec.ID == gotato.SearchToolName && strings.Contains(spec.Description, "files.read") {
				t.Fatalf("the catalog lists the switched-off files.read: %s", spec.Description)
			}
		}
	}

	// Another Runner on the same store reads the same facts.
	other, err := service.New(service.Config{Store: store, Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = other.Tools(ctx, "", id)
	if err != nil {
		t.Fatal(err)
	}
	read, _ := entryOf(entries, "files.read")
	write, _ := entryOf(entries, "files.write")
	if read.Active || read.Loaded || !write.Active || !write.Loaded {
		t.Fatalf("after the Run: read = %+v, write = %+v", read, write)
	}

	// A switched-off tool can be named again to switch it on.
	entry, err = other.SetToolActive(ctx, id, "files.read", true)
	if err != nil || !entry.Active {
		t.Fatalf("SetToolActive on = %+v, %v", entry, err)
	}
	entries, _ = other.Tools(ctx, "", id)
	if read, _ := entryOf(entries, "files.read"); !read.Active {
		t.Fatalf("files.read = %+v, want active", read)
	}
}

func TestAgentMaxEffectHidesToolsAboveIt(t *testing.T) {
	ctx := context.Background()
	lookup := testkit.NewFakeTool("lookup", "found")
	refund := testkit.NewFakeTool("refund", "refunded")
	purge := testkit.NewFakeTool("purge", "purged")
	model := testkit.NewFakeModel(
		testkit.ToolCalls(
			gotato.ToolCall{ID: "c1", ToolID: "lookup", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "c2", ToolID: "refund", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "c3", ToolID: "purge", Arguments: []byte(`{}`)},
		),
		testkit.Text("done"),
	)
	runner, _ := newRunner(t, service.Agent{
		Name:        "capped",
		Model:       model,
		Tools:       []gotato.Tool{gotato.ToolWithEffect(lookup, gotato.ToolEffectRead), gotato.ToolWithEffect(refund, gotato.ToolEffectWriteExternal)},
		ToolSources: []gotato.ToolSource{toolList{gotato.ToolWithEffect(purge, gotato.ToolEffectDestructive)}},
		MaxEffect:   gotato.ToolEffectRead,
	})
	entries, err := runner.Tools(ctx, "capped", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Spec.ID != "lookup" {
		t.Fatalf("Runner.Tools = %+v, want only lookup", entries)
	}
	s, err := runner.CreateSession(ctx, "capped", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A Session cannot switch on a tool its Agent hides.
	for _, id := range []string{"refund", "purge"} {
		if _, err := runner.SetToolActive(ctx, s.ID(), id, true); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Fatalf("SetToolActive(%s) = %v, want an unknown tool", id, err)
		}
	}
	result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"})
	if err != nil || result.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	if lookup.Calls() != 1 || refund.Calls() != 0 || purge.Calls() != 0 {
		t.Fatalf("calls: lookup %d, refund %d, purge %d; want 1, 0, 0", lookup.Calls(), refund.Calls(), purge.Calls())
	}
	for _, request := range model.Requests() {
		// read_result declares read, so the cap keeps it; the tools above the cap are gone.
		if len(request.Tools) != 2 || request.Tools[0].ID != "lookup" || request.Tools[1].ID != "read_result" {
			t.Fatalf("request tools = %+v, want only lookup and read_result", request.Tools)
		}
	}
}

func TestNewRejectsAnUnknownMaxEffect(t *testing.T) {
	_, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "a", Model: testkit.EchoModel{}, MaxEffect: "mutate"}},
	})
	if err == nil {
		t.Fatal("New accepted an unknown MaxEffect")
	}
}

// A Tool ID that an Agent's Tools and one of its ToolSources both list fails
// the Agent's tool surface as a whole, as any bad Tool of a source does: a Run
// is refused with invalid_argument and so is Runner.Tools, and neither Tool
// is kept in place of the other.
func TestATwiceNamedToolFailsTheAgentsTools(t *testing.T) {
	lookup := testkit.NewFakeTool("lookup", "found")
	runner, _ := newRunner(t, service.Agent{
		Name:        "twice",
		Model:       testkit.NewFakeModel(testkit.Text("done")),
		Tools:       []gotato.Tool{lookup},
		ToolSources: []gotato.ToolSource{toolList{testkit.NewFakeTool("lookup", "other")}},
	})
	if _, err := runner.Run(context.Background(), service.RunRequest{Agent: "twice", Prompt: "go"}); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("Run err = %v, want invalid_argument", err)
	}
	if entries, err := runner.Tools(context.Background(), "twice", ""); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("Runner.Tools = %+v, %v, want invalid_argument", entries, err)
	}
}

// An unknown tool is refused before the Session is claimed: the Record is not written.
func TestSetToolActiveOnAnUnknownToolWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	runner := recreatedRunner(t, "a", store, -1)
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Log(ctx, s.ID(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.SetToolActive(ctx, s.ID(), "no.such.tool", false); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("SetToolActive = %v, want an unknown tool", err)
	}
	after, err := store.Log(ctx, s.ID(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the log grew from %d to %d entries for an unknown tool", len(before), len(after))
	}
}

// panicSpecTool is a Tool whose Spec panics.
type panicSpecTool struct{}

func (panicSpecTool) Spec() gotato.ToolSpec { panic("boom-spec") }
func (panicSpecTool) Execute(context.Context, gotato.ToolUse, gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	return gotato.ToolResult{}, nil
}

// A Tool of the Agent whose Spec panics fails the listing with a code.
func TestAToolWhoseSpecPanicsFailsTheListing(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{
		Name:  "boom",
		Model: testkit.NewFakeModel(testkit.Text("done")),
		Tools: []gotato.Tool{panicSpecTool{}},
	})
	if entries, err := runner.Tools(context.Background(), "boom", ""); !gotato.IsCode(err, gotato.CodeExtensionFailure) {
		t.Fatalf("Runner.Tools = %+v, %v, want extension_failure", entries, err)
	}
}
