package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func tagged(role, tag, text string) recordMessage {
	return recordMessage{Role: role, Parts: []recordPart{{Text: text, Metadata: map[string]string{"gotato.tag": tag}}}}
}

func TestUnloadsAndLoadCounts(t *testing.T) {
	r := record{Events: []recordEvent{
		{Kind: "tool_loaded", Tool: catalogTool},
		{Kind: "tool_unloaded", Tool: catalogTool, Why: "compaction"},
		{Kind: "tool_unloaded", Tool: "crm.get_order", Why: "gone"},
		{Kind: "tool_loaded", Tool: catalogTool},
	}}
	if got := unloadedByCompaction(r); !slices.Equal(got, []string{catalogTool}) {
		t.Fatalf("unloadedBy = %v", got)
	}
	if l, u := loadCounts(r, catalogTool); l != 2 || u != 1 {
		t.Fatalf("loadCounts = %d, %d", l, u)
	}
}

// A Recap quotes the messages it covers, so a tag in it must not count as a second copy of the message.
func TestCountSkipsTheRecap(t *testing.T) {
	r := record{Messages: []recordMessage{
		{Role: "user", Parts: []recordPart{{Text: "<segment>", Metadata: map[string]string{"gotato.tag": "segment"}}, {Text: "T[a] [script:load]"}}},
		said("load-done"),
		tagged("user", "recap", "<recap>- user: T[a] [script:load]</recap>"),
		user("T[b]"),
	}}
	if n := r.count("T[a]"); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

func TestSegmentHoldsAndRecap(t *testing.T) {
	r := record{Segments: []recordSegment{{Number: 1, Recap: "Summary", Messages: []recordMessage{
		{Role: "user", Parts: []recordPart{{Text: "T[a] hello"}}},
		tagged("user", "recap", "T[b] quoted"),
	}}}}
	if !segmentHolds(r, 1, "T[a]") || segmentHolds(r, 1, "T[b]") || segmentHolds(r, 2, "T[a]") {
		t.Fatal("segmentHolds")
	}
	if segmentRecap(r, 1) != "Summary" || segmentRecap(r, 2) != "" {
		t.Fatal("segmentRecap")
	}
}

func TestInstanceOfTagAndSegmentAnswers(t *testing.T) {
	r := record{
		Messages: []recordMessage{
			{Role: "user", RunID: "r1", Parts: []recordPart{{Text: "T[x-a] [script:readseg]"}}},
			said("segment-ok: Segment 1 [user] T[x-0]"),
			{Role: "user", RunID: "r2", Parts: []recordPart{{Text: "T[x-b] [script:readseg]"}}},
			said("segment-error: no such segment"),
		},
		Events: []recordEvent{
			{Kind: "session_claimed", Instance: "i1"}, {Kind: "run_started", RunID: "r1"},
			{Kind: "session_claimed", Instance: "i2"}, {Kind: "run_started", RunID: "r2"},
		},
	}
	if got := instanceOfTag(r, "T[x-a]"); got != "i1" {
		t.Fatalf("a on %q", got)
	}
	if got := instanceOfTag(r, "T[x-b]"); got != "i2" {
		t.Fatalf("b on %q", got)
	}
	if got := instanceOfTag(r, "T[nope]"); got != "" {
		t.Fatalf("missing tag on %q", got)
	}
	ok, bad := segmentAnswers(r)
	if len(ok) != 1 || ok["T[x-a]"] == "" || len(bad) != 1 || bad["T[x-b]"] == "" {
		t.Fatalf("ok %v bad %v", ok, bad)
	}
}

func TestLastCompactionAndInstance(t *testing.T) {
	r := record{Events: []recordEvent{
		{Kind: "session_claimed", Instance: "i1"}, {Kind: "compaction_ended", Instance: "i1", Number: 3},
		{Kind: "session_claimed", Instance: "i2"}, {Kind: "compaction_ended", Instance: "i2", Number: 9},
	}}
	if ev, ok := lastCompactionEnded(r); !ok || ev.Number != 9 {
		t.Fatalf("%+v", ev)
	}
	if _, ok := lastCompactionEnded(record{}); ok {
		t.Fatal("none")
	}
	if lastInstance(r) != "i2" {
		t.Fatal("lastInstance")
	}
	if lastRecapWriter(record{}) != "" || lastRecapWriter(record{Compactions: []recordCompaction{{Recap: "none"}, {Recap: "truncate"}}}) != "truncate" {
		t.Fatal("lastRecapWriter")
	}
}

// The Record as Gotato's HTTP API writes it after a compaction (shortened).
func TestDecodeCompactedRecord(t *testing.T) {
	const doc = `{"id":"lark-1","messages":[{"id":"segment-1-1","role":"user","parts":[{"kind":"text","text":"<segment n=\"1\">","metadata":{"gotato.tag":"segment"}},{"kind":"text","text":"T[a]"}]}],
	"segments":[{"number":1,"recap":"Summary","source":{"from_number":7},"messages":[{"id":"segment-1-1","role":"user","parts":[{"kind":"text","text":"T[a]"}]}]}],
	"compactions":[{"at":"2026-10-26T05:07:04Z","replaced_messages":4,"recap":"truncate","segment":1}],"tools":["crm.get_invoice"],
	"events":[{"kind":"tool_unloaded","number":38,"tool":"crm.get_invoice","why":"compaction"}]}`
	var r record
	if err := json.Unmarshal([]byte(doc), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Segments) != 1 || r.Compactions[0].Recap != "truncate" || r.Tools[0] != catalogTool || !segmentHolds(r, 1, "T[a]") || r.count("T[a]") != 1 {
		t.Fatalf("%+v", r)
	}
	if got := unloadedByCompaction(r); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestErrorTextsByPhase(t *testing.T) {
	r := record{Messages: []recordMessage{
		user("T[s10-A-01-a] [script:mcp=8s]"), said("mcp-error: mcp crm.get_order: rejected by transport"),
		user("T[s10-G-01-a] [script:mcp=8s]"), said("mcp-ok: order ord-1"),
		user("T[s10-G2-01-b]"), said("mcp-error: unknown tool"),
	}}
	got := errorTextsByPhase(r)
	if len(got["A"]) != 1 || len(got["G"]) != 0 || len(got["G2"]) != 1 {
		t.Fatalf("%v", got)
	}
	if countIn([]string{"x rejected by transport", "other", "rejected by transport"}, "rejected by transport") != 2 {
		t.Fatal("countIn")
	}
}
