package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
)

// harness runs the CLI in-process against a temporary store.
type harness struct {
	t    *testing.T
	home string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, home: t.TempDir()}
}

func (h *harness) run(args ...string) (int, string, string) {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		switch key {
		case "GOTATO_HOME":
			return h.home
		case "GOTATO_GATEWAY_CONFIG":
			return filepath.Join(h.home, "missing-gateway.yaml")
		}
		return ""
	}
	code := Main(args, strings.NewReader(""), &stdout, &stderr, getenv)
	return code, stdout.String(), stderr.String()
}

func (h *harness) mustJSON(out string, into any) {
	h.t.Helper()
	if err := json.Unmarshal([]byte(out), into); err != nil {
		h.t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
}

func (h *harness) ok(args ...string) string {
	h.t.Helper()
	code, out, errOut := h.run(args...)
	if code != ExitOK {
		h.t.Fatalf("%v exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func TestDoctorJSON(t *testing.T) {
	h := newHarness(t)
	out := h.ok("doctor", "--json")
	var report doctorReport
	h.mustJSON(out, &report)
	if !report.OK || report.Version != Version || len(report.Checks) < 4 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunCreatesSessionAndPersists(t *testing.T) {
	h := newHarness(t)
	out := h.ok("run", "--json", "hello")
	var outcome runOutcome
	h.mustJSON(out, &outcome)
	if outcome.Status != gotato.RunCompleted || outcome.FinalText != "echo: hello" || outcome.SessionID == "" || outcome.Messages != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	// The session is on disk and lists.
	var list []map[string]any
	h.mustJSON(h.ok("session", "list", "--json"), &list)
	if len(list) != 1 || list[0]["id"] != outcome.SessionID {
		t.Fatalf("list = %v", list)
	}
	// A second run against the same session continues it.
	var second runOutcome
	h.mustJSON(h.ok("run", "--session", outcome.SessionID, "--json", "again"), &second)
	if second.Messages != 4 || second.RunID == outcome.RunID {
		t.Fatalf("second = %+v", second)
	}
	// session show returns the persisted document with both runs.
	var doc map[string]any
	h.mustJSON(h.ok("session", "show", outcome.SessionID, "--json"), &doc)
	if runs, _ := doc["runs"].([]any); len(runs) != 2 {
		t.Fatalf("runs = %v", doc["runs"])
	}
	if messages, _ := doc["messages"].([]any); len(messages) != 4 {
		t.Fatalf("messages = %v", len(messages))
	}
}

func TestDemoToolLoopAndEvents(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)

	var outcome runOutcome
	h.mustJSON(h.ok("run", "--session", id, "--model", "demo", "--json", "use-tool"), &outcome)
	if outcome.Metrics.ToolCalls != 1 || outcome.Metrics.Turns != 2 || outcome.FinalText != "demo response: use-tool" {
		t.Fatalf("outcome = %+v", outcome)
	}
	out := h.ok("events", "--session", id, "--jsonl")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	kinds := map[string]int{}
	for _, line := range lines {
		var event gotato.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		kinds[string(event.Kind)]++
	}
	if kinds["agent_start"] != 1 || kinds["agent_end"] != 1 || kinds["context_built"] != 2 || kinds["tool_execution_end"] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
	// --kind filters.
	out = h.ok("events", "--session", id, "--kind", "turn_end")
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 2 {
		t.Fatalf("turn_end lines = %d", got)
	}
	// session events <id> is the same data.
	if h.ok("session", "events", id) != h.ok("events", "--session", id) {
		t.Fatal("session events differs from events --session")
	}
}

func TestRunEventsJSONLStreamsThenResult(t *testing.T) {
	h := newHarness(t)
	out := h.ok("run", "--events", "jsonl", "--model", "demo", "use-tool")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 5 {
		t.Fatalf("too few lines: %d", len(lines))
	}
	var first gotato.Event
	h.mustJSON(lines[0], &first)
	if first.Kind != gotato.EventAgentStart {
		t.Fatalf("first line kind = %s", first.Kind)
	}
	var last struct {
		Kind   string     `json:"kind"`
		Result runOutcome `json:"result"`
	}
	h.mustJSON(lines[len(lines)-1], &last)
	if last.Kind != "run_result" || last.Result.Status != gotato.RunCompleted {
		t.Fatalf("last line = %+v", last)
	}
}

func TestContextInspectBuildCompact(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json", "--panel", "time"), &created)
	id := created["id"].(string)
	for _, prompt := range []string{"one", "two", "three"} {
		h.ok("run", "--session", id, "--quiet", prompt)
	}
	type report struct {
		Strategy         string `json:"strategy"`
		SourceMessages   int    `json:"source_messages"`
		SelectedMessages int    `json:"selected_messages"`
		ApproxTokens     int    `json:"approx_tokens"`
		PanelBytes       int    `json:"panel_bytes"`
		PrefixHash       string `json:"prefix_hash"`
		Request          struct {
			SystemInstructions string                   `json:"system_instructions"`
			Messages           []gotato.Message         `json:"messages"`
			Tools              []gotato.ToolSpec        `json:"tools"`
			CacheBreakpoints   []gotato.CacheBreakpoint `json:"cache_breakpoints"`
		} `json:"request"`
	}
	var first report
	h.mustJSON(h.ok("context", "inspect", id, "--json"), &first)
	if first.Strategy != "full_history" || first.SourceMessages != 6 || first.SelectedMessages != 6 || first.ApproxTokens == 0 || first.PanelBytes == 0 || len(first.PrefixHash) != 16 {
		t.Fatalf("report = %+v", first)
	}
	if len(first.Request.Tools) != 2 || len(first.Request.CacheBreakpoints) != 3 {
		t.Fatalf("request = %+v", first.Request)
	}
	// The stored history ends with an assistant answer, so the panel is
	// built (panel_bytes > 0) but not attached: it only rides on a user or
	// tool-result tail, never on an assistant message.
	for _, message := range first.Request.Messages {
		if strings.Contains(gotato.TextOf(message), "<panel>") {
			t.Fatalf("panel attached to %s message", message.Role)
		}
	}
	// The panel is time-dependent, but the prefix hash is not.
	var second report
	h.mustJSON(h.ok("context", "inspect", id, "--json"), &second)
	if second.PrefixHash != first.PrefixHash {
		t.Fatalf("prefix hash unstable: %s vs %s", first.PrefixHash, second.PrefixHash)
	}
	// During a run the new prompt is the tail and carries the panel.
	out := h.ok("run", "--session", id, "--events", "jsonl", "four")
	sawPanel := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var event gotato.Event
		if json.Unmarshal([]byte(line), &event) == nil && event.Kind == gotato.EventContextBuilt {
			if bytes, _ := event.Payload["panel_bytes"].(float64); bytes > 0 {
				sawPanel = true
			}
		}
	}
	if !sawPanel {
		t.Fatal("context_built did not report a panel during the run")
	}
	// build prints the request only.
	var built gotato.ModelRequest
	h.mustJSON(h.ok("context", "build", id, "--json"), &built)
	if len(built.Messages) != 8 || built.SystemInstructions == "" {
		t.Fatalf("built = %+v", built)
	}
	var result struct {
		Replaced       bool `json:"replaced"`
		MessagesBefore int  `json:"messages_before"`
		MessagesAfter  int  `json:"messages_after"`
		TokensBefore   int  `json:"tokens_before"`
		TokensAfter    int  `json:"tokens_after"`
	}
	h.mustJSON(h.ok("context", "compact", id, "--keep", "2", "--json"), &result)
	if !result.Replaced || result.MessagesBefore != 8 || result.MessagesAfter != 3 || result.TokensAfter >= result.TokensBefore {
		t.Fatalf("compact = %+v", result)
	}
	var doc struct {
		Messages    []gotato.Message `json:"messages"`
		Compactions []any            `json:"compactions"`
	}
	h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
	if len(doc.Messages) != 3 || len(doc.Compactions) != 1 {
		t.Fatalf("doc after compact: %d messages, %d compactions", len(doc.Messages), len(doc.Compactions))
	}
	// The compacted session keeps running; the session never stores a panel.
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--session", id, "--json", "five"), &outcome)
	if outcome.Status != gotato.RunCompleted || outcome.Messages != 5 {
		t.Fatalf("run after compact = %+v", outcome)
	}
	h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
	for _, message := range doc.Messages {
		if strings.Contains(gotato.TextOf(message), "<panel>") {
			t.Fatal("panel leaked into the session")
		}
	}
}

func TestAutoCompactViaCLI(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json", "--compact-ceiling", "60"), &created)
	id := created["id"].(string)
	var outcome runOutcome
	compacted := false
	for i := 0; i < 8 && !compacted; i++ {
		h.mustJSON(h.ok("run", "--session", id, "--json", strings.Repeat("word ", 20)), &outcome)
		compacted = outcome.Compacted
	}
	if !compacted {
		t.Fatalf("auto compaction never triggered: %+v", outcome)
	}
	var doc struct {
		Compactions []any `json:"compactions"`
	}
	h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
	if len(doc.Compactions) == 0 {
		t.Fatal("no compaction recorded")
	}
}

func TestToolsActivationIsSessionState(t *testing.T) {
	h := newHarness(t)
	var listing toolListing
	h.mustJSON(h.ok("tools", "list", "--json"), &listing)
	if len(listing.Tools) != 2 || !listing.Tools[0].Active || listing.Tools[0].ID != "demo.echo" || len(listing.Tools[0].InputSchema) == 0 {
		t.Fatalf("listing = %+v", listing)
	}
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)
	h.ok("tools", "deactivate", "time.now", "--session", id, "--json")
	h.mustJSON(h.ok("tools", "active", "--session", id, "--json"), &listing)
	if len(listing.Tools) != 1 || listing.Tools[0].ID != "demo.echo" {
		t.Fatalf("active = %+v", listing.Tools)
	}
	var view toolView
	h.mustJSON(h.ok("tools", "describe", "time.now", "--session", id, "--json"), &view)
	if view.Active {
		t.Fatalf("time.now should be inactive: %+v", view)
	}
	h.ok("tools", "activate", "time.now", "--session", id)
	h.mustJSON(h.ok("tools", "active", "--session", id, "--json"), &listing)
	if len(listing.Tools) != 2 {
		t.Fatalf("after activate = %+v", listing.Tools)
	}
	if code, _, _ := h.run("tools", "activate", "time.now"); code != ExitUsage {
		t.Fatalf("activate without --session exit = %d", code)
	}
	if code, _, _ := h.run("tools", "describe", "nope", "--json"); code != ExitNotFound {
		t.Fatalf("describe unknown exit = %d", code)
	}
}

func TestForkIsIndependent(t *testing.T) {
	h := newHarness(t)
	var first runOutcome
	h.mustJSON(h.ok("run", "--json", "root"), &first)
	var fork map[string]any
	h.mustJSON(h.ok("session", "fork", first.SessionID, "--json"), &fork)
	if fork["parent_id"] != first.SessionID || fork["id"] == first.SessionID || int(fork["messages"].(float64)) != 2 {
		t.Fatalf("fork = %v", fork)
	}
	h.ok("run", "--session", fork["id"].(string), "--quiet", "branch")
	var parent map[string]any
	h.mustJSON(h.ok("session", "show", first.SessionID, "--json"), &parent)
	if messages, _ := parent["messages"].([]any); len(messages) != 2 {
		t.Fatalf("parent mutated by fork run: %d", len(messages))
	}
}

func TestExitCodes(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		args []string
		code int
	}{
		{[]string{}, ExitUsage},
		{[]string{"bogus"}, ExitUsage},
		{[]string{"run"}, ExitUsage},
		{[]string{"run", "--events", "xml", "hi"}, ExitUsage},
		{[]string{"run", "--model", "nope", "hi"}, ExitUsage},
		{[]string{"run", "--panel", "weather", "hi"}, ExitUsage},
		{[]string{"session", "show", "missing"}, ExitNotFound},
		{[]string{"context", "inspect", "missing", "--json"}, ExitNotFound},
		{[]string{"events", "--session", "missing"}, ExitNotFound},
		{[]string{"run", "--model", "gateway", "hi"}, ExitError},
		{[]string{"help"}, ExitOK},
		{[]string{"version", "--json"}, ExitOK},
	}
	for _, tc := range cases {
		code, out, errOut := h.run(tc.args...)
		if code != tc.code {
			t.Errorf("%v exit = %d, want %d\nstdout=%s\nstderr=%s", tc.args, code, tc.code, out, errOut)
		}
	}
	// Machine mode failures also put one JSON object on stdout.
	_, out, _ := h.run("session", "show", "missing", "--json")
	var failure map[string]any
	h.mustJSON(out, &failure)
	if failure["exit_code"] != float64(ExitNotFound) {
		t.Fatalf("failure json = %v", failure)
	}
}

