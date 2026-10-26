package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// What the bot's scripted model says (script.go), which the checks below look for.
const (
	ansMCPOK    = "mcp-ok:"
	ansMCPError = "mcp-error:"
	ansLoaded   = "loaded:"
	ansBashErr  = "bash-error:"
	exportTool  = "orders.export"
	exportCall  = "export-1"
	catalogTool = "crm.get_invoice"
)

func (m recordMessage) text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// assistantTexts is the text of every assistant message that has one, in order.
func (r record) assistantTexts() []string {
	var out []string
	for _, m := range r.Messages {
		if m.Role == "assistant" && m.text() != "" {
			out = append(out, m.text())
		}
	}
	return out
}

// hasEvent says the Record holds an event of the kind (for the tool kinds, of that tool).
func (r record) hasEvent(kind, toolID string) bool {
	for _, ev := range r.Events {
		if ev.Kind == kind && (toolID == "" || ev.ToolID == toolID || ev.Tool == toolID) {
			return true
		}
	}
	return false
}

// takeoverOf is the claim by which another Instance took the Session over from victim.
func takeoverOf(r record, victim string) (recordEvent, bool) {
	for _, ev := range r.Events {
		if ev.Kind == "session_claimed" && ev.From == victim && ev.Instance != victim {
			return ev, true
		}
	}
	return recordEvent{}, false
}

// lookupCheck is what a Transcript says about s6: the spilled export, and bash reading it.
type lookupCheck struct {
	Spilled      int      // tool results the core spilled
	SpilledBytes int      // size of the biggest
	BashOK       int      // bash results with status ok
	BashFailed   int      // bash results with another status
	Answers      int      // lookup answers: the sentence, or a bash-error
	Correct      int      // answers equal to want
	Wrong        []string // the others
	DupCalls     int      // call IDs that appear in more than one tool call or tool result
}

// checkLookup reads a Record for s6. want is the sentence a correct lookup ends with.
func checkLookup(r record, want string) lookupCheck {
	var c lookupCheck
	toolOf := map[string]string{}
	calls, results := map[string]int{}, map[string]int{}
	for _, m := range r.Messages {
		for _, tc := range m.ToolCalls {
			toolOf[tc.ID] = tc.ToolID
			calls[tc.ID]++
		}
		if m.ToolResult == nil {
			continue
		}
		results[m.ToolResult.CallID]++
		if len(m.ToolResult.Spilled) > 0 && string(m.ToolResult.Spilled) != "null" {
			c.Spilled++
		}
		if toolOf[m.ToolResult.CallID] == "bash" {
			if m.ToolResult.Status == "ok" {
				c.BashOK++
			} else {
				c.BashFailed++
			}
		}
	}
	for _, ev := range r.Events {
		if ev.Kind == "tool_result_spilled" {
			c.SpilledBytes = max(c.SpilledBytes, ev.Bytes)
		}
	}
	for id, n := range calls {
		if n > 1 || results[id] > 1 {
			c.DupCalls++
		}
	}
	for _, text := range r.assistantTexts() {
		switch {
		case strings.HasPrefix(text, ansBashErr), strings.Contains(text, "is on line"):
			c.Answers++
			if text == want {
				c.Correct++
			} else {
				c.Wrong = append(c.Wrong, text)
			}
		}
	}
	return c
}

// mcpAnswers are the model's reports on its MCP calls in a Transcript, by the tag of the user message they
// answer: ok and error texts, each in order.
type mcpAnswers struct {
	OK, Err map[string]int // tag -> count
	Texts   []string       // the error texts
}

