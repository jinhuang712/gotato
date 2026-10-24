package gotato

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// activeNames is a Transcript that remembers the ToolSets earlier Runs
// activated, as a session.Record does.
type activeNames struct {
	memoryTranscript
	names []string
}

func (a *activeNames) ActiveToolSets() []string { return a.names }

func restoredToolIDs(t *testing.T, transcript Transcript, options ...Option) []string {
	t.Helper()
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}
	agent, err := NewSession(append([]Option{WithModel(model), WithTranscript(transcript)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	var ids []string
	for _, spec := range agent.registry.visibleSpecs() {
		ids = append(ids, spec.ID)
	}
	return ids
}

func TestRestoredToolSetsAreActiveFromTheFirstRequest(t *testing.T) {
	files := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}
	git := &stubToolSet{name: "git", tools: []Tool{&namedTool{id: "status"}}}
	ids := restoredToolIDs(t, &activeNames{names: []string{"files"}}, WithToolSet(files), WithToolSet(git))
	if !slices.Contains(ids, "files.read") || slices.Contains(ids, "git.status") || !slices.Contains(ids, ActivationToolName) {
		t.Fatalf("tools = %v", ids)
	}
	// A Transcript that remembers nothing restores nothing.
	ids = restoredToolIDs(t, &memoryTranscript{}, WithToolSet(files), WithToolSet(git))
	if slices.Contains(ids, "files.read") {
		t.Fatalf("a Transcript without activations restored a ToolSet: %v", ids)
	}
}

func TestRestoredToolSetsSkipWhatCannotBeKept(t *testing.T) {
	broken := &stubToolSet{name: "broken", err: errors.New("gone")}
	files := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}
	// An unknown name and a ToolSet that no longer resolves stay off, and the
	// Session still starts.
	ids := restoredToolIDs(t, &activeNames{names: []string{"missing", "broken", "files"}}, WithToolSet(broken), WithToolSet(files))
	if !slices.Contains(ids, "files.read") || !slices.Contains(ids, ActivationToolName) {
		t.Fatalf("tools = %v", ids)
	}
	// Past MaxActiveToolSets, the later names stay off.
	limits := defaultLimits()
	limits.MaxActiveToolSets = 1
	git := &stubToolSet{name: "git", tools: []Tool{&namedTool{id: "status"}}}
	ids = restoredToolIDs(t, &activeNames{names: []string{"files", "git"}}, WithLimits(limits), WithToolSet(files), WithToolSet(git))
	if !slices.Contains(ids, "files.read") || slices.Contains(ids, "git.status") {
		t.Fatalf("tools over the limit = %v", ids)
	}
	// A ToolSet the Agent installed active takes its place first.
	ids = restoredToolIDs(t, &activeNames{names: []string{"files"}}, WithLimits(limits), WithActiveToolSet(git), WithToolSet(files))
	if !slices.Contains(ids, "git.status") || slices.Contains(ids, "files.read") {
		t.Fatalf("tools with a configured ToolSet = %v", ids)
	}
}

func TestAConfiguredToolSetIsNeverOff(t *testing.T) {
	git := &stubToolSet{name: "git", tools: []Tool{&namedTool{id: "status"}}}
	// Whatever the Transcript remembers, a ToolSet installed active stays on.
	// Nothing switches a ToolSet off yet, so this guards the marking that a
	// later change which deactivates ToolSets will read.
	ids := restoredToolIDs(t, &activeNames{names: []string{}}, WithActiveToolSet(git))
	if !slices.Contains(ids, "git.status") {
		t.Fatalf("tools = %v", ids)
	}
}

func TestARestoredToolSetThatCollidesStaysOff(t *testing.T) {
	// A root Tool already holds the qualified ID "files.read", so activating
	// the "files" ToolSet would collide. It stays off; "git" is restored and
	// the Session starts.
	files := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}
	git := &stubToolSet{name: "git", tools: []Tool{&namedTool{id: "status"}}}
	ids := restoredToolIDs(t, &activeNames{names: []string{"files", "git"}}, WithTool(&namedTool{id: "files.read"}), WithToolSet(files), WithToolSet(git))
	count := 0
	for _, id := range ids {
		if id == "files.read" {
			count++
		}
	}
	if count != 1 || !slices.Contains(ids, "git.status") || !slices.Contains(ids, ActivationToolName) {
		t.Fatalf("tools = %v", ids)
	}
}
