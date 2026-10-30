package modelctx_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
)

func snapshot(messages []gotato.Message) gotato.ContextSnapshot {
	return gotato.ContextSnapshot{SystemInstructions: "sys", Messages: messages}
}

func TestPrefixHashStableAcrossAppendOnlyTurns(t *testing.T) {
	messages := toolConversation()
	builder := gotato.FullHistoryContext()
	tools := []gotato.ToolSpec{{ID: "t", InputSchema: []byte(`{"type":"object"}`)}}
	first, _ := modelctx.Inspect(context.Background(), builder, snapshot(messages), tools)
	// Next Turn: the tail Message changed (a new assistant answer appended and
	// a new user prompt). Everything before the new tail is identical.
	next := append(append([]gotato.Message{}, messages...), gotato.UserMessage("u3"))
	second, _ := modelctx.Inspect(context.Background(), builder, snapshot(next), tools)
	if first.PrefixHash == second.PrefixHash {
		t.Fatal("prefix must change when a message is appended before the tail")
	}
	// Runtime fields never reach the prompt: IDs and usage differ, hash equal.
	altered := make([]gotato.Message, len(next))
	for i, message := range next {
		altered[i] = message.Clone()
		altered[i].ID = gotato.MessageID("other-" + string(rune('a'+i)))
		altered[i].Usage = gotato.Usage{TotalTokens: uint64(i)}
	}
	fourth, _ := modelctx.Inspect(context.Background(), builder, snapshot(altered), tools)
	if fourth.PrefixHash != second.PrefixHash {
		t.Fatal("runtime fields perturbed the prompt bytes")
	}
	wantBreakpoints := []gotato.CacheBreakpoint{{After: gotato.CacheAnchorAfterSystem}, {After: gotato.CacheAnchorAfterTools}, {After: gotato.CacheAnchorAfterMessage, Index: len(next) - 1}}
	if len(second.Request.CacheBreakpoints) != 3 {
		t.Fatalf("breakpoints = %+v", second.Request.CacheBreakpoints)
	}
	for i, want := range wantBreakpoints {
		if second.Request.CacheBreakpoints[i] != want {
			t.Fatalf("breakpoint %d = %+v, want %+v", i, second.Request.CacheBreakpoints[i], want)
		}
	}
}

// TestReportAlwaysCarriesTheCeiling: an unknown ceiling is 0 in JSON, not a
// missing key, so a consumer can tell "off" from "not reported".
func TestReportAlwaysCarriesTheCeiling(t *testing.T) {
	for _, ceiling := range []int{0, 9000} {
		encoded, err := json.Marshal(modelctx.Report{CompactCeilingTokens: ceiling})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		got, ok := fields["compact_ceiling_tokens"]
		if !ok || got != float64(ceiling) {
			t.Fatalf("ceiling %d: compact_ceiling_tokens = %v, present %v", ceiling, got, ok)
		}
	}
}
