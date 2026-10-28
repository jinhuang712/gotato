package gotato_test

import (
	"context"
	"slices"
	"strings"
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

// effectLevels is every Effect a tool can declare, the empty one included.
var effectLevels = []gotato.ToolEffect{gotato.ToolEffectRead, gotato.ToolEffectWriteLocal, gotato.ToolEffectWriteExternal, gotato.ToolEffectDestructive, gotato.ToolEffectUnspecified}

func effectToolID(effect gotato.ToolEffect) string {
	if effect == gotato.ToolEffectUnspecified {
		return "unspecified"
	}
	return string(effect)
}

func specIDs(specs []gotato.ToolSpec) []string {
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	return ids
}

// toolResultsByCall maps each tool result in a request to its call.
func toolResultsByCall(request gotato.ModelRequest) map[gotato.ToolCallID]gotato.ToolResult {
	out := map[gotato.ToolCallID]gotato.ToolResult{}
	for _, msg := range request.Messages {
		if msg.ToolResult != nil {
			out[msg.ToolResult.CallID] = *msg.ToolResult
		}
	}
	return out
}

// TestEffectCapHidesToolsAboveIt is G-G05: Agents with every cap get a tool
// at every Effect level, and the model calls all of them by name. A tool above
// the cap is in no request and no listing, its code never runs, and its call
// fails as a call to a tool this Session does not allow, which does not name it as available.
func TestEffectCapHidesToolsAboveIt(t *testing.T) {
	ctx := context.Background()
	for _, ceiling := range []gotato.ToolEffect{gotato.ToolEffectRead, gotato.ToolEffectWriteLocal, gotato.ToolEffectWriteExternal, gotato.ToolEffectDestructive} {
		t.Run(string(ceiling), func(t *testing.T) {
			fakes := map[gotato.ToolEffect]*testkit.FakeTool{}
			var tools []gotato.Tool
			var calls []gotato.ToolCall
			var want []string
			for _, effect := range effectLevels {
				id := effectToolID(effect)
				fakes[effect] = testkit.NewFakeTool(id, "ran "+id)
				tools = append(tools, gotato.ToolWithEffect(fakes[effect], effect))
				calls = append(calls, gotato.ToolCall{ID: gotato.ToolCallID("call-" + id), ToolID: id, Arguments: []byte(`{}`)})
				if effect.Within(ceiling) {
					want = append(want, id)
				}
			}
			slices.Sort(want)
			model := testkit.NewFakeModel(testkit.ToolCalls(calls...), testkit.Text("done"))
			agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(tools...), gotato.WithMaxEffect(ceiling))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = agent.Close(ctx) }()
			if got := specIDs(agent.Tools()); !slices.Equal(got, want) {
				t.Fatalf("Tools() = %v, want %v", got, want)
			}
			inspection, err := agent.InspectContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := specIDs(inspection.Request.Tools); !slices.Equal(got, want) {
				t.Fatalf("inspected request tools = %v, want %v", got, want)
			}
			result, err := agent.Prompt(ctx, gotato.UserMessage("call every tool"))
			if err != nil || result.Status != gotato.RunStatusCompleted {
				t.Fatalf("Prompt = %+v, %v", result, err)
			}
			requests := model.Requests()
			if len(requests) != 2 {
				t.Fatalf("model calls = %d, want 2", len(requests))
			}
			for _, request := range requests {
				if got := specIDs(request.Tools); !slices.Equal(got, want) {
					t.Fatalf("request tools = %v, want %v", got, want)
				}
			}
			results := toolResultsByCall(requests[1])
			for _, effect := range effectLevels {
				id := effectToolID(effect)
				res := results[gotato.ToolCallID("call-"+id)]
				if effect.Within(ceiling) {
					if fakes[effect].Calls() != 1 || res.Status != gotato.ToolResultStatusOK {
						t.Errorf("%s within %s: calls %d, result %+v", id, ceiling, fakes[effect].Calls(), res)
					}
					continue
				}
				if fakes[effect].Calls() != 0 {
					t.Errorf("%s above %s ran %d times", id, ceiling, fakes[effect].Calls())
				}
				_, available, found := strings.Cut(res.SafeError, "available tools:")
				if res.Status != gotato.ToolResultStatusFailed || res.Executed || !strings.Contains(res.SafeError, "not allowed in this Session") || !found {
					t.Errorf("%s above %s: result %+v, want a failed not-allowed result", id, ceiling, res)
				}
				if strings.Contains(available, id) {
					t.Errorf("%s above %s: the result lists it as available: %q", id, ceiling, res.SafeError)
				}
			}
		})
	}
}

// opsToolSet is a ToolSet whose tools the test chooses.
type opsToolSet struct{ tools []gotato.Tool }

func (s opsToolSet) Spec() gotato.ToolSetSpec                     { return gotato.ToolSetSpec{Name: "ops"} }
func (s opsToolSet) Tools(context.Context) ([]gotato.Tool, error) { return s.tools, nil }

