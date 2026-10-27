package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/testkit"
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
	if outcome.Status != gotato.RunStatusCompleted || outcome.FinalText != "echo: hello" || outcome.SessionID == "" || outcome.Messages != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.RecordEpoch == "" {
		t.Fatalf("the run result carries no record_epoch: %s", out)
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
		event, err := gotato.UnmarshalEvent([]byte(line))
		if err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		kinds[string(event.Kind())]++
	}
	if kinds["run_started"] != 1 || kinds["run_ended"] != 1 || kinds["context_built"] != 2 || kinds["tool_ended"] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
	// --kind filters.
	out = h.ok("events", "--session", id, "--kind", "turn_ended")
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 2 {
		t.Fatalf("turn_ended lines = %d", got)
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
	// A Run's stream holds the Session's own stored Events only: the ones that
	// made a new Session, then the Run's. The Instance Events (session_loaded,
	// session_saved) are not in it.
	for i, line := range lines[:len(lines)-1] {
		event, err := gotato.UnmarshalEvent([]byte(line))
		if err != nil {
			t.Fatalf("line %d %q: %v", i, line, err)
		}
		if i == 0 && event.Kind() != gotato.EventKindSessionCreated {
			t.Fatalf("first line kind = %s, want session_created", event.Kind())
		}
		if event.Kind().Class() == gotato.EventClassInstance {
			t.Fatalf("line %d is the Instance Event %s", i, event.Kind())
		}
	}
	var last struct {
		Kind   string     `json:"kind"`
		Result runOutcome `json:"result"`
	}
	h.mustJSON(lines[len(lines)-1], &last)
	if last.Kind != "run_result" || last.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("last line = %+v", last)
	}
}

func TestContextInspectBuildCompact(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)
	for _, prompt := range []string{"one", "two", "three"} {
		h.ok("run", "--session", id, "--quiet", prompt)
	}
	type report struct {
		Strategy         string `json:"strategy"`
		SourceMessages   int    `json:"source_messages"`
		SelectedMessages int    `json:"selected_messages"`
		ApproxTokens     int    `json:"approx_tokens"`
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
	if first.Strategy != "full_history" || first.SourceMessages != 6 || first.SelectedMessages != 6 || first.ApproxTokens == 0 || len(first.PrefixHash) != 16 {
		t.Fatalf("report = %+v", first)
	}
	// The two tools of the Agent, and read_result, which every Agent with a tool gets.
	if len(first.Request.Tools) != 3 || len(first.Request.CacheBreakpoints) != 3 {
		t.Fatalf("request = %+v", first.Request)
	}
	var second report
	h.mustJSON(h.ok("context", "inspect", id, "--json"), &second)
	if second.PrefixHash != first.PrefixHash {
		t.Fatalf("prefix hash unstable: %s vs %s", first.PrefixHash, second.PrefixHash)
	}
	// A Turn's request is its history and nothing else.
	out := h.ok("run", "--session", id, "--events", "jsonl", "four")
	sawBuild := false
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if event, err := gotato.UnmarshalEvent([]byte(line)); err == nil && event.Kind() == gotato.EventKindContextBuilt {
			sawBuild = true
			if strings.Contains(line, "panel_bytes") {
				t.Fatalf("context_built still reports panel_bytes: %s", line)
			}
		}
	}
	if !sawBuild {
		t.Fatal("the run emitted no context_built")
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
		Compaction     struct {
			Segment int    `json:"segment"`
			Recap   string `json:"recap"`
		} `json:"compaction"`
	}
	// Three Runs close into Segment 1 (six Messages), two stay.
	h.mustJSON(h.ok("context", "compact", id, "--keep-messages", "2", "--json"), &result)
	if !result.Replaced || result.MessagesBefore != 8 || result.MessagesAfter != 8 || result.Compaction.Segment != 1 || result.Compaction.Recap != "none" {
		t.Fatalf("compact = %+v", result)
	}
	var doc struct {
		Messages    []gotato.Message `json:"messages"`
		Compactions []any            `json:"compactions"`
	}
	h.mustJSON(h.ok("session", "show", id, "--json"), &doc)
	if len(doc.Messages) != 8 || len(doc.Compactions) != 1 {
		t.Fatalf("doc after compact: %d messages, %d compactions", len(doc.Messages), len(doc.Compactions))
	}
	// The compacted session keeps running.
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--session", id, "--json", "five"), &outcome)
	if outcome.Status != gotato.RunStatusCompleted || outcome.Messages != 10 {
		t.Fatalf("run after compact = %+v", outcome)
	}
}

