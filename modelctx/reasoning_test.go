package modelctx_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

func thinking(label string) gotato.ContentPart {
	return gotato.ContentPart{Kind: gotato.ContentKindReasoning, Text: "think-" + label, Signature: []byte(`{"type":"thinking","signature":"sig-` + label + `"}`)}
}

// reasoningSession holds two Rounds whose replies all carry reasoning. A
// compaction that keeps the last four Messages keeps the second Round.
func reasoningSession(t *testing.T) *session.Record {
	t.Helper()
	call := gotato.Message{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{thinking("r2")},
		ToolCalls: []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}, StopReason: gotato.StopReasonToolCalls}
	result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusOK},
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "done"}}}
	answer := func(label, text string) gotato.Message {
		return gotato.Message{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{thinking(label), {Kind: gotato.ContentKindText, Text: text}}, StopReason: gotato.StopReasonEndTurn}
	}
	s := session.New()
	for _, message := range []gotato.Message{
		gotato.UserMessage("u1"), answer("r1", "a1"),
		gotato.UserMessage("u2"), call, result, answer("r3", "a2"),
	} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func reasoningParts(messages []gotato.Message) int {
	count := 0
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Kind == gotato.ContentKindReasoning {
				count++
			}
		}
	}
	return count
}

// The summary replaces the prefix the kept replies' reasoning was written
// under. A provider that checks that prefix (Claude) may refuse the
// reasoning, and one that is sent only the current Round's reasoning (Claude,
// OpenAI) never needs it again, so the Checkpoint leaves it out. Without
// DropReasoning, as for DeepSeek, which needs every reasoning artifact back,
// it stays.
func TestCompactLeavesTheReasoningOutOfTheKeptMessagesWhenAsked(t *testing.T) {
	ctx := context.Background()
	for _, drop := range []bool{false, true} {
		store := session.NewMemoryStore()
		s := reasoningSession(t)
		writer, err := session.CreateWriter(ctx, store, s, "test")
		if err != nil {
			t.Fatal(err)
		}
		result, err := modelctx.Compact(ctx, writer.Record(), modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{DropReasoning: drop}, KeepMessages: 4})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Release(ctx); err != nil {
			t.Fatal(err)
		}
		if !result.Replaced {
			t.Fatalf("drop=%v: nothing was compacted", drop)
		}
		kept := s.Tail()
		want := 2
		if drop {
			want = 0
		}
		if got := reasoningParts(kept); got != want {
			t.Errorf("drop=%v: the kept Messages hold %d reasoning Parts, want %d", drop, got, want)
		}
		last := kept[len(kept)-1]
		if gotato.TextOf(last) != "a2" || len(kept) != 4 || len(kept[1].ToolCalls) != 1 {
			t.Errorf("drop=%v: compaction changed more than the reasoning: %+v", drop, kept)
		}
		reloaded, err := session.Open(ctx, store, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		if got := reasoningParts(reloaded.Messages()); got != want {
			t.Errorf("drop=%v: after a reload the kept Messages hold %d reasoning Parts, want %d", drop, got, want)
		}
	}
}

func TestAutoCompactLeavesTheReasoningOutWhenItsPolicySays(t *testing.T) {
	s := reasoningSession(t)
	auto := modelctx.AutoCompact(s, modelctx.CompactPolicy{CeilingTokens: 1, DropReasoning: true})
	// A ceiling of one token is never met again, so the Run would end; the
	// compaction itself is what this checks.
	prepared, err := auto.PrepareTurn(context.Background(), gotato.PendingTurn{RunID: "r", Turn: 2})
	if !gotato.IsCode(err, gotato.CodeLimitExceeded) || !prepared.Changed {
		t.Fatalf("prepared = %+v, err = %v", prepared, err)
	}
	if _, runs := auto.Last(); runs != 1 {
		t.Fatalf("auto compactions = %d, want 1", runs)
	}
	if got := reasoningParts(s.Tail()); got != 0 {
		t.Fatalf("the kept Messages hold %d reasoning Parts, want 0", got)
	}
}
