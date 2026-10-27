package main

import (
	"encoding/json"
	"testing"
)

func user(text string) recordMessage {
	return recordMessage{Role: "user", Parts: []recordPart{{Text: text}}}
}
func said(text string) recordMessage {
	return recordMessage{Role: "assistant", Parts: []recordPart{{Text: text}}}
}

func TestCheckMCPByTagAndPhase(t *testing.T) {
	r := record{Messages: []recordMessage{
		user("T[s7-A-01-a] [script:mcp=8s]"), said("mcp-error: mcp crm.get_order: connection reset"),
		user("T[s7-B-01-b] [script:mcp=2s]"), said("mcp-ok: order ord-3: status paid"),
		user("T[s7-B-01-c] [script:mcp=2s]"), said("plain text"), said("mcp-ok: order ord-5: status paid"),
	}}
	a := checkMCP(r)
	if a.Err["T[s7-A-01-a]"] != 1 || a.OK["T[s7-B-01-b]"] != 1 || a.OK["T[s7-B-01-c]"] != 1 || len(a.Texts) != 1 {
		t.Fatalf("%+v", a)
	}
	ok, errs := map[string]int{}, map[string]int{}
	a.tally(ok, errs)
	if ok["B"] != 2 || errs["A"] != 1 || errs["B"] != 0 {
		t.Fatalf("ok %v errs %v", ok, errs)
	}
	if got := phaseCounts(ok, errs, []string{"A", "B"}); got != "A ok 0 / error 1; B ok 2 / error 0" {
		t.Fatal(got)
	}
}

func TestLoadedToolsAndAnswers(t *testing.T) {
	r := record{Events: []recordEvent{
		{Kind: "tool_loaded", Tool: catalogTool}, {Kind: "tool_loaded", Tool: "crm.get_order"}, {Kind: "tool_unloaded", Tool: "crm.get_order"},
	}, Messages: []recordMessage{said("loaded: false"), said("something else"), said("loaded: true")}}
	if got := loadedTools(r); len(got) != 1 || got[0] != catalogTool {
		t.Fatalf("loaded = %v", got)
	}
	if got := loadedAnswers(r); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("answers = %v", got)
	}
	if !r.hasEvent("tool_loaded", catalogTool) || r.hasEvent("tool_loaded", "crm.nope") || !r.hasEvent("tool_unloaded", "") {
		t.Fatal("hasEvent")
	}
}

func TestTakeoverOf(t *testing.T) {
	r := record{Events: []recordEvent{
		{Kind: "session_claimed", Instance: "a"},
		{Kind: "session_claimed", Instance: "b", From: "a", Why: "writer_gone"},
	}}
	if ev, ok := takeoverOf(r, "a"); !ok || ev.Instance != "b" {
		t.Fatalf("%+v %v", ev, ok)
	}
	if _, ok := takeoverOf(r, "c"); ok {
		t.Fatal("no takeover from c")
	}
}

// A Record as Gotato's HTTP API writes it (shortened): events carry objects, strings and numbers in fields the
// driver does not read, and the decode must not fail on any of them.
func TestDecodeRecord(t *testing.T) {
	const doc = `{"id":"lark-om_1","messages":[
	  {"id":"m1","role":"user","parts":[{"kind":"text","text":"T[a]"}]},
	  {"id":"m2","role":"assistant","tool_calls":[{"id":"export-1","tool_id":"orders.export","arguments":"e30="}]},
	  {"id":"m3","role":"tool_result","parts":[{"kind":"text","text":"{}"}],"tool_result":{"call_id":"export-1","status":"ok","executed":true,"spilled":{"seq":14,"bytes":99}}}],
	 "runs":[{"run_id":"r1","status":"completed"}],
	 "events":[
	  {"kind":"message_committed","number":7,"message":{"id":"m1","role":"user"}},
	  {"kind":"tool_result_spilled","number":14,"call_id":"export-1","tool_id":"orders.export","bytes":1231620},
	  {"kind":"tool_loaded","number":15,"tool":"crm.get_invoice","why":"names"},
	  {"kind":"extension_failed","number":16,"stage":"toolset:crm","code":"tool_resolution_failure"}]}`
	var r record
	if err := json.Unmarshal([]byte(doc), &r); err != nil {
		t.Fatal(err)
	}
	if !r.hasEvent("tool_result_spilled", exportTool) {
		t.Fatalf("no spilled event: %+v", r.Events)
	}
	if got := loadedTools(r); len(got) != 1 || got[0] != catalogTool {
		t.Fatalf("loaded = %v", got)
	}
	if r.Events[3].Stage != "toolset:crm" || !r.hasEvent("tool_result_spilled", exportTool) {
		t.Fatalf("%+v", r.Events)
	}
}

func TestToolsetErrorsAndSequence(t *testing.T) {
	logs := []logLine{
		{Msg: "toolset unavailable", KV: map[string]string{"err": `list tools: sending "tools/list": failed to connect (session ID: AAA): session not found`}},
		{Msg: "toolset unavailable", KV: map[string]string{"err": `list tools: sending "tools/list": failed to connect (session ID: BBB): session not found`}},
		{Msg: "message received", KV: map[string]string{"err": "x"}},
	}
	got := toolsetErrors(logs)
	if len(got) != 1 {
		t.Fatalf("%v", got)
	}
	for _, n := range got {
		if n != 2 {
			t.Fatalf("%v", got)
		}
	}
	byThread := map[string]record{
		"c1": {Messages: []recordMessage{user("T[x]"), said("mcp-error: boom")}},
		"c2": {Messages: []recordMessage{user("T[x]"), said("mcp-ok: fine")}},
		"c3": {Messages: []recordMessage{user("T[x]"), said("hm")}},
	}
	ts := []*thread{{chat: "c1"}, {chat: "c2"}, {chat: "c3"}, {chat: "c2"}}
	if s := sequence(ts, func(t *thread) mcpAnswers { return checkMCP(byThread[t.chat]) }); s != "Eo?o" {
		t.Fatal(s)
	}
}
