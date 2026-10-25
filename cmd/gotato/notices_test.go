package main

import (
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
)

// A notice from the CLI starts no Run: its notice_queued is stored with the
// Session, and the next Run, from another process, puts it in after the
// prompt.
func TestNoticeWaitsForTheNextRunAndStartsNone(t *testing.T) {
	h := newHarness(t)
	var created struct {
		ID string `json:"id"`
	}
	h.mustJSON(h.ok("session", "create", "--json"), &created)

	var queued struct {
		SessionID string `json:"session_id"`
		Queued    bool   `json:"queued"`
		Number    uint64 `json:"number"`
		MessageID string `json:"message_id"`
	}
	h.mustJSON(h.ok("session", "notice", created.ID, "--kind", "order_update", "--json", "Order 42 was delivered."), &queued)
	if queued.SessionID != created.ID || !queued.Queued || queued.Number == 0 || queued.MessageID == "" {
		t.Fatalf("notice = %+v", queued)
	}
	var shown struct {
		Messages []gotato.Message `json:"messages"`
	}
	h.mustJSON(h.ok("session", "show", created.ID, "--json"), &shown)
	if len(shown.Messages) != 0 {
		t.Fatalf("a notice entered the Transcript before a Run: %+v", shown.Messages)
	}

	var outcome runOutcome
	h.mustJSON(h.ok("run", "--session", created.ID, "--json", "hello"), &outcome)
	if outcome.Status != gotato.RunCompleted || outcome.Messages != 3 {
		t.Fatalf("outcome = %+v, want user, notice and reply", outcome)
	}
	h.mustJSON(h.ok("session", "show", created.ID, "--json"), &shown)
	notice := shown.Messages[1]
	if notice.Role != gotato.RoleNotice || notice.Notice == nil || notice.Notice.Kind != "order_update" || notice.Notice.Source != gotato.NoticeFromService || gotato.TextOf(notice) != "Order 42 was delivered." {
		t.Fatalf("the Record keeps %+v after the prompt", notice)
	}
	events := h.ok("events", "--session", created.ID)
	for _, kind := range []string{`"kind":"notice_queued"`, `"kind":"notice_committed"`} {
		if !strings.Contains(events, kind) {
			t.Errorf("events lack %s:\n%s", kind, events)
		}
	}
}

func TestNoticeUsageAndErrors(t *testing.T) {
	h := newHarness(t)
	var created struct {
		ID string `json:"id"`
	}
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	for name, tc := range map[string]struct {
		args []string
		exit int
	}{
		"no session": {[]string{"session", "notice", "--json"}, ExitUsage},
		"no kind":    {[]string{"session", "notice", created.ID, "--json", "hi"}, ExitUsage},
		"reserved":   {[]string{"session", "notice", created.ID, "--kind", "time_away", "--json", "hi"}, ExitUsage},
		"no text":    {[]string{"session", "notice", created.ID, "--kind", "k", "--json"}, ExitUsage},
		"missing":    {[]string{"session", "notice", "missing", "--kind", "k", "--json", "hi"}, ExitNotFound},
		"bad window": {[]string{"run", "--time-away", "banana", "--json", "hi"}, ExitUsage},
	} {
		if code, out, _ := h.run(tc.args...); code != tc.exit || !strings.Contains(out, `"exit_code"`) {
			t.Errorf("%s: exit %d, stdout %s; want exit %d with the error object", name, code, out, tc.exit)
		}
	}
}

// A Run going in another process takes its notice itself: from here the answer
// is not_writer, with the writer's address.
func TestNoticeForARunInAnotherProcessIsNotWriter(t *testing.T) {
	h := newHarness(t)
	var created struct {
		ID string `json:"id"`
	}
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	end := outsideRun(t, h, created.ID, "http://outside.example:8787")
	defer end()

	code, out, _ := h.run("session", "notice", created.ID, "--kind", "k", "--json", "from the CLI")
	var failure struct {
		ExitCode  int    `json:"exit_code"`
		Code      string `json:"code"`
		NotRun    bool   `json:"not_run"`
		Retryable bool   `json:"retryable"`
		Writer    string `json:"writer"`
	}
	h.mustJSON(out, &failure)
	if code != ExitError || failure.Code != "not_writer" || !failure.NotRun || !failure.Retryable || failure.Writer != "http://outside.example:8787" {
		t.Fatalf("exit %d, json %s; want exit 1, not_writer, not run, retryable, with the writer", code, out)
	}
}
