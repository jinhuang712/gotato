package workload

import (
	"context"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func TestToolFailureAttribution(t *testing.T) {
	failed, ok, blocked, cancelled := string(gotato.ToolResultFailed), string(gotato.ToolResultOK), string(gotato.ToolResultBlocked), string(gotato.ToolResultCanceled)
	cases := []struct {
		call    toolCall
		result  string
		cause   Cause
		code    string
		example string
	}{
		{toolCall{toolID: "orders.lookup", status: failed}, "tool_argument_validation_failure: orders.lookup: arguments.id is required", CauseModel, "tool_argument_validation_failure", ""},
		{toolCall{toolID: "x", status: failed}, `tool_resolution_failure: unknown tool "x"; available tools: a`, CauseModel, "tool_resolution_failure", ""},
		{toolCall{toolID: "orders.lookup", status: failed, executed: true}, "order 7 not found", CauseTool, "", "order 7 not found"},
		{toolCall{toolID: "orders.lookup", status: failed, executed: true}, "tool_execution_failure (Tool): tool panic: boom", CauseTool, "tool_execution_failure", ""},
		{toolCall{toolID: "orders.lookup", status: failed, executed: true}, "internal_invariant_failure (Core): broken", CauseGotato, "internal_invariant_failure", ""},
		{toolCall{toolID: "orders.lookup", status: blocked}, "refused by policy", CauseExtension, "", ""},
		{toolCall{toolID: "orders.lookup", status: cancelled}, "context canceled", CauseCancelled, "", ""},
		// Without a committed result, the Event alone decides the cause.
		{toolCall{toolID: "orders.lookup", status: failed}, "", CauseModel, "", "orders.lookup: failed"},
	}
	for _, c := range cases {
		var committed *gotato.ToolResult
		if c.result != "" {
			committed = &gotato.ToolResult{SafeError: c.result}
		}
		f, isFailure := toolFailure(c.call, committed)
		if !isFailure || f.cause != c.cause || f.code != c.code || (c.example != "" && f.message != c.example) {
			t.Errorf("%+v %q: cause %s code %q message %q", c.call, c.result, f.cause, f.code, f.message)
		}
	}
	if _, isFailure := toolFailure(toolCall{status: ok, executed: true}, nil); isFailure {
		t.Fatal("an ok result is not a failure")
	}
}

func TestRunFailureAttribution(t *testing.T) {
	cases := map[gotato.ErrorCode]Cause{
		gotato.ErrModelFailure:         CauseProvider,
		gotato.ErrModelProtocolFailure: CauseProvider,
		gotato.ErrToolExecutionFailure: CauseTool,
		gotato.ErrToolArgumentFailure:  CauseModel,
		gotato.ErrExtensionFailure:     CauseExtension,
		gotato.ErrLimitExceeded:        CauseLimit,
		gotato.ErrDeadlineExceeded:     CauseLimit,
		gotato.ErrCancelled:            CauseCancelled,
		gotato.ErrInternalInvariant:    CauseGotato,
		gotato.ErrInvalidState:         CauseGotato,
	}
	for code, want := range cases {
		result := service.RunResult{Result: gotato.RunResult{Status: gotato.RunFailed, Error: gotato.ErrorOf(code, "x")}}
		if f, failed := runFailure(result, nil); !failed || f.cause != want {
			t.Errorf("%s: cause %s, want %s", code, f.cause, want)
		}
	}
	if f, failed := runFailure(service.RunResult{Result: gotato.RunResult{Status: gotato.RunCompleted}}, service.ErrNotPersisted); !failed || f.cause != CauseStore {
		t.Fatalf("a Run that was not saved is the store's: %+v", f)
	}
	if _, failed := runFailure(service.RunResult{Result: gotato.RunResult{Status: gotato.RunCompleted}}, nil); failed {
		t.Fatal("a completed Run is not a failure")
	}
}

// TestContextCheckReportsAMismatch makes sure G-G03 can fail: a Turn whose
// prefix hash differs from its inspection is counted and described.
func TestContextCheckReportsAMismatch(t *testing.T) {
	task := testkit.LookupTask()
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{Store: store, Specs: []service.AgentSpec{{Name: task.Name, Model: task.Model, Tools: task.Tools}}})
	if err != nil {
		t.Fatal(err)
	}
	s := testkit.NewSession("where is order 42?", "it shipped")
	s.Set(service.MetaAgent, task.Name)
	var report TaskReport
	checkContext(context.Background(), runner, store, s, []build{{messages: 1, prefix: "not-the-hash"}, {messages: 9, prefix: "x"}}, &report)
	if report.ContextTurns != 2 || report.ContextMismatches != 2 || !strings.Contains(report.ContextDetail, "not-the-hash") {
		t.Fatalf("report = %+v", report)
	}
}

func TestNumberReadsPayloadValues(t *testing.T) {
	for _, value := range []any{3, int64(3), uint32(3), uint64(3), float64(3), "3"} {
		if got := number(value); got != 3 {
			t.Errorf("number(%#v) = %d", value, got)
		}
	}
	if number(nil) != 0 || number("x") != 0 || number(-1) != 0 {
		t.Fatal("an absent or bad value reads as 0")
	}
}
