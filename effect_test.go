package gotato_test

import (
	"context"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestToolEffectOrdering(t *testing.T) {
	order := []gotato.ToolEffect{gotato.EffectRead, gotato.EffectWriteLocal, gotato.EffectWriteExternal, gotato.EffectDestructive}
	for i, e := range order {
		if e.Rank() != i+1 {
			t.Errorf("%s.Rank() = %d, want %d", e, e.Rank(), i+1)
		}
		if !e.Valid() {
			t.Errorf("%s is not Valid", e)
		}
	}
	if gotato.EffectUnspecified.Rank() != gotato.EffectDestructive.Rank() {
		t.Error("an unspecified effect must rank as destructive")
	}
	if gotato.EffectUnspecified.Within(gotato.EffectWriteExternal) {
		t.Error("an unspecified effect passed a write_external ceiling")
	}
	if !gotato.EffectRead.Within(gotato.EffectWriteLocal) || gotato.EffectWriteExternal.Within(gotato.EffectWriteLocal) {
		t.Error("Within does not follow the ordering")
	}
	if gotato.ToolEffect("mutate").Valid() {
		t.Error("an unknown effect is Valid")
	}
}

func TestToolWithEffectSetsSpecAndKeepsBehavior(t *testing.T) {
	inner := testkit.NewFakeTool("fs.read", "contents")
	tool := gotato.ToolWithEffect(inner, gotato.EffectRead)
	if got := tool.Spec(); got.Effect != gotato.EffectRead || got.ID != "fs.read" {
		t.Fatalf("Spec = %+v", got)
	}
	res, err := tool.Execute(context.Background(), gotato.ToolUse{}, nil)
	if err != nil || inner.Calls() != 1 || gotato.TextOf(gotato.Message{Parts: res.Content}) != "contents" {
		t.Fatalf("Execute = %+v, %v, calls %d", res, err, inner.Calls())
	}
}

func TestUnknownEffectIsRejected(t *testing.T) {
	tool := gotato.ToolWithEffect(testkit.NewFakeTool("x", "ok"), gotato.ToolEffect("mutate"))
	_, err := gotato.NewAgent(gotato.WithModel(testkit.NewFakeModel()), gotato.WithTool(tool))
	if !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("NewAgent with an unknown effect = %v, want ErrInvalidArgument", err)
	}
}

// ceiling blocks every call above its effect.
type ceiling struct {
	max  gotato.ToolEffect
	seen []gotato.ToolEffect
}

func (c *ceiling) Before(_ context.Context, use gotato.ToolUse) (gotato.PreToolDecision, error) {
	c.seen = append(c.seen, use.Effect)
	if !use.Effect.Within(c.max) {
		return gotato.PreToolDecision{Block: true, Reason: "above the effect ceiling"}, nil
	}
	return gotato.PreToolDecision{}, nil
}

func TestPreToolUseSeesEffect(t *testing.T) {
	ctx := context.Background()
	read := testkit.NewFakeTool("read", "ok")
	push := testkit.NewFakeTool("push", "pushed")
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "read", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "c2", ToolID: "push", Arguments: []byte(`{}`)}),
		testkit.Text("done"),
	)
	policy := &ceiling{max: gotato.EffectWriteLocal}
	agent, err := gotato.NewAgent(gotato.WithModel(model),
		gotato.WithTools(gotato.ToolWithEffect(read, gotato.EffectRead), gotato.ToolWithEffect(push, gotato.EffectWriteExternal)),
		gotato.WithExtension(policy))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(ctx) }()
	if _, err := agent.Prompt(ctx, gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if len(policy.seen) != 2 || policy.seen[0] != gotato.EffectRead || policy.seen[1] != gotato.EffectWriteExternal {
		t.Fatalf("effects seen by PreToolUse = %v", policy.seen)
	}
	if read.Calls() != 1 || push.Calls() != 0 {
		t.Fatalf("calls: read %d, push %d; want 1, 0", read.Calls(), push.Calls())
	}
}
