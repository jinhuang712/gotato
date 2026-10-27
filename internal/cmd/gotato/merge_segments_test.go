package main

import (
	"strings"
	"testing"
)

// Once a Session has a Segment its tool list holds read_segment, and when the
// Segments pass the default budget (32K tokens, as no model window is known),
// the compaction that closed the last one merges the oldest and says so in
// `merge`; the Context then opens with the merged Segment.
func TestContextCompactMergesTheOldestSegmentsPastTheDefaultBudget(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--model", "echo", "--json"), &created)
	id := created["id"].(string)

	type inspected struct {
		Request struct {
			Tools    []struct{ ID string } `json:"tools"`
			Messages []struct {
				Parts []struct{ Text string } `json:"parts"`
			} `json:"messages"`
		} `json:"request"`
	}
	toolIDs := func() []string {
		var report inspected
		h.mustJSON(h.ok("context", "inspect", id, "--json"), &report)
		var ids []string
		for _, tool := range report.Request.Tools {
			ids = append(ids, tool.ID)
		}
		return ids
	}
	for _, tool := range toolIDs() {
		if tool == "read_segment" {
			t.Fatal("read_segment is offered before any Segment")
		}
	}

	type answer struct {
		Replaced bool `json:"replaced"`
		Merge    *struct {
			Merged       bool   `json:"merged"`
			Reason       string `json:"reason"`
			Segment      int    `json:"segment"`
			Replaced     []int  `json:"replaced"`
			Recap        string `json:"recap"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"merge"`
	}
	// Each Run is a question of about 2K tokens and the echo of it; the default
	// tail keeps the latest Run, so each compaction closes the Run before.
	long := strings.Repeat("a long question about the batch ", 250)
	var merged *answer
	mergedAt := 0
	for run := 1; run <= 10; run++ {
		h.ok("run", "--session", id, "--model", "echo", "--quiet", long)
		if run == 1 {
			continue
		}
		var got answer
		h.mustJSON(h.ok("context", "compact", id, "--model", "echo", "--json"), &got)
		if !got.Replaced {
			t.Fatalf("compaction %d closed nothing: %+v", run-1, got)
		}
		if got.Merge != nil && merged == nil {
			merged, mergedAt = &got, run-1
		}
		if run == 2 {
			if got.Merge != nil {
				t.Fatalf("the first Segment was merged: %+v", got.Merge)
			}
			found := false
			for _, tool := range toolIDs() {
				found = found || tool == "read_segment"
			}
			if !found {
				t.Fatal("read_segment is not offered once the Session has a Segment")
			}
		}
	}
	// Eight Segments of about 4.1K tokens pass 32K; the merge takes the oldest
	// down to half the budget, and the ninth Segment then fits.
	if merged == nil || mergedAt != 8 || !merged.Merge.Merged || merged.Merge.Segment != 9 || len(merged.Merge.Replaced) < 2 || merged.Merge.Replaced[0] != 1 ||
		merged.Merge.Recap != "digest" || merged.Merge.BudgetTokens != 32000 {
		t.Fatalf("the eighth compaction did not merge the oldest: %+v at %d", merged, mergedAt)
	}
	var report inspected
	h.mustJSON(h.ok("context", "inspect", id, "--json"), &report)
	if first := report.Request.Messages[0].Parts[0].Text; !strings.HasPrefix(first, `<segment n="1-`) {
		t.Fatalf("the Context opens with %q, want the merged Segment", first)
	}
	// The originals stay in the Record: the compactions are listed, merge last.
	var doc struct {
		Compactions []struct {
			Segment int   `json:"segment"`
			Merged  []int `json:"merged"`
		} `json:"compactions"`
	}
	h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
	if n := len(doc.Compactions); n != 10 || doc.Compactions[8].Segment != 9 || len(doc.Compactions[8].Merged) < 2 || doc.Compactions[9].Segment != 10 {
		t.Fatalf("compactions = %+v, want eight closings, the merge, and one more closing", doc.Compactions)
	}
}
