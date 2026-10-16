package gotato_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// effectLevels is every Effect a tool can declare, the empty one included.
var effectLevels = []gotato.ToolEffect{gotato.EffectRead, gotato.EffectWriteLocal, gotato.EffectWriteExternal, gotato.EffectDestructive, gotato.EffectUnspecified}

func effectToolID(effect gotato.ToolEffect) string {
	if effect == gotato.EffectUnspecified {
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
// fails as a call to an unknown tool that does not name it as available.
func TestEffectCapHidesToolsAboveIt(t *testing.T) {
	ctx := context.Background()
	for _, ceiling := range []gotato.ToolEffect{gotato.EffectRead, gotato.EffectWriteLocal, gotato.EffectWriteExternal, gotato.EffectDestructive} {
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
			agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTools(tools...), gotato.WithMaxEffect(ceiling))
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
			if err != nil || result.Status != gotato.RunCompleted {
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
					if fakes[effect].Calls() != 1 || res.Status != gotato.ToolResultOK {
						t.Errorf("%s within %s: calls %d, result %+v", id, ceiling, fakes[effect].Calls(), res)
					}
					continue
				}
				if fakes[effect].Calls() != 0 {
					t.Errorf("%s above %s ran %d times", id, ceiling, fakes[effect].Calls())
				}
				_, available, found := strings.Cut(res.SafeError, "available tools:")
				if res.Status != gotato.ToolResultFailed || res.Executed || !strings.Contains(res.SafeError, "unknown tool") || !found {
					t.Errorf("%s above %s: result %+v, want a failed unknown-tool result", id, ceiling, res)
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

func TestEffectCapHidesToolsOfAnActivatedToolSet(t *testing.T) {
	ctx := context.Background()
	lookup := testkit.NewFakeTool("lookup", "found")
	push := testkit.NewFakeTool("push", "pushed")
	set := opsToolSet{tools: []gotato.Tool{gotato.ToolWithEffect(lookup, gotato.EffectRead), gotato.ToolWithEffect(push, gotato.EffectWriteExternal)}}
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "a", ToolID: gotato.ActivationToolName, Arguments: []byte(`{"name":"ops"}`)}),
		testkit.ToolCalls(gotato.ToolCall{ID: "b", ToolID: "ops.lookup", Arguments: []byte(`{}`)},
			gotato.ToolCall{ID: "c", ToolID: "ops.push", Arguments: []byte(`{}`)}),
		testkit.Text("done"),
	)
	agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithToolSet(set), gotato.WithMaxEffect(gotato.EffectRead))
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
	// The activation tool declares read, so even the lowest cap keeps it.
	if got := specIDs(requests[0].Tools); !slices.Equal(got, []string{gotato.ActivationToolName}) {
		t.Fatalf("first request tools = %v, want only the activation tool", got)
	}
	if got := specIDs(requests[1].Tools); !slices.Equal(got, []string{"ops.lookup"}) {
		t.Fatalf("tools after activation = %v, want [ops.lookup]", got)
	}
	if lookup.Calls() != 1 || push.Calls() != 0 {
		t.Fatalf("calls: lookup %d, push %d; want 1, 0", lookup.Calls(), push.Calls())
	}
	if res := toolResultsByCall(requests[2])["c"]; res.Status != gotato.ToolResultFailed || !strings.Contains(res.SafeError, "unknown tool") {
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
	for _, extra := range [][]gotato.Option{nil, {gotato.WithMaxEffect(gotato.EffectUnspecified)}} {
		options := append([]gotato.Option{gotato.WithModel(testkit.NewFakeModel()), gotato.WithTools(tools...)}, extra...)
		agent, err := gotato.NewAgent(options...)
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
	_, err := gotato.NewAgent(gotato.WithModel(testkit.NewFakeModel()), gotato.WithMaxEffect("mutate"))
	if !gotato.IsCode(err, gotato.ErrInvalidArgument) {
		t.Fatalf("WithMaxEffect(mutate) = %v, want ErrInvalidArgument", err)
	}
}