func TestAutoCompactViaCLI(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json", "--compact-ceiling", "20000"), &created)
	id := created["id"].(string)
	var outcome runOutcome
	compacted := false
	for i := 0; i < 8 && !compacted; i++ {
		// A prompt past 2K tokens is cut in a Segment, so a compaction shrinks it.
		h.mustJSON(h.ok("run", "--session", id, "--json", strings.Repeat("word ", 4000)), &outcome)
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
	if listing.Agent != "echo" {
		t.Fatalf("tools list agent = %q, want echo", listing.Agent)
	}
	if len(listing.Tools) != 2 {
		t.Fatalf("tools list = %+v", listing)
	}
	byID := map[string]toolView{}
	for _, view := range listing.Tools {
		byID[view.ID] = view
	}
	echo, ok := byID["demo.echo"]
	if !ok || !echo.Active || len(echo.InputSchema) == 0 {
		t.Fatalf("demo.echo = %+v (found=%v)", echo, ok)
	}
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)
	h.ok("tools", "deactivate", "time.now", "--session", id, "--json")
	h.mustJSON(h.ok("tools", "active", "--session", id, "--json"), &listing)
	if len(listing.Tools) != 2 || listing.Tools[0].ID != "demo.echo" || listing.Tools[1].ID != "read_result" {
		t.Fatalf("active = %+v", listing.Tools)
	}
	var view toolView
	h.mustJSON(h.ok("tools", "describe", "time.now", "--session", id, "--json"), &view)
	if view.Active {
		t.Fatalf("time.now should be inactive: %+v", view)
	}
	h.ok("tools", "activate", "time.now", "--session", id)
	h.mustJSON(h.ok("tools", "active", "--session", id, "--json"), &listing)
	if len(listing.Tools) != 3 {
		t.Fatalf("after activate = %+v", listing.Tools)
	}
	if code, _, _ := h.run("tools", "activate", "time.now"); code != ExitUsage {
		t.Fatalf("activate without --session exit = %d", code)
	}
	if code, _, _ := h.run("tools", "describe", "nope", "--json"); code != ExitNotFound {
		t.Fatalf("describe unknown exit = %d", code)
	}
}

