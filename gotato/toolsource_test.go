package gotato

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type dynamicSource struct {
	mu    sync.Mutex
	tools []Tool
	err   error
}

func (s *dynamicSource) Tools(context.Context) ([]Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return append([]Tool(nil), s.tools...), nil
}

func (s *dynamicSource) set(tools ...Tool) {
	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

func TestToolSourceRefreshesAtTurnBoundary(t *testing.T) {
	source := &dynamicSource{}
	model := &testModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
	agent, err := NewSession(context.Background(), WithModel(model), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if got := agent.Tools(); len(got) != 0 {
		t.Fatalf("tools before registration = %v", got)
	}
	if _, err := agent.Prompt(context.Background(), UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if got := len(model.lastRequest().Tools); got != 0 {
		t.Fatalf("model saw %d tools, want 0", got)
	}
	source.set(&scriptedTool{})
	if _, err := agent.Prompt(context.Background(), UserMessage("two")); err != nil {
		t.Fatal(err)
	}
	request := model.lastRequest()
	if len(request.Tools) != 1 || request.Tools[0].ID != "demo.echo" {
		t.Fatalf("model saw tools %+v", request.Tools)
	}
	if got := agent.Tools(); len(got) != 1 || got[0].ID != "demo.echo" {
		t.Fatalf("inspector = %+v", got)
	}
	source.set()
	if _, err := agent.Prompt(context.Background(), UserMessage("three")); err != nil {
		t.Fatal(err)
	}
	if got := len(model.lastRequest().Tools); got != 0 {
		t.Fatalf("model saw %d tools after removal, want 0", got)
	}
}

func TestToolSourceToolIsCallable(t *testing.T) {
	tool := &scriptedTool{}
	source := &dynamicSource{tools: []Tool{tool}}
	agent, err := NewSession(context.Background(), WithModel(&toolModel{}), WithToolSource(source))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), UserMessage("use tool"))
	if err != nil {
		t.Fatal(err)
	}
	if TextOf(*result.FinalMessage) != "final" {
		t.Fatalf("final = %+v", result.FinalMessage)
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d", tool.calls)
	}
}

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

// A ToolSource or ToolSet that supplies a nil Tool fails with a code, not a
// process panic.
func TestANilToolFromASourceOrASetFailsWithACode(t *testing.T) {
	t.Run("ToolSource", func(t *testing.T) {
		source := &dynamicSource{tools: []Tool{nil}}
		_, err := NewSession(context.Background(), WithModel(&testModel{}), WithToolSource(source))
		if err == nil {
			t.Fatal("a nil Tool from a ToolSource did not fail NewSession")
		}
	})
	t.Run("ToolSet", func(t *testing.T) {
		set := &stubToolSet{name: "files", tools: []Tool{nil}}
		model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
		session, err := NewSession(context.Background(), WithModel(model), WithToolSet(set))
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer func() { _ = session.Close(context.Background()) }()
		if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
			t.Logf("Prompt: %v", err)
		}
	})
}