func TestRunIncompleteExitCode(t *testing.T) {
	h := newHarness(t)
	// A 1ns command deadline cancels the run before the model answers.
	code, out, _ := h.run("run", "--timeout", "1ns", "--json", "hello")
	if code != ExitRunIncomplete {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	var outcome runOutcome
	h.mustJSON(out, &outcome)
	if outcome.Status == gotato.RunCompleted || outcome.Error == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestStdinPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	home := t.TempDir()
	code := Main([]string{"run", "--json", "-"}, strings.NewReader("from stdin"), &stdout, &stderr, func(key string) string {
		if key == "GOTATO_HOME" {
			return home
		}
		return ""
	})
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var outcome runOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil || outcome.FinalText != "echo: from stdin" {
		t.Fatalf("outcome = %+v err=%v", outcome, err)
	}
}

// TestBinaryScenario builds the real binary and drives it through a shell-like
// sequence, so the process boundary (stdout/stderr split, exit code) is
// exercised too.
func TestBinaryScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	bin := filepath.Join(t.TempDir(), "gotato")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	runBin := func(args ...string) (int, string, string) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(cmd.Environ(), "GOTATO_HOME="+home, "GOTATO_GATEWAY_CONFIG="+filepath.Join(home, "none.yaml"))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, stdout.String(), stderr.String()
	}
	code, out, errOut := runBin("run", "--json", "--model", "demo", "use-tool")
	if code != 0 {
		t.Fatalf("run exit %d: %s", code, errOut)
	}
	if errOut != "" {
		t.Fatalf("machine mode must keep stderr empty on success, got %q", errOut)
	}
	var outcome runOutcome
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runBin("session", "show", "missing")
	if code != ExitNotFound || !strings.Contains(errOut, "not found") {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	code, out, _ = runBin("doctor", "--json")
	if code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("doctor exit=%d out=%s", code, out)
	}
}
