package gotato

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// flakySet is a ToolSet whose answer the test changes between Runs.
type flakySet struct {
	name  string
	mu    sync.Mutex
	tools []Tool
	err   error
}

func (s *flakySet) Spec() ToolSetSpec {
	return ToolSetSpec{Name: s.name, Description: s.name + " tools"}
}

func (s *flakySet) Tools(context.Context) ([]Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return slices.Clone(s.tools), nil
}

func (s *flakySet) set(tools []Tool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools, s.err = tools, err
}

func runEvents(t *testing.T, agent *Session, text string) []Event {
	t.Helper()
	sub, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if _, err := agent.Prompt(context.Background(), UserMessage(text)); err != nil {
		t.Fatal(err)
	}
	var events []Event
	for {
		event, err := sub.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.Kind() == EventKindRunEnded {
			return events
		}
	}
}

func unavailableStages(events []Event) []string {
	var stages []string
	for _, event := range events {
		if e, ok := event.(ExtensionFailed); ok && strings.HasPrefix(e.Stage, "toolset:") {
			stages = append(stages, e.Stage)
		}
	}
	return stages
}

func goneTools(events []Event) []string {
	var ids []string
	for _, event := range events {
		if e, ok := event.(ToolUnloaded); ok && e.Why == ToolUnloadedWhyGone {
			ids = append(ids, e.ToolID)
		}
	}
	return ids
}

func TestAToolSetThatFailsIsSkippedAndTheOthersWork(t *testing.T) {
	orders, _ := ordersSet()
	down := &flakySet{name: "mail", err: errors.New("mail server is down")}
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s1", map[string]any{"names": []string{"orders.lookup"}}),
		callTool("c1", "orders.lookup"),
		finalScript("done"),
	}}
	agent := newSearchSession(t, model, WithToolSet(orders), WithToolSet(down))
	events := runEvents(t, agent, "go")
	if got := unavailableStages(events); !slices.Equal(got, []string{"toolset:mail"}) {
		t.Fatalf("warnings = %v", got)
	}
	description := descriptionOf(model, 0, SearchToolName)
	if !strings.Contains(description, "mail — mail tools (unavailable now; no tools)") {
		t.Fatalf("the catalog does not say mail is unavailable:\n%s", description)
	}
	if !strings.Contains(description, "orders.lookup") {
		t.Fatalf("the other ToolSet is missing:\n%s", description)
	}
	if text := textOfResult(resultOf(t, model, 2, "c1")); text != "ran lookup" {
		t.Fatalf("the working ToolSet did not run: %q", text)
	}
}

func TestAToolAddedOrRemovedBetweenRunsIsSeenAtTheNextRun(t *testing.T) {
	set := &flakySet{name: "files", tools: []Tool{&docTool{id: "read", description: "read a file"}}}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("a"), finalScript("b"), finalScript("c")}}
	agent := newSearchSession(t, model, WithToolSet(set))
	runEvents(t, agent, "one")
	set.set([]Tool{&docTool{id: "read", description: "read a file"}, &docTool{id: "write", description: "write a file"}}, nil)
	runEvents(t, agent, "two")
	set.set([]Tool{&docTool{id: "write", description: "write a file"}}, nil)
	runEvents(t, agent, "three")
	first, second, third := descriptionOf(model, 0, SearchToolName), descriptionOf(model, 1, SearchToolName), descriptionOf(model, 2, SearchToolName)
	if !strings.Contains(first, "files.read") || strings.Contains(first, "files.write") {
		t.Fatalf("first catalog:\n%s", first)
	}
	if !strings.Contains(second, "files.read, files.write") {
		t.Fatalf("a tool added between Runs is not in the next catalog:\n%s", second)
	}
	if strings.Contains(third, "files.read") || !strings.Contains(third, "files.write") {
		t.Fatalf("a tool removed between Runs is still in the catalog:\n%s", third)
	}
}

func TestAnUnchangedCatalogIsByteForByteTheSame(t *testing.T) {
	set, _ := ordersSet()
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("a"), finalScript("b"), finalScript("c")}}
	agent := newSearchSession(t, model, WithToolSet(set))
	for _, text := range []string{"one", "two", "three"} {
		runEvents(t, agent, text)
	}
	want := descriptionOf(model, 0, SearchToolName)
	if want == "" {
		t.Fatal("no catalog")
	}
	for i := 1; i < 3; i++ {
		if got := descriptionOf(model, i, SearchToolName); got != want {
			t.Fatalf("Run %d changed the catalog:\n%s\n---\n%s", i, want, got)
		}
	}
}

func TestALoadedToolThatVanishedIsUnloadedAtTheNextRun(t *testing.T) {
	set := &flakySet{name: "files", tools: []Tool{&docTool{id: "read", description: "read a file"}, &docTool{id: "write", description: "write a file"}}}
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s1", map[string]any{"names": []string{"files.read", "files.write"}}),
		finalScript("loaded"),
		finalScript("again"),
	}}
	agent := newSearchSession(t, model, WithToolSet(set))
	runEvents(t, agent, "one")
	set.set([]Tool{&docTool{id: "write", description: "write a file"}}, nil)
	events := runEvents(t, agent, "two")
	if got := goneTools(events); !slices.Equal(got, []string{"files.read"}) {
		t.Fatalf("unloaded gone = %v", got)
	}
	if ids := model.toolIDs(2); !slices.Equal(ids, []string{"files.write", SearchToolName}) {
		t.Fatalf("tools offered after the unload = %v", ids)
	}
}

func TestARecoveredToolSetComesBackWithItsLoadedTools(t *testing.T) {
	set := &flakySet{name: "files", tools: []Tool{&docTool{id: "read", description: "read a file"}}}
	model := &recordingModel{scripts: [][]ModelEvent{
		searchCall("s1", map[string]any{"names": []string{"files.read"}}),
		finalScript("loaded"),
		finalScript("down"),
		finalScript("up"),
	}}
	other := &describedSet{name: "mail", tools: []Tool{&docTool{id: "send", description: "send mail"}}}
	agent := newSearchSession(t, model, WithToolSet(set), WithToolSet(other))
	runEvents(t, agent, "one")
	set.set(nil, errors.New("down"))
	down := runEvents(t, agent, "two")
	if got := unavailableStages(down); !slices.Equal(got, []string{"toolset:files"}) {
		t.Fatalf("warnings = %v", got)
	}
	if got := goneTools(down); len(got) != 0 {
		t.Fatalf("an unavailable ToolSet unloaded %v; it only hides its tools", got)
	}
	if ids := model.toolIDs(2); slices.Contains(ids, "files.read") {
		t.Fatalf("an unavailable ToolSet's tool is offered: %v", ids)
	}
	set.set([]Tool{&docTool{id: "read", description: "read a file"}}, nil)
	up := runEvents(t, agent, "three")
	if got := unavailableStages(up); len(got) != 0 {
		t.Fatalf("warnings after recovery = %v", got)
	}
	if ids := model.toolIDs(3); !slices.Contains(ids, "files.read") {
		t.Fatalf("the recovered ToolSet's loaded tool is not offered again: %v", ids)
	}
}

func TestToolSetsAreNotResolvedTwiceBeforeTheFirstRun(t *testing.T) {
	set := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("a"), finalScript("b")}}
	agent := newSearchSession(t, model, WithToolSet(set))
	runEvents(t, agent, "one")
	if set.calls != 1 {
		t.Fatalf("resolutions before the first Run's request = %d, want 1", set.calls)
	}
	runEvents(t, agent, "two")
	if set.calls != 2 {
		t.Fatalf("resolutions after the second Run = %d, want 2", set.calls)
	}
}
