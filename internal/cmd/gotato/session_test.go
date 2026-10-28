package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// holdModel waits for its gate before it answers, like a Run that is going on
// in another process.
type holdModel struct {
	started chan struct{}
	release chan struct{}
}

func (m *holdModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.started <- struct{}{}
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.EchoModel{}.Stream(ctx, request)
}

// outsideRun starts a Run on the harness's store from a Runner that is not the
// CLI's, which advertises an address, and returns a function that ends it.
func outsideRun(t *testing.T, h *harness, id, address string) func() {
	t.Helper()
	store, err := session.NewFileStore(filepath.Join(h.home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	model := &holdModel{started: make(chan struct{}, 1), release: make(chan struct{})}
	runner, err := service.New(service.Config{Store: store, Instance: "outside", Address: address, Agents: []service.Agent{{Name: "echo", Model: model}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "hello"})
	}()
	select {
	case <-model.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the outside Run never started")
	}
	return func() {
		close(model.release)
		<-done
		_ = runner.Close(context.Background())
	}
}

func TestMessageForARunInAnotherProcessIsNotWriter(t *testing.T) {
	h := newHarness(t)
	var created struct {
		ID string `json:"id"`
	}
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	end := outsideRun(t, h, created.ID, "http://outside.example:8787")

	for _, args := range [][]string{
		{"session", "message", created.ID, "--kind", "interrupt", "--json", "change of plans"},
		{"session", "abort", created.ID, "--json"},
	} {
		code, out, _ := h.run(args...)
		var failure struct {
			ExitCode  int    `json:"exit_code"`
			Code      string `json:"code"`
			NotRun    bool   `json:"not_run"`
			Retryable bool   `json:"retryable"`
			Writer    string `json:"writer"`
		}
		h.mustJSON(out, &failure)
		if code != ExitError || failure.ExitCode != ExitError || failure.Code != "not_writer" || !failure.NotRun || !failure.Retryable || failure.Writer != "http://outside.example:8787" {
			t.Fatalf("%v: exit %d, json %s; want exit 1, not_writer, not run, retryable, with the writer", args, code, out)
		}
	}
	end()

	// With no Run going, the message starts a new Run on the CLI's own instance.
	var outcome runOutcome
	h.mustJSON(h.ok("session", "message", created.ID, "--json", "next"), &outcome)
	if outcome.Status != gotato.RunStatusCompleted || outcome.FinalText != "echo: next" || outcome.Messages != 4 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestMessageUsageAndErrors(t *testing.T) {
	h := newHarness(t)
	var created struct {
		ID string `json:"id"`
	}
	h.mustJSON(h.ok("session", "create", "--json"), &created)
	for name, tc := range map[string]struct {
		args []string
		exit int
	}{
		"no session":  {[]string{"session", "message", "--json"}, ExitUsage},
		"no text":     {[]string{"session", "message", created.ID, "--json"}, ExitUsage},
		"bad kind":    {[]string{"session", "message", created.ID, "--kind", "shout", "--json", "hi"}, ExitUsage},
		"missing":     {[]string{"session", "message", "missing", "--json", "hi"}, ExitNotFound},
		"abort none":  {[]string{"session", "abort", created.ID, "--json"}, ExitError},
		"abort usage": {[]string{"session", "abort", "--json"}, ExitUsage},
	} {
		if code, out, _ := h.run(tc.args...); code != tc.exit || !strings.Contains(out, `"exit_code"`) {
			t.Errorf("%s: exit %d, stdout %s; want exit %d with the error object", name, code, out, tc.exit)
		}
	}
}

func TestServeTakesTheAdvertiseAndForwardFlags(t *testing.T) {
	h := newHarness(t)
	code, _, errOut := h.run("serve", "--addr", "127.0.0.1:0", "--timeout", "30ms", "--drain-timeout", "1ms", "--advertise", "http://127.0.0.1:0", "--forward", "--jsonl")
	if code != ExitOK {
		t.Fatalf("serve exit = %d: %s", code, errOut)
	}
}

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
		Number    uint64 `json:"number"`
		MessageID string `json:"message_id"`
	}
	h.mustJSON(h.ok("session", "notice", created.ID, "--kind", "order_update", "--json", "Order 42 was delivered."), &queued)
	if queued.SessionID != created.ID || queued.Number == 0 || queued.MessageID == "" {
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
	if outcome.Status != gotato.RunStatusCompleted || outcome.Messages != 3 {
		t.Fatalf("outcome = %+v, want user, notice and reply", outcome)
	}
	h.mustJSON(h.ok("session", "show", created.ID, "--json"), &shown)
	notice := shown.Messages[1]
	if notice.Role != gotato.RoleNotice || notice.Notice == nil || notice.Notice.Kind != "order_update" || notice.Notice.Source != gotato.NoticeSourceService || gotato.TextOf(notice) != "Order 42 was delivered." {
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
