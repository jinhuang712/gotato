package service_test

import (
	"context"
	"slices"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A SessionTools func is given the Record of the Session that reads its tools
// and the Runner's store, so a Tool it builds sees that Session and no other.
// The Tool is listed for a Session and for the Agent alone.
func TestSessionToolsGetTheirOwnSessionsRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	var seenLog session.EntryLog
	build := func(record *session.Record, log session.EntryLog) []gotato.Tool {
		seenLog = log
		tool, err := gotato.NewFuncTool("whoami", "Says which Session it runs in.", func(context.Context, struct{}) (string, error) {
			return "session " + record.ID(), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return []gotato.Tool{gotato.ToolWithEffect(tool, gotato.EffectRead)}
	}
	model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		if results := testkit.ToolResults(request); len(results) > 0 {
			return testkit.Text(results[0]), nil
		}
		return testkit.ToolCalls(gotato.ToolCall{ID: "w-1", ToolID: "whoami", Arguments: []byte(`{}`)}), nil
	})
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{
		Name: "a", Model: model, SessionTools: []service.SessionToolsFunc{build}, MaxEffect: gotato.EffectRead,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "who"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(ctx, service.RunRequest{Prompt: "who"})
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == second.SessionID || first.FinalText != "session "+first.SessionID || second.FinalText != "session "+second.SessionID {
		t.Fatalf("%q for %s, %q for %s", first.FinalText, first.SessionID, second.FinalText, second.SessionID)
	}
	if seenLog != store {
		t.Fatal("the func did not get the Runner's store")
	}
	for _, sessionID := range []string{first.SessionID, ""} {
		entries, err := runner.Tools(ctx, "a", sessionID)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, e := range entries {
			ids = append(ids, e.Spec.ID)
		}
		if !slices.Contains(ids, "whoami") || (sessionID != "" && !slices.Contains(ids, "read_result")) {
			t.Errorf("session %q lists %v", sessionID, ids)
		}
	}
}

// A Tool above the Agent's MaxEffect that a SessionTools func builds is hidden.
func TestSessionToolsAreWithinMaxEffect(t *testing.T) {
	build := func(*session.Record, session.EntryLog) []gotato.Tool {
		return []gotato.Tool{gotato.ToolWithEffect(testkit.NewFakeTool("danger", "x"), gotato.EffectDestructive)}
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{
		Name: "a", Model: testkit.NewFakeModel(testkit.Text("hi")), SessionTools: []service.SessionToolsFunc{build}, MaxEffect: gotato.EffectRead,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := runner.Tools(context.Background(), "a", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Spec.ID == "danger" {
			t.Fatal("a destructive tool is offered under a read cap")
		}
	}
}
