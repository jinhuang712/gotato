package main

import (
	"slices"
	"strings"
)

// What the bot's scripted model says for s9 (script.go).
const (
	ansLoadDone = "load-done"
	ansSegOK    = "segment-ok:"
	ansSegError = "segment-error:"
)

// lastCompactionEnded is the newest compaction_ended event of the Record.
func lastCompactionEnded(r record) (recordEvent, bool) {
	for _, ev := range slices.Backward(r.Events) {
		if ev.Kind == "compaction_ended" {
			return ev, true
		}
	}
	return recordEvent{}, false
}

// unloadedByCompaction lists the tools a compaction took out (tool_unloaded with why=compaction), in order.
func unloadedByCompaction(r record) []string {
	var out []string
	for _, ev := range r.Events {
		if ev.Kind == "tool_unloaded" && ev.Why == "compaction" {
			out = append(out, ev.Tool)
		}
	}
	return out
}

// loadCounts counts the tool_loaded and tool_unloaded events of a tool.
func loadCounts(r record, tool string) (loads, unloads int) {
	for _, ev := range r.Events {
		switch {
		case ev.Kind == "tool_loaded" && ev.Tool == tool:
			loads++
		case ev.Kind == "tool_unloaded" && ev.Tool == tool:
			unloads++
		}
	}
	return loads, unloads
}

// segmentHolds says Segment n holds a user message that carries the tag, word for word.
func segmentHolds(r record, n int, tag string) bool {
	for _, s := range r.Segments {
		if s.Number != n {
			continue
		}
		for _, m := range s.Messages {
			if m.Role != "user" {
				continue
			}
			for _, p := range m.Parts {
				if p.Metadata["gotato.tag"] == "" && strings.Contains(p.Text, tag) {
					return true
				}
			}
		}
	}
	return false
}

// segmentRecap is the Recap text of Segment n, "" when it has none.
func segmentRecap(r record, n int) string {
	for _, s := range r.Segments {
		if s.Number == n {
			return s.Recap
		}
	}
	return ""
}

// instanceOfTag is the Instance that held the Session for the Run of the user message that carries the tag:
// the newest session_claimed before that Run's run_started.
func instanceOfTag(r record, tag string) string {
	runID := ""
	for _, m := range r.Messages {
		if m.Role == "user" && m.RunID != "" && strings.Contains(m.text(), tag) {
			runID = m.RunID
		}
	}
	if runID == "" {
		return ""
	}
	inst := ""
	for _, ev := range r.Events {
		switch {
		case ev.Kind == "session_claimed":
			inst = ev.Instance
		case ev.Kind == "run_started" && ev.RunID == runID:
			return inst
		}
	}
	return ""
}

// segmentAnswers are the model's reports on read_segment: "segment-ok: ..." and "segment-error: ...", by the
// tag of the user message they answer, in order.
func segmentAnswers(r record) (ok, bad map[string]string) {
	ok, bad = map[string]string{}, map[string]string{}
	tag := ""
	for _, m := range r.Messages {
		text := m.text()
		switch m.Role {
		case "user":
			if i := strings.Index(text, "T["); i >= 0 {
				if j := strings.Index(text[i:], "]"); j > 0 {
					tag = text[i : i+j+1]
				}
			}
		case "assistant":
			if v, found := strings.CutPrefix(text, ansSegOK); found {
				ok[tag] = v
			} else if v, found := strings.CutPrefix(text, ansSegError); found {
				bad[tag] = v
			}
		}
	}
	return ok, bad
}
