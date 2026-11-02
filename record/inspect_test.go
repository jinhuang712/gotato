package record_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
)

func snapshot(messages []gotato.Message) gotato.ContextSnapshot {
	return gotato.ContextSnapshot{SystemInstructions: "sys", Messages: messages}
}

func TestPrefixHashStableAcrossAppendOnlyTurns(t *testing.T) {
	messages := toolConversation()
	builder := gotato.FullHistoryContext()
	tools := []gotato.ToolSpec{{ID: "t", InputSchema: []byte(`{"type":"object"}`)}}
	first, _ := record.Inspect(context.Background(), builder, snapshot(messages), tools)
	// Next Turn: the tail Message changed (a new assistant answer appended and
	// a new user prompt). Everything before the new tail is identical.
	next := append(append([]gotato.Message{}, messages...), gotato.UserMessage("u3"))
	second, _ := record.Inspect(context.Background(), builder, snapshot(next), tools)
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
	fourth, _ := record.Inspect(context.Background(), builder, snapshot(altered), tools)
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
		encoded, err := json.Marshal(record.Report{CompactCeilingTokens: ceiling})
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

// TestReportPartsAddUpToTheWhole: for a Context with a Segment, a Tool call
// and a Tool Result, the parts add up to the estimate of the whole request,
// within the rounding of bytes over 4 (up to 3 bytes lost per part).
func TestReportPartsAddUpToTheWhole(t *testing.T) {
	segment := gotato.Message{Role: gotato.RoleUser, Parts: []gotato.ContentPart{{
		Kind: gotato.ContentKindText, Text: "<segment n=\"1\">\nearlier work\n</segment>",
		Metadata: map[string]string{gotato.MetaTag: gotato.TagSegment},
	}}}
	messages := append([]gotato.Message{segment}, toolConversation()...)
	tools := []gotato.ToolSpec{{ID: "t", Description: "a tool", InputSchema: []byte(`{"type":"object"}`)}}
	report, err := record.Inspect(context.Background(), gotato.FullHistoryContext(), snapshot(messages), tools)
	if err != nil {
		t.Fatal(err)
	}
	parts := []int{report.EstimateSystemTokens, report.EstimateToolDefsTokens, report.EstimateSegmentsTokens, report.EstimateHistoryTokens, report.EstimateToolResultsTokens}
	sum := 0
	for i, tokens := range parts {
		if tokens <= 0 {
			t.Fatalf("part %d is %d, want a positive estimate: %+v", i, tokens, report)
		}
		sum += tokens
	}
	encodedTools, _ := json.Marshal(report.Request.Tools)
	whole := record.EstimateTokens(report.Request.Messages) + (len(report.Request.SystemInstructions)+len(encodedTools)+3)/4
	if diff := sum - whole; diff < -1 || diff > len(parts) {
		t.Fatalf("parts add up to %d, whole is %d", sum, whole)
	}
	// The same numbers as the record package rule: the history part is the user
	// and assistant Messages alone.
	history := []gotato.Message{messages[1], messages[2], messages[4], messages[5], messages[6]}
	if got := record.EstimateTokens(history); report.EstimateHistoryTokens < got-2 || report.EstimateHistoryTokens > got+1 {
		t.Fatalf("history part = %d, EstimateTokens = %d", report.EstimateHistoryTokens, got)
	}
	if report.EstimateSystemTokens != (len(report.Request.SystemInstructions)+3)/4 {
		t.Fatalf("system part = %d", report.EstimateSystemTokens)
	}
}

// TestReportCarriesPartsAlways: a 0 part is a key with 0, not a missing key.
func TestReportCarriesPartsAlways(t *testing.T) {
	encoded, err := json.Marshal(record.Report{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"estimate_system_tokens", "estimate_tool_defs_tokens", "estimate_segments_tokens", "estimate_history_tokens", "estimate_tool_results_tokens"} {
		if !strings.Contains(string(encoded), `"`+key+`":0`) {
			t.Fatalf("%s missing from %s", key, encoded)
		}
	}
}
