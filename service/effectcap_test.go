package service_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// toolList is a ToolSource with a fixed set of Tools.
type toolList []gotato.Tool

func (l toolList) Tools() []gotato.Tool { return l }

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
		Tools:       []gotato.Tool{gotato.ToolWithEffect(lookup, gotato.EffectRead), gotato.ToolWithEffect(refund, gotato.EffectWriteExternal)},
		ToolSources: []gotato.ToolSource{toolList{gotato.ToolWithEffect(purge, gotato.EffectDestructive)}},
		MaxEffect:   gotato.EffectRead,
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
		if _, err := runner.SetToolActive(ctx, s.ID(), id, true); !gotato.IsCode(err, gotato.ErrInvalidArgument) {
			t.Fatalf("SetToolActive(%s) = %v, want an unknown tool", id, err)
		}
	}
	result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"})
	if err != nil || result.Result.Status != gotato.RunCompleted {
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
