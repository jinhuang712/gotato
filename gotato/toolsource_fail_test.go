package gotato

import (
	"context"
	"errors"
	"testing"
)

// A ToolSource that fails between Turns leaves the last set in place and
// stores an ExtensionFailed Event; the Run goes on, as for a failing ToolSet.
func TestFailingToolSourceKeepsTheLastSet(t *testing.T) {
	source := &dynamicSource{tools: []Tool{&scriptedTool{}}}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	session, err := NewSession(context.Background(), WithModel(model), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	events := recordAll(t, session)
	source.mu.Lock()
	source.err = errors.New("registry unreachable")
	source.mu.Unlock()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatalf("the Run failed with its ToolSource: %v", err)
	}
	if got := model.lastRequest().Tools; len(got) != 1 || got[0].ID != "demo.echo" {
		t.Fatalf("tools offered = %+v, want the last set", got)
	}
	var failed bool
	for _, event := range events.events() {
		if e, ok := event.(ExtensionFailed); ok && e.Stage == "toolsource" && e.Code == CodeToolResolutionFailure {
			failed = true
		}
	}
	if !failed {
		t.Fatal("no extension_failed event for the ToolSource")
	}
}

// NewSession has no earlier set to keep: a ToolSource that fails there fails it.
func TestToolSourceFailureFailsNewSession(t *testing.T) {
	source := &dynamicSource{err: errors.New("down")}
	_, err := NewSession(context.Background(), WithModel(&testModel{}), WithToolSource(source))
	if !IsCode(err, CodeToolResolutionFailure) {
		t.Fatalf("err = %v, want tool_resolution_failure", err)
	}
}