func TestToolsListReportsTheEffectCap(t *testing.T) {
	h := newHarness(t)
	lookup := gotato.ToolWithEffect(testkit.NewFakeTool("lookup", "found"), gotato.ToolEffectRead)
	push := testkit.NewFakeTool("push", "pushed") // declares no Effect, so it ranks as destructive
	testAgents = []service.Agent{{Name: "reader", Model: testkit.EchoModel{}, Tools: []gotato.Tool{lookup, push}, MaxEffect: gotato.ToolEffectRead}}
	defer func() { testAgents = nil }()

	var listing toolListing
	h.mustJSON(h.ok("tools", "list", "--model", "reader", "--json"), &listing)
	if listing.MaxEffect != "read" || len(listing.Tools) != 1 || listing.Tools[0].ID != "lookup" {
		t.Fatalf("tools list = %+v, want max_effect read and only lookup", listing)
	}
	if code, _, _ := h.run("tools", "describe", "push", "--model", "reader", "--json"); code != ExitNotFound {
		t.Fatalf("describe a hidden tool exit = %d, want %d", code, ExitNotFound)
	}
	if out := h.ok("tools", "list", "--model", "reader"); !strings.HasPrefix(out, "max effect: read\n") {
		t.Fatalf("human tools list = %q", out)
	}
	// An Agent without a cap reports none.
	var open toolListing
	h.mustJSON(h.ok("tools", "list", "--json"), &open)
	if open.MaxEffect != "" {
		t.Fatalf("uncapped tools list max_effect = %q", open.MaxEffect)
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
		{[]string{"run", "--panel", "time", "hi"}, ExitUsage}, // the flag is gone
		{[]string{"session", "show", "missing"}, ExitNotFound},
		{[]string{"context", "inspect", "missing", "--json"}, ExitNotFound},
		{[]string{"events", "--session", "missing"}, ExitNotFound},
		{[]string{"run", "--model", "gateway", "hi"}, ExitError},
		{[]string{"help"}, ExitOK},
		{[]string{"version", "--json"}, ExitOK},
		{[]string{"version", "--bogus"}, ExitUsage},
		{[]string{"session", "--help"}, ExitOK},
		{[]string{"context", "--help"}, ExitOK},
		{[]string{"tools", "--help"}, ExitOK},
		{[]string{"session", "--json", "list"}, ExitOK},
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

// A new ErrorCode must be given an exit code on purpose: this fails until
// exitOfCode lists it.
func TestEveryErrorCodeHasAnExplicitExitCode(t *testing.T) {
	for _, code := range gotato.ErrorCodes() {
		if _, ok := exitOfCode[code]; !ok {
			t.Errorf("error code %q has no exit code", code)
		}
	}
	if len(exitOfCode) != len(gotato.ErrorCodes()) {
		t.Errorf("exitOfCode has %d entries, gotato.ErrorCodes lists %d", len(exitOfCode), len(gotato.ErrorCodes()))
	}
}

// The error object of a failed command says what the runtime says: the code,
// whether the command did not run, and whether running it again may succeed.
func TestFailureJSONCarriesCodeNotRunAndRetryable(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"session", "show", "missing", "--json"}, ExitNotFound, "not_found"},
		{[]string{"run", "--session", "missing", "--json", "hi"}, ExitNotFound, "not_found"},
		{[]string{"run", "--model", "nope", "--json", "hi"}, ExitUsage, "invalid_argument"},
		{[]string{"bogus", "--json"}, ExitUsage, "invalid_argument"},
	} {
		code, out, _ := h.run(tc.args...)
		var failure struct {
			ExitCode  int    `json:"exit_code"`
			Code      string `json:"code"`
			NotRun    *bool  `json:"not_run"`
			Retryable *bool  `json:"retryable"`
		}
		h.mustJSON(out, &failure)
		if code != tc.exit || failure.ExitCode != tc.exit || failure.Code != tc.code || failure.NotRun == nil || failure.Retryable == nil {
			t.Errorf("%v: exit %d, json %s; want exit %d, code %q, not_run and retryable set", tc.args, code, out, tc.exit, tc.code)
		}
		if tc.code == "not_found" && (!*failure.NotRun || *failure.Retryable) {
			t.Errorf("%v: not_found must be not run and not retryable: %s", tc.args, out)
		}
	}
}

func TestVersionAndEmptyEventsJSON(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("version", "--json")
	if code != ExitOK {
		t.Fatalf("version exit = %d", code)
	}
	var version map[string]any
	h.mustJSON(out, &version)
	if version["version"] != Version {
		t.Fatalf("version json = %v", version)
	}

	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	code, out, _ = h.run("events", "--session", created["id"].(string), "--json")
	if code != ExitOK {
		t.Fatalf("events exit = %d", code)
	}
	// A Session nobody ran holds the Events that made it, and no Run's.
	var recorded []struct {
		Kind string `json:"kind"`
	}
	h.mustJSON(out, &recorded)
	if len(recorded) == 0 || recorded[0].Kind != "session_created" {
		t.Fatalf("events json = %q, want a Record that opens with session_created", out)
	}
	for _, event := range recorded {
		if strings.HasPrefix(event.Kind, "run_") {
			t.Fatalf("a Session nobody ran holds %s: %q", event.Kind, out)
		}
	}
}

