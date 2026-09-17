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
	h.mustJSON(h.ok("session", "create", "--json", "--context", "window:2"), &created)
	id := created["id"].(string)
	for _, prompt := range []string{"one", "two", "three"} {
		h.ok("run", "--session", id, "--quiet", prompt)
	}
	var report struct {
		Strategy         string `json:"strategy"`
		SourceMessages   int    `json:"source_messages"`
		SelectedMessages int    `json:"selected_messages"`
		DroppedMessages  int    `json:"dropped_messages"`
		ApproxTokens     int    `json:"approx_tokens"`
	}
	h.mustJSON(h.ok("context", "inspect", id, "--json"), &report)
	if report.Strategy != "window:2" || report.SourceMessages != 6 || report.SelectedMessages != 2 || report.DroppedMessages != 4 || report.ApproxTokens == 0 {
		t.Fatalf("report = %+v", report)
	}
	// An explicit strategy overrides the session setting.
	h.mustJSON(h.ok("context", "inspect", id, "--json", "--context", "full"), &report)
	if report.SelectedMessages != 6 {
		t.Fatalf("full report = %+v", report)
	}
	var built gotato.ModelContext
	h.mustJSON(h.ok("context", "build", id, "--json"), &built)
	if len(built.Messages) != 2 || gotato.TextOf(built.Messages[0]) != "three" {
		t.Fatalf("built = %+v", built.Messages)
	}
	var result struct {
		Replaced       bool `json:"replaced"`
		MessagesBefore int  `json:"messages_before"`
		MessagesAfter  int  `json:"messages_after"`
	}
	h.mustJSON(h.ok("context", "compact", id, "--keep", "2", "--json"), &result)
	if !result.Replaced || result.MessagesBefore != 6 || result.MessagesAfter != 3 {
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
	// The compacted session keeps running.
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--session", id, "--json", "four"), &outcome)
	if outcome.Status != gotato.RunCompleted || outcome.Messages != 5 {
		t.Fatalf("run after compact = %+v", outcome)
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
