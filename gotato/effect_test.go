package gotato_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestToolEffectOrdering(t *testing.T) {
	order := []gotato.ToolEffect{gotato.ToolEffectRead, gotato.ToolEffectWriteLocal, gotato.ToolEffectWriteExternal, gotato.ToolEffectDestructive}
	for i, e := range order {
		if e.Rank() != i+1 {
			t.Errorf("%s.Rank() = %d, want %d", e, e.Rank(), i+1)
		}
		if !e.Valid() {
			t.Errorf("%s is not Valid", e)
		}
	}
	if gotato.ToolEffectUnspecified.Rank() != gotato.ToolEffectDestructive.Rank() {
		t.Error("an unspecified effect must rank as destructive")
	}
	if gotato.ToolEffectUnspecified.Within(gotato.ToolEffectWriteExternal) {
		t.Error("an unspecified effect passed a write_external ceiling")
	}
	if !gotato.ToolEffectRead.Within(gotato.ToolEffectWriteLocal) || gotato.ToolEffectWriteExternal.Within(gotato.ToolEffectWriteLocal) {
		t.Error("Within does not follow the ordering")
	}
	if gotato.ToolEffect("mutate").Valid() {
		t.Error("an unknown effect is Valid")
	}
}

func TestToolWithEffectSetsSpecAndKeepsBehavior(t *testing.T) {
	inner := testkit.NewFakeTool("fs.read", "contents")
	tool := gotato.ToolWithEffect(inner, gotato.ToolEffectRead)
	if got := tool.Spec(); got.Effect != gotato.ToolEffectRead || got.ID != "fs.read" {
		t.Fatalf("Spec = %+v", got)
	}
	res, err := tool.Execute(context.Background(), gotato.ToolUse{}, nil)
	if err != nil || inner.Calls() != 1 || gotato.TextOf(gotato.Message{Parts: res.Content}) != "contents" {
		t.Fatalf("Execute = %+v, %v, calls %d", res, err, inner.Calls())
	}
}

func TestUnknownEffectIsRejected(t *testing.T) {
	tool := gotato.ToolWithEffect(testkit.NewFakeTool("x", "ok"), gotato.ToolEffect("mutate"))
	_, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.NewFakeModel()), gotato.WithTools(tool))
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("NewSession with an unknown effect = %v, want CodeInvalidArgument", err)
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
	policy := &ceiling{max: gotato.ToolEffectWriteLocal}
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model),
		gotato.WithTools(gotato.ToolWithEffect(read, gotato.ToolEffectRead), gotato.ToolWithEffect(push, gotato.ToolEffectWriteExternal)),
		gotato.WithExtension(policy))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(ctx) }()
	if _, err := agent.Prompt(ctx, gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	if len(policy.seen) != 2 || policy.seen[0] != gotato.ToolEffectRead || policy.seen[1] != gotato.ToolEffectWriteExternal {
		t.Fatalf("effects seen by PreToolUse = %v", policy.seen)
	}
	if read.Calls() != 1 || push.Calls() != 0 {
		t.Fatalf("calls: read %d, push %d; want 1, 0", read.Calls(), push.Calls())
	}
}
