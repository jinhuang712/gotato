package gotato

import (
	"context"
	"sync"
	"testing"
)

type dynamicSource struct {
	mu    sync.Mutex
	tools []Tool
}

func (s *dynamicSource) Tools() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Tool(nil), s.tools...)
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
