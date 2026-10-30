package service_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/testkit"
)

// Update {"gotato.instruction": ...} sets the instruction of a Run: it replaces
// Agent.Instruction (it does not append), SkipSave makes it last for that Run
// alone, and an empty value falls back to Agent.Instruction.
func TestRunUpdateInstructionReplacesTheAgentsForOneRun(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("1"), testkit.Text("2"), testkit.Text("3"), testkit.Text("4"), testkit.Text("5"))
	runner, _ := newRunner(t, service.Agent{Name: "bot", Model: model, Instruction: "agent instruction"})
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "bot", nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(update map[string]string, skipSave bool) string {
		t.Helper()
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Update: update, SkipSave: skipSave}); err != nil {
			t.Fatal(err)
		}
		request, _ := model.LastRequest()
		return request.SystemInstructions
	}

	// A one-Run instruction replaces the Agent's; nothing is appended.
	if got := run(map[string]string{service.MetaInstruction: "answer in French"}, true); got != "answer in French" {
		t.Fatalf("instruction with Update and SkipSave = %q, want it to replace the Agent's", got)
	}
	// SkipSave did not store it: the next Run is back on the Agent's.
	if got := run(nil, false); got != "agent instruction" {
		t.Fatalf("instruction after a SkipSave Run = %q, want the Agent's", got)
	}
	// Without SkipSave it is stored and lasts.
	if got := run(map[string]string{service.MetaInstruction: "be brief"}, false); got != "be brief" {
		t.Fatalf("instruction with Update = %q", got)
	}
	if got := run(nil, false); got != "be brief" {
		t.Fatalf("stored instruction on the next Run = %q, want it kept", got)
	}
	// An empty value falls back to the Agent's.
	if got := run(map[string]string{service.MetaInstruction: ""}, false); got != "agent instruction" {
		t.Fatalf("instruction with an empty value = %q, want the Agent's", got)
	}
}
