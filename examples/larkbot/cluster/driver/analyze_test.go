package main

import "testing"

func ev(n int, kind, inst, from string, epoch int) recordEvent {
	return recordEvent{Number: n, Kind: kind, Instance: inst, From: from, Epoch: epoch}
}

func TestRecordFaults(t *testing.T) {
	ok := record{Events: []recordEvent{
		ev(1, "session_created", "", "", 0),
		ev(2, "session_claimed", "a", "", 1),
		ev(3, "run_started", "", "", 0),
		ev(4, "session_claimed", "b", "a", 2), // a takeover that names the holder it took from
		ev(5, "session_released", "b", "", 2),
	}}
	if f := recordFaults(ok); len(f) != 0 {
		t.Fatalf("clean Record flagged: %v", f)
	}
	gap := record{Events: []recordEvent{ev(1, "session_created", "", "", 0), ev(3, "run_started", "", "", 0)}}
	if f := recordFaults(gap); len(f) != 1 {
		t.Fatalf("gap not flagged: %v", f)
	}
	overlap := record{Events: []recordEvent{
		ev(1, "session_claimed", "a", "", 1),
		ev(2, "session_claimed", "b", "", 2), // b claims while a holds it, and does not say it took over from a
	}}
	if f := recordFaults(overlap); len(f) != 1 {
		t.Fatalf("overlap not flagged: %v", f)
	}
}

func TestClassifyAcceptedButMissing(t *testing.T) {
	e := &env{}
	m := &msg{sent: sent{MessageID: "om_1", Deliveries: []sentDelivery{{Status: 200}}}}
	lines := []logLine{
		{Pod: "p1", Msg: "message received", KV: map[string]string{"message_id": "om_1"}},
		{Pod: "p1", Msg: "send queued", KV: map[string]string{"message_id": "om_1"}},
	}
	if c, _ := e.classify(m, 0, lines); c != fault {
		t.Fatalf("queued but missing: %s", c)
	}
	e.victimPod = "p1"
	if c, _ := e.classify(m, 0, lines); c != gapKnown {
		t.Fatalf("queued by the killed pod: %s", c)
	}
	if c, _ := e.classify(m, 1, lines); c != "ok" {
		t.Fatalf("present once: %s", c)
	}
}

func rec(roles ...string) record {
	var r record
	for i, role := range roles {
		m := recordMessage{Role: role}
		m.Parts = append(m.Parts, recordPart{Text: "T[" + string(rune('a'+i)) + "]"})
		r.Messages = append(r.Messages, m)
	}
	return r
}

func TestUnansweredAndInversions(t *testing.T) {
	if rec("user", "assistant").unanswered() || !rec("user", "assistant", "user").unanswered() || (record{}).unanswered() {
		t.Fatal("unanswered")
	}
	r := rec("user", "assistant", "user", "user") // tags T[a] T[b] T[c] T[d]; T[b] is an assistant message
	if n := r.inversions([]string{"T[a]", "T[c]", "T[d]"}); n != 0 {
		t.Fatalf("in order: %d", n)
	}
	if n := r.inversions([]string{"T[d]", "T[c]", "T[a]", "T[zz]"}); n != 2 {
		t.Fatalf("reversed: %d", n)
	}
}

func TestStoreErrors(t *testing.T) {
	logs := []logLine{
		{Pod: "p1", Msg: "send", KV: map[string]string{"level": "ERROR", "err": "stale fence 7"}},
		{Pod: "p2", Msg: "send", KV: map[string]string{"level": "ERROR", "err": "stale epoch 3"}},
		{Pod: "p3", Msg: "send", KV: map[string]string{"level": "ERROR", "err": "busy"}},
		{Pod: "p4", Msg: "message received", KV: map[string]string{"level": "INFO", "err": "epoch"}}, // INFO is not read
	}
	if got := storeErrors(logs); len(got) != 2 {
		t.Fatalf("want 2, got %v", got)
	}
}

func TestDupStats(t *testing.T) {
	dup := &msg{tag: "T[a]", sent: sent{MessageID: "om_1", Deliveries: []sentDelivery{{Error: "timeout"}, {Error: "timeout"}, {Error: "timeout"}}}}
	once := &msg{tag: "T[b]", sent: sent{MessageID: "om_2", Deliveries: []sentDelivery{{Status: 200}}}}
	logs := []logLine{
		{Pod: "p1", Msg: "message received", KV: map[string]string{"message_id": "om_1"}},
		{Pod: "p2", Msg: "message received", KV: map[string]string{"message_id": "om_1"}},
		{Pod: "p2", Msg: "duplicate suspected", KV: map[string]string{"message_id": "om_1"}},
		{Pod: "p1", Msg: "message received", KV: map[string]string{"message_id": "om_2"}},
	}
	// a, a, b in the Transcript with three assistant replies; the thread has two messages and two cards
	r := rec("user", "assistant", "user", "assistant", "user", "assistant")
	r.Messages[2].Parts[0].Text = "T[a]"
	r.Messages[4].Parts[0].Text = "T[b]"
	r.Messages[0].Parts[0].Text = "T[a]"
	var s dupStats
	s.addThread([]*msg{dup, once}, r, 3, logs)
	if s.Messages != 2 || s.DeliveredTwice != 1 || s.ExtraDeliveries != 2 || s.Acked != 1 || s.InTranscriptTwice != 1 || s.ExtraUserCopies != 1 ||
		s.ExtraAssistant != 1 || s.ExtraCards != 1 || s.DupSuspectedCaught != 1 || s.PodsHandled[2] != 1 || s.PodsHandled[1] != 1 {
		t.Fatalf("%+v", s)
	}
}
