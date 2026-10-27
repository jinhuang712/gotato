package service_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// roundModel stands for a gateway Client of a recognized Claude or OpenAI
// provider, which is sent only the current Round's reasoning.
type roundModel struct{ *testkit.FakeModel }

func (roundModel) RoundReasoningOnly() bool { return true }

func thinkingReply(text string) testkit.Script {
	return testkit.Script{
		{Kind: gotato.ModelEventKindReasoningDelta, Text: "plan"},
		{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: []byte(`{"type":"thinking","signature":"sig"}`)},
		{Kind: gotato.ModelEventKindTextDelta, Text: text},
		{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn},
	}
}

func storedReasoning(t *testing.T, store session.Store, id string) int {
	t.Helper()
	s, err := session.Open(context.Background(), store, id)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range s.Messages() {
		for _, part := range message.Parts {
			if part.Kind == gotato.ContentKindReasoning {
				count++
			}
		}
	}
	return count
}

// The Runner asks the Agent's Model whether its provider is sent only the
// current Round's reasoning, and if so compaction, on demand and automatic,
// leaves the reasoning out of the Messages it keeps. Any other Model keeps it.
func TestCompactionLeavesOutReasoningForAModelSentOnlyTheCurrentRound(t *testing.T) {
	ctx := context.Background()
	for _, round := range []bool{false, true} {
		model := func() gotato.Model {
			fake := testkit.NewFakeModel(thinkingReply("a"))
			if round {
				return roundModel{fake}
			}
			return fake
		}

		runner, store := newRunner(t, service.Agent{Name: "a", Model: model()})
		first, err := runner.Run(ctx, service.RunRequest{Agent: "a", Prompt: "u1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "u2"}); err != nil {
			t.Fatal(err)
		}
		result, err := runner.Compact(ctx, first.SessionID, modelctx.CompactConfig{Keep: 2})
		if err != nil || !result.Replaced {
			t.Fatalf("round=%v: compact = %+v, %v", round, result, err)
		}
		want := 1
		if round {
			want = 0
		}
		if got := storedReasoning(t, store, first.SessionID); got != want {
			t.Errorf("round=%v: Compact kept %d reasoning Parts, want %d", round, got, want)
		}

		runner, store = newRunner(t, service.Agent{Name: "a", Model: model(), Compact: modelctx.CompactPolicy{Floor: 70}})
		first, err = runner.Run(ctx, service.RunRequest{Agent: "a", Prompt: "u1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "u2"}); err != nil {
			t.Fatal(err)
		}
		third, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "u3",
			Update: map[string]string{service.MetaCompactCeiling: "1"}})
		// A ceiling of one token can never be met, so the Run ends after its
		// compaction; the compaction is what this checks.
		if !gotato.IsCode(err, gotato.CodeLimitExceeded) || !third.Compacted {
			t.Fatalf("round=%v: the third Run did not compact first: %+v, %v", round, third, err)
		}
		// The kept tail is u2 and its reply.
		want = 1
		if round {
			want = 0
		}
		if got := storedReasoning(t, store, first.SessionID); got != want {
			t.Errorf("round=%v: AutoCompact kept %d reasoning Parts, want %d", round, got, want)
		}
	}
}