func TestRunIncompleteExitCode(t *testing.T) {
	h := newHarness(t)
	// A model that blocks until the run deadline cancels it makes the
	// deadline_exceeded outcome deterministic: a fast model can otherwise
	// finish before the wall-clock timer fires.
	block := make(chan struct{})
	defer close(block)
	model := testkit.NewFakeModel(testkit.Text("unreachable"))
	model.Block = block
	testAgents = []service.Agent{{Name: "blocking", Model: model, ModelName: "blocking", Instruction: defaultInstruction}}
	defer func() { testAgents = nil }()

	code, out, _ := h.run("run", "--model", "blocking", "--timeout", "20ms", "--json", "hello")
	if code != ExitRunIncomplete {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	var outcome runOutcome
	h.mustJSON(out, &outcome)
	if outcome.Status != gotato.RunStatusDeadlineExceeded || outcome.Error == nil || outcome.SessionID == "" {
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

// TestUsageErrorsHonorMachineFlags covers --json appearing after the command
// token or a bad subcommand flag, where ordinary flag parsing never reaches it.
func TestUsageErrorsHonorMachineFlags(t *testing.T) {
	h := newHarness(t)
	cases := [][]string{
		{"bogus", "--json"},
		{"run", "--bogus", "--json"},
		{"session", "bogus", "--json"},
	}
	for _, args := range cases {
		code, out, _ := h.run(args...)
		if code != ExitUsage {
			t.Errorf("%v exit = %d, want %d", args, code, ExitUsage)
			continue
		}
		var failure map[string]any
		h.mustJSON(out, &failure)
		if failure["exit_code"] != float64(ExitUsage) {
			t.Errorf("%v failure = %v", args, failure)
		}
	}
}

// TestMachineFlagScanIgnoresValues guards against treating a flag value that
// happens to equal "json"/"jsonl" as the machine-output flag.
func TestMachineFlagScanIgnoresValues(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("run", "--model", "json", "hi")
	if code != ExitUsage {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("stdout should be empty without a real --json flag, got %q", out)
	}
}

func TestSessionDelete(t *testing.T) {
	h := newHarness(t)
	var created map[string]any
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	id := created["id"].(string)
	var result map[string]any
	h.mustJSON(h.ok("session", "delete", id, "--json"), &result)
	if result["id"] != id || result["deleted"] != true {
		t.Fatalf("delete = %v", result)
	}
	if code, _, _ := h.run("session", "show", id, "--json"); code != ExitNotFound {
		t.Fatalf("show after delete exit = %d", code)
	}
}

// TestCommandsLeaveNoLivenessRow: a command removes the Liveness row of the
// Runner it built when it ends, so the store does not keep a row for every
// command that ever ran.
func TestCommandsLeaveNoLivenessRow(t *testing.T) {
	h := newHarness(t)
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--json", "hello"), &outcome)
	h.ok("run", "--json", "--session", outcome.SessionID, "again")
	h.ok("session", "create", "--json")
	rows, err := filepath.Glob(filepath.Join(h.home, "sessions", ".instances", "*.json"))
	if err != nil || len(rows) != 0 {
		t.Fatalf("liveness rows left behind = %v, %v", rows, err)
	}
}

// TestServeHonorsTimeout verifies --timeout bounds the serve lifetime; without
// it the command would block until a signal.
func TestServeHonorsTimeout(t *testing.T) {
	h := newHarness(t)
	done := make(chan int, 1)
	go func() {
		code, _, _ := h.run("serve", "--addr", "127.0.0.1:0", "--timeout", "30ms", "--drain-timeout", "1ms", "--jsonl")
		done <- code
	}()
	select {
	case code := <-done:
		if code != ExitOK {
			t.Fatalf("serve exit = %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not honor --timeout")
	}
}

// TestServeTakesTheWarmSessionFlags verifies the flags of the warm Session
// parse: a TTL (a negative one keeps no Session in memory) and the switch that
// turns the idle compaction off. A bad duration is a usage error.
func TestServeTakesTheWarmSessionFlags(t *testing.T) {
	h := newHarness(t)
	code, _, errOut := h.run("serve", "--addr", "127.0.0.1:0", "--timeout", "30ms", "--drain-timeout", "1ms", "--warm-ttl", "-1s", "--no-idle-compaction", "--jsonl")
	if code != ExitOK {
		t.Fatalf("serve exit = %d: %s", code, errOut)
	}
	if code, _, _ := h.run("serve", "--warm-ttl", "soon"); code != ExitUsage {
		t.Fatalf("serve --warm-ttl soon exit = %d, want %d", code, ExitUsage)
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

// Settings given to a run change the session as the run's first write, and
// --no-save keeps them to that run only.
func TestRunSettingsAreSavedUnlessNoSave(t *testing.T) {
	h := newHarness(t)
	var created runOutcome
	h.mustJSON(h.ok("run", "--json", "hello"), &created)
	h.ok("run", "--session", created.SessionID, "--instruction", "kept", "--json", "again")
	h.ok("run", "--session", created.SessionID, "--instruction", "dropped", "--no-save", "--json", "once more")
	var doc struct {
		Metadata map[string]string `json:"metadata"`
		Messages []any             `json:"messages"`
	}
	h.mustJSON(h.ok("session", "show", created.SessionID, "--json"), &doc)
	if doc.Metadata[service.MetaInstruction] != "kept" {
		t.Fatalf("instruction = %q, want the saved run's", doc.Metadata[service.MetaInstruction])
	}
	if len(doc.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: the --no-save run left nothing", len(doc.Messages))
	}
}
