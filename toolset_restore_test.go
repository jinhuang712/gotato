package gotato

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
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

// blockingToolSet resolves only when its context ends.
type blockingToolSet struct{ entered chan struct{} }

func (blockingToolSet) Spec() ToolSetSpec { return ToolSetSpec{Name: "slow"} }

func (b blockingToolSet) Tools(ctx context.Context) ([]Tool, error) {
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNewSessionContextCancelsARestoreInProgress(t *testing.T) {
	set := blockingToolSet{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewSessionContext(ctx, WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}),
			WithTranscript(&activeNames{names: []string{"slow"}}), WithToolSet(set))
		done <- err
	}()
	select {
	case <-set.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the restore never reached the ToolSet")
	}
	cancel()
	select {
	case err := <-done:
		if !IsCode(err, ErrCancelled) {
			t.Fatalf("err = %v, want cancelled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NewSessionContext did not return after the context ended")
	}
}

func TestNewSessionContextRefusesAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSessionContext(ctx, WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}})); !IsCode(err, ErrCancelled) {
		t.Fatalf("err = %v, want cancelled", err)
	}
}