// checkMCP walks a Transcript: an assistant report belongs to the last user message before it.
func checkMCP(r record) mcpAnswers {
	a := mcpAnswers{OK: map[string]int{}, Err: map[string]int{}}
	tag := ""
	for _, m := range r.Messages {
		switch m.Role {
		case "user":
			if i := strings.Index(m.text(), "T["); i >= 0 {
				if j := strings.Index(m.text()[i:], "]"); j > 0 {
					tag = m.text()[i : i+j+1]
				}
			}
		case "assistant":
			switch text := m.text(); {
			case strings.HasPrefix(text, ansMCPOK):
				a.OK[tag]++
			case strings.HasPrefix(text, ansMCPError):
				a.Err[tag]++
				a.Texts = append(a.Texts, text)
			}
		}
	}
	return a
}

// phaseOf is the phase of a tag such as T[s7-A-03-a]: the part after the scenario name.
func phaseOf(tag string) string {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(tag, "T["), "]"), "-")
	if len(parts) < 2 {
		return "?"
	}
	return parts[1]
}

// tally adds the counts of one Transcript to the per-phase totals.
func (a mcpAnswers) tally(ok, errs map[string]int) {
	for tag, n := range a.OK {
		ok[phaseOf(tag)] += n
	}
	for tag, n := range a.Err {
		errs[phaseOf(tag)] += n
	}
}

// loadedTools lists the catalog tools the Record says are loaded now: tool_loaded facts, less tool_unloaded.
func loadedTools(r record) []string {
	var out []string
	for _, ev := range r.Events {
		switch ev.Kind {
		case "tool_loaded":
			out = append(out, ev.Tool)
		case "tool_unloaded":
			for i, id := range out {
				if id == ev.Tool {
					out = append(out[:i], out[i+1:]...)
					break
				}
			}
		}
	}
	return out
}

// loadedAnswers are the model's reports "loaded: true|false", in order.
func loadedAnswers(r record) []bool {
	var out []bool
	for _, text := range r.assistantTexts() {
		if v, ok := strings.CutPrefix(text, ansLoaded+" "); ok {
			out = append(out, v == "true")
		}
	}
	return out
}

// sortedKeys returns the keys of a count map in order.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func phaseCounts(ok, errs map[string]int, phases []string) string {
	var parts []string
	for _, p := range phases {
		parts = append(parts, fmt.Sprintf("%s ok %d / error %d", p, ok[p], errs[p]))
	}
	return strings.Join(parts, "; ")
}

func round(d time.Duration) time.Duration { return d.Round(100 * time.Millisecond) }

var sessionIDRe = regexp.MustCompile(`session ID: \w+`)

// toolsetErrors counts the bots' "toolset unavailable" lines by cause, with Session IDs taken out so equal
// causes group.
func toolsetErrors(logs []logLine) map[string]int {
	out := map[string]int{}
	for _, l := range logs {
		if l.Msg == "toolset unavailable" {
			out[sessionIDRe.ReplaceAllString(l.KV["err"], "session ID: ...")]++
		}
	}
	return out
}

// sequence draws one thread per rune in the order they were sent: o for a tool call that succeeded, E for a
// tool error, ? for neither.
func sequence(ts []*thread, answers func(*thread) mcpAnswers) string {
	var b strings.Builder
	for _, t := range ts {
		a := answers(t)
		ok, errs := 0, 0
		for _, n := range a.OK {
			ok += n
		}
		for _, n := range a.Err {
			errs += n
		}
		switch {
		case errs > 0:
			b.WriteByte('E')
		case ok > 0:
			b.WriteByte('o')
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// errorTextsByPhase are the model's tool-error reports of a Transcript, by the phase of the message they answer.
func errorTextsByPhase(r record) map[string][]string {
	out := map[string][]string{}
	tag := ""
	for _, m := range r.Messages {
		switch m.Role {
		case "user":
			if i := strings.Index(m.text(), "T["); i >= 0 {
				if j := strings.Index(m.text()[i:], "]"); j > 0 {
					tag = m.text()[i : i+j+1]
				}
			}
		case "assistant":
			if strings.HasPrefix(m.text(), ansMCPError) {
				out[phaseOf(tag)] = append(out[phaseOf(tag)], m.text())
			}
		}
	}
	return out
}
