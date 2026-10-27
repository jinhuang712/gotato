package service_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

type filesToolSet struct{}

func (filesToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "files"} }

func (filesToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{testkit.NewFakeTool("read", "contents")}, nil
}

func loadFiles() testkit.Script {
	arguments, _ := json.Marshal(map[string][]string{"names": {"files.read"}})
	return testkit.ToolCalls(gotato.ToolCall{ID: "load-1", ToolID: gotato.SearchToolName, Arguments: arguments})
}

func toolIDsOf(request gotato.ModelRequest) []string {
	ids := make([]string, 0, len(request.Tools))
	for _, spec := range request.Tools {
		ids = append(ids, spec.ID)
	}
	return ids
}

func toolSetRunner(t *testing.T, store session.Store, model gotato.Model) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "a", Model: model, Options: []gotato.Option{gotato.WithToolSet(filesToolSet{})}}},
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
	if _, err := other.Compact(ctx, first.SessionID, modelctx.CompactOptions{Keep: 2}); err != nil {
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
	// Nothing in the ToolSet may load, so the Agent offers no search tool.
	if got := toolIDsOf(requests[0]); slices.Contains(got, "files.read") || slices.Contains(got, gotato.SearchToolName) {
		t.Fatalf("tools = %v, want neither files.read nor search_tools", got)
	}
	record, err := session.View(ctx, runner.Store(), result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.LoadedTools(); len(got) != 0 {
		t.Fatalf("LoadedTools = %v", got)
	}
}
