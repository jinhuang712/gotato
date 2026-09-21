package main

import (
	"strings"
	"testing"
)

// The tests below pin the CLI contract details that the review found broken.

func TestUsageErrorsKeepMachineOutput(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run("run", "--json")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want usage", code)
	}
	if !strings.Contains(out, `"exit_code": 2`) || !strings.Contains(out, `"error"`) {
		t.Fatalf("machine-mode usage error must write the error object, got %q", out)
	}
	if !strings.Contains(errOut, "prompt is required") {
		t.Fatalf("stderr = %q", errOut)
	}
	// No command at all: stdout stays machine-readable.
	code, out, _ = h.run("--json")
	if code != ExitUsage || !strings.Contains(out, `"exit_code": 2`) {
		t.Fatalf("no-command exit=%d stdout=%q", code, out)
	}
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("run", "--help")
	if code != ExitOK || !strings.Contains(out, "Usage:") {
		t.Fatalf("run --help exit=%d stdout=%q", code, out)
	}
}

func TestServeRejectsInvalidFlags(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("serve", "--queue", "bogus"); code != ExitUsage {
		t.Fatalf("serve --queue bogus exit = %d", code)
	}
	if code, _, _ := h.run("serve", "--max-runs", "-1"); code != ExitUsage {
		t.Fatalf("serve --max-runs -1 exit = %d", code)
	}
}

func TestForkNeverOverwritesASession(t *testing.T) {
	h := newHarness(t)
	var first, second runOutcome
	h.mustJSON(h.ok("run", "--json", "first"), &first)
	h.mustJSON(h.ok("run", "--json", "second"), &second)

	if code, out, _ := h.run("session", "fork", first.SessionID, "--id", second.SessionID, "--json"); code != ExitUsage {
		t.Fatalf("fork onto an existing id exit=%d out=%s", code, out)
	}
	if code, _, _ := h.run("session", "fork", first.SessionID, "--id", first.SessionID, "--json"); code != ExitUsage {
		t.Fatalf("self fork exit = %d", code)
	}
	// The victim kept its history.
	var doc map[string]any
	h.mustJSON(h.ok("session", "show", second.SessionID, "--json"), &doc)
	if messages, _ := doc["messages"].([]any); len(messages) != 2 {
		t.Fatalf("victim messages = %v", len(messages))
	}
	if runs, _ := doc["runs"].([]any); len(runs) != 1 {
		t.Fatalf("victim runs = %v", len(runs))
	}
}

func TestResumeAcceptsFlagsBeforeTheSessionID(t *testing.T) {
	h := newHarness(t)
	var first runOutcome
	h.mustJSON(h.ok("run", "--json", "hello"), &first)

	code, out, errOut := h.run("session", "resume", "--json", first.SessionID, "again")
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
	var second runOutcome
	h.mustJSON(out, &second)
	if second.SessionID != first.SessionID || second.Messages != 4 {
		t.Fatalf("resume outcome = %+v", second)
	}
}

func TestNoSaveLeavesNoSession(t *testing.T) {
	h := newHarness(t)
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--no-save", "--json", "hello"), &outcome)
	if outcome.Status != "completed" {
		t.Fatalf("outcome = %+v", outcome)
	}
	var list []map[string]any
	h.mustJSON(h.ok("session", "list", "--json"), &list)
	if len(list) != 0 {
		t.Fatalf("--no-save left %d sessions", len(list))
	}
}

func TestContextInspectOverridesTheSession(t *testing.T) {
	h := newHarness(t)
	var outcome runOutcome
	h.mustJSON(h.ok("run", "--json", "hello"), &outcome)

	out := h.ok("context", "inspect", outcome.SessionID, "--instruction", "be terse", "--panel", "time", "--json")
	if !strings.Contains(out, "be terse") || !strings.Contains(out, `"panel_bytes": 50`) {
		t.Fatalf("override missing from the report: %s", out)
	}
	// The Session itself was not rewritten.
	var doc map[string]any
	h.mustJSON(h.ok("session", "show", outcome.SessionID, "--json"), &doc)
	if metadata, _ := doc["metadata"].(map[string]any); metadata["gotato.instruction"] != nil {
		t.Fatalf("inspect mutated session metadata: %v", metadata)
	}
}

func TestDoctorCheckNamesMatchTheContract(t *testing.T) {
	h := newHarness(t)
	var report doctorReport
	h.mustJSON(h.ok("doctor", "--json"), &report)
	names := map[string]bool{}
	for _, check := range report.Checks {
		names[check.Name] = true
	}
	for _, want := range []string{"store", "model.echo", "model.demo", "model.gateway", "tools"} {
		if !names[want] {
			t.Fatalf("doctor is missing check %q: %v", want, names)
		}
	}
}
