package service_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
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
		Agents: []service.Agent{{Name: "a", Model: model, Options: []gotato.Option{gotato.WithToolSet(set)}}},
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
