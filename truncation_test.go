package gotato_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// truncated is a Turn that spent the whole output limit reasoning and never
// acted: what a reasoning model returns when it plans at length.
func truncated() testkit.Script {
	return testkit.Script{
		{Kind: gotato.ModelReasoningDelta, Text: "Let me think through every function first..."},
		{Kind: gotato.ModelDone, StopReason: gotato.StopMaxTokens},
	}
}

// Observed in Mow (I29): a DeepSeek Worker reasoned through all of its work
// in one Turn, hit the output limit, and returned no Tool Call; the Loop took
// the cut-off Message as the final answer and the Run completed with nothing
// done. With MaxTruncatedContinuations set, such a Turn is continued instead.
func TestTruncatedTurnContinuesWithinTheBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bound   uint32
		scripts []testkit.Script
		calls   int
		final   string
	}{
		{name: "default settles on the cut-off turn", bound: 0, scripts: []testkit.Script{truncated(), testkit.Text("done")}, calls: 1, final: ""},
		{name: "continued once", bound: 2, scripts: []testkit.Script{truncated(), testkit.Text("done")}, calls: 2, final: "done"},
		{name: "bound reached", bound: 2, scripts: []testkit.Script{truncated(), truncated(), truncated(), testkit.Text("done")}, calls: 3, final: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := testkit.NewFakeModel(tc.scripts...)
			limits := gotato.DefaultLimits()
			limits.MaxTruncatedContinuations = tc.bound
			agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithLimits(limits))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = agent.Close(context.Background()) }() // test teardown; the Run already settled
			result, err := agent.Prompt(context.Background(), gotato.UserMessage("do the work"))
			if err != nil {
				t.Fatal(err)
			}
			if model.Calls() != tc.calls {
				t.Fatalf("model calls = %d, want %d", model.Calls(), tc.calls)
			}
			if result.Status != gotato.RunCompleted || result.FinalMessage == nil || gotato.TextOf(*result.FinalMessage) != tc.final {
				t.Fatalf("result = %+v", result)
			}
			if tc.calls < 2 {
				return
			}
			// The continuation is a notice the Model sees after the cut-off
			// Turn, so the next request never ends on an assistant, and it is
			// not the user's words (G-F29).
			request, _ := model.LastRequest()
			last := request.Messages[len(request.Messages)-1]
			want := `<notice kind="continue">` + gotato.TruncatedContinuationText + `</notice>`
			if last.Role != gotato.RoleNotice || gotato.TextOf(last) != want {
				t.Fatalf("last message before the continued turn = %+v", last)
			}
		})
	}
}
