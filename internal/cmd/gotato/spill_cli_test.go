package main

import (
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// A Tool Result over one page is spilled: the Session shows the preview and
// the position of the Entry, and `events` shows tool_result_spilled with its
// size and no body.
func TestALargeToolResultIsSpilledAndShownAsAnEventWithoutItsBody(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--session", id, "--model", "demo", "--json", "use-big-tool"), &outcome)
	if outcome.Metrics.ToolCalls != 1 || outcome.Metrics.Turns != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	out := h.ok("events", "--session", id, "--kind", "tool_result_spilled")
	var event map[string]any
	h.mustJSON(strings.TrimSpace(out), &event)
	if event["kind"] != "tool_result_spilled" || event["call_id"] != "call-big" || event["tool_id"] != "demo.echo" || event["bytes"].(float64) < 32<<10 {
		t.Fatalf("event = %v", event)
	}
	if _, hasBody := event["body"]; hasBody || len(out) > 4<<10 {
		t.Fatalf("events carries the body (%d bytes)", len(out))
	}
	var doc struct {
		Messages []gotato.Message `json:"messages"`
	}
	h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
	var result *gotato.ToolResult
	for _, message := range doc.Messages {
		if message.ToolResult != nil {
			result = message.ToolResult
		}
	}
	if result == nil || result.Spilled == nil || result.Spilled.Seq == 0 || result.Spilled.Bytes < 32<<10 ||
		!strings.Contains(result.Content[0].Text, `read_result(ref="result:call-big", page=1)`) || len(result.Content[0].Text) > 9000 {
		t.Fatalf("tool result = %+v", result)
	}
}
