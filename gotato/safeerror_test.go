package gotato

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type publicErr struct{}

func (publicErr) Error() string  { return "dial tcp 10.0.3.7:5432: password=hunter2" }
func (publicErr) Public() string { return "the order service is unavailable" }

// failingTool returns err from Execute.
type failingTool struct{ err error }

func (t failingTool) Spec() ToolSpec { return (&scriptedTool{}).Spec() }
func (t failingTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{}, t.err
}

func failedToolText(t *testing.T, err error) string {
	t.Helper()
	session, serr := NewSession(context.Background(), WithModel(echoToolModel("call-1")), WithTools(failingTool{err: err}))
	if serr != nil {
		t.Fatal(serr)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, perr := session.Prompt(context.Background(), UserMessage("go")); perr != nil {
		t.Fatal(perr)
	}
	for _, m := range session.Transcript().Messages() {
		if m.ToolResult != nil {
			return m.ToolResult.SafeError
		}
	}
	t.Fatal("no tool result")
	return ""
}

// SafeError does not carry the text of an error the Tool returned, unless the
// error says it is public.
func TestSafeErrorKeepsOnlyPublicText(t *testing.T) {
	raw := failedToolText(t, errors.New("dial tcp 10.0.3.7:5432: password=hunter2"))
	if strings.Contains(raw, "hunter2") || !strings.Contains(raw, "tool failed") {
		t.Fatalf("SafeError = %q", raw)
	}
	wrapped := failedToolText(t, runtimeError(CodeStoreFailure, "db", "the store is down", errors.New("password=hunter2")))
	if strings.Contains(wrapped, "hunter2") {
		t.Fatalf("SafeError = %q", wrapped)
	}
	if got := failedToolText(t, publicErr{}); got != "the order service is unavailable" {
		t.Fatalf("SafeError = %q, want the public text", got)
	}
}

// The Message of an extension failure is fixed; the extension's text stays on
// the Cause.
func TestGuardDoesNotCarryRawErrorText(t *testing.T) {
	failure := guard("stage", func() error { return errors.New("password=hunter2") })
	if failure == nil || strings.Contains(failure.Error(), "hunter2") || failure.Cause == nil {
		t.Fatalf("failure = %v", failure)
	}
}