func TestEffectCapKeepsAToolOfAToolSetOutOfTheIndex(t *testing.T) {
	ctx := context.Background()
	lookup := testkit.NewFakeTool("lookup", "found")
	push := testkit.NewFakeTool("push", "pushed")
	set := opsToolSet{tools: []gotato.Tool{gotato.ToolWithEffect(lookup, gotato.ToolEffectRead), gotato.ToolWithEffect(push, gotato.ToolEffectWriteExternal)}}
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "a", ToolID: gotato.SearchToolName, Arguments: []byte(`{"names":["ops.lookup","ops.push"],"queries":["push"]}`)}),
		testkit.ToolCalls(gotato.ToolCall{ID: "b", ToolID: "ops.lookup", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "c", ToolID: "ops.push", Arguments: []byte(`{}`)}),
		testkit.Text("done"),
	)
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithToolSet(set), gotato.WithMaxEffect(gotato.ToolEffectRead))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(ctx) }()
	if _, err := agent.Prompt(ctx, gotato.UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) != 3 {
		t.Fatalf("model calls = %d, want 3", len(requests))
	}
	// The search tool declares read, so even the lowest cap keeps it.
	if got := specIDs(requests[0].Tools); !slices.Equal(got, []string{gotato.SearchToolName}) {
		t.Fatalf("first request tools = %v, want only the search tool", got)
	}
	catalog := requests[0].Tools[0].Description
	if !strings.Contains(catalog, "ops.lookup") || strings.Contains(catalog, "ops.push") {
		t.Fatalf("the catalog = %q, want ops.lookup and not ops.push", catalog)
	}
	// The cap comes first: the tool is not found by name or by query, and is not loaded.
	found := toolResultsByCall(requests[1])["a"]
	text := gotato.TextOf(gotato.Message{Parts: found.Content})
	if !strings.Contains(text, "Loaded: ops.lookup.") || !strings.Contains(text, "Not found: ops.push.") || !strings.Contains(text, "No tool matches: push") {
		t.Fatalf("search result = %q", text)
	}
	if got := specIDs(requests[1].Tools); !slices.Equal(got, []string{"ops.lookup", gotato.SearchToolName}) {
		t.Fatalf("tools after the load = %v, want [ops.lookup search_tools]", got)
	}
	if lookup.Calls() != 1 || push.Calls() != 0 {
		t.Fatalf("calls: lookup %d, push %d; want 1, 0", lookup.Calls(), push.Calls())
	}
	if res := toolResultsByCall(requests[2])["c"]; res.Status != gotato.ToolResultStatusFailed || !strings.Contains(res.SafeError, "not allowed in this Session") {
		t.Fatalf("hidden ToolSet tool result = %+v", res)
	}
}

func TestNoEffectCapOffersEveryTool(t *testing.T) {
	var tools []gotato.Tool
	var want []string
	for _, effect := range effectLevels {
		tools = append(tools, gotato.ToolWithEffect(testkit.NewFakeTool(effectToolID(effect), "ok"), effect))
		want = append(want, effectToolID(effect))
	}
	slices.Sort(want)
	for _, extra := range [][]gotato.SessionOption{nil, {gotato.WithMaxEffect(gotato.ToolEffectUnspecified)}} {
		options := append([]gotato.SessionOption{gotato.WithModel(testkit.NewFakeModel()), gotato.WithTools(tools...)}, extra...)
		agent, err := gotato.NewSession(context.Background(), options...)
		if err != nil {
			t.Fatal(err)
		}
		if got := specIDs(agent.Tools()); !slices.Equal(got, want) {
			t.Errorf("Tools() = %v, want %v", got, want)
		}
		_ = agent.Close(context.Background())
	}
}

func TestWithMaxEffectRejectsAnUnknownEffect(t *testing.T) {
	_, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.NewFakeModel()), gotato.WithMaxEffect("mutate"))
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("WithMaxEffect(mutate) = %v, want CodeInvalidArgument", err)
	}
}

// A call to a tool the Effect cap hides says that the tool exists but this
// Session does not allow it, and keeps the tool_resolution_failure code. A
// name that no tool carries stays an unknown tool.
func TestEffectCapRefusalNamesTheCap(t *testing.T) {
	ctx := context.Background()
	danger := testkit.NewFakeTool("delete_file", "deleted")
	model := testkit.NewFakeModel(
		testkit.ToolCalls(
			gotato.ToolCall{ID: "capped", ToolID: "delete_file", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "typo", ToolID: "delete_fiel", Arguments: []byte(`{}`)},
		),
		testkit.Text("done"),
	)
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model),
		gotato.WithTools(gotato.ToolWithEffect(danger, gotato.ToolEffectDestructive)),
		gotato.WithMaxEffect(gotato.ToolEffectRead))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(ctx) }()
	if result, err := agent.Prompt(ctx, gotato.UserMessage("go")); err != nil || result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Prompt = %+v, %v", result, err)
	}
	results := toolResultsByCall(model.Requests()[1])
	capped, typo := results["capped"], results["typo"]
	if capped.Status != gotato.ToolResultStatusFailed || capped.Executed || danger.Calls() != 0 {
		t.Fatalf("capped result = %+v, calls %d", capped, danger.Calls())
	}
	if !strings.Contains(capped.SafeError, string(gotato.CodeToolResolutionFailure)) || !strings.Contains(capped.SafeError, "not allowed in this Session") || strings.Contains(capped.SafeError, "unknown tool") {
		t.Errorf("capped SafeError = %q, want the code and that the tool is not allowed here", capped.SafeError)
	}
	if !strings.Contains(typo.SafeError, "unknown tool") {
		t.Errorf("typo SafeError = %q, want an unknown tool", typo.SafeError)
	}
}
