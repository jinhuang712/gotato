package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/testkit"
)

// deadlineProbe is a PreToolUse Extension that records the deadline of the
// Run's context when the Run reaches its first Tool call.
type deadlineProbe struct {
	mu       sync.Mutex
	deadline time.Time
	has      bool
}

func (p *deadlineProbe) Before(ctx context.Context, _ gotato.ToolUse) (gotato.PreToolDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deadline, p.has = ctx.Deadline()
	return gotato.PreToolDecision{}, nil
}

// runDeadline runs one Run of an Agent with the given RunTimeout under the
// caller's ctx and request, and returns how far ahead of now the Run's
// context deadline was when the Run reached its first Tool call.
func runDeadline(t *testing.T, ctx context.Context, runTimeout, requestTimeout time.Duration) (time.Duration, bool) {
	t.Helper()
	probe := &deadlineProbe{}
	runner, _ := newRunner(t, service.Agent{
		Name:       "probe",
		Model:      testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "lookup", Arguments: []byte(`{}`)}), testkit.Text("done")),
		Tools:      []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
		Extensions: []any{probe},
		RunTimeout: runTimeout,
	})
	start := time.Now()
	result, err := runner.Run(ctx, service.RunRequest{Prompt: "go", Timeout: requestTimeout})
	if err != nil || result.Result.Status != gotato.RunCompleted {
		t.Fatalf("Run = %+v, %v", result.Result, err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.deadline.Sub(start), probe.has
}

// A Run of an Agent with no RunTimeout has a deadline of 30 minutes.
func TestARunHasADefaultDeadlineOf30Minutes(t *testing.T) {
	ahead, has := runDeadline(t, context.Background(), 0, 0)
	if !has || ahead > service.DefaultRunTimeout+time.Minute || ahead < service.DefaultRunTimeout-time.Minute || service.DefaultRunTimeout != 30*time.Minute {
		t.Fatalf("deadline %v ahead (set %v); want about 30 minutes", ahead, has)
	}
}

// The Agent's RunTimeout replaces the default, the request's Timeout replaces
// the Agent's, and a negative RunTimeout leaves the Run without a deadline.
func TestTheAgentsRunTimeoutAndTheRequestsTimeoutOverrideTheDefault(t *testing.T) {
	ahead, has := runDeadline(t, context.Background(), time.Hour, 0)
	if !has || ahead < 59*time.Minute || ahead > time.Hour+time.Minute {
		t.Fatalf("Agent RunTimeout: deadline %v ahead (set %v); want about 1 hour", ahead, has)
	}
	ahead, has = runDeadline(t, context.Background(), time.Hour, 5*time.Minute)
	if !has || ahead < 4*time.Minute || ahead > 6*time.Minute {
		t.Fatalf("request Timeout: deadline %v ahead (set %v); want about 5 minutes", ahead, has)
	}
	if _, has = runDeadline(t, context.Background(), -1, 0); has {
		t.Fatal("a negative RunTimeout still set a deadline")
	}
}

// A deadline on the caller's context that comes sooner wins over the default.
func TestACallersShorterDeadlineOverridesTheDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ahead, has := runDeadline(t, ctx, 0, 0)
	if !has || ahead > 2*time.Minute+time.Second {
		t.Fatalf("deadline %v ahead (set %v); want the caller's 2 minutes", ahead, has)
	}
}

// A Run that outlasts its deadline settles as deadline_exceeded.
func TestARunPastItsRunTimeoutSettlesAsDeadlineExceeded(t *testing.T) {
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	runner, _ := newRunner(t, service.Agent{Name: "slow", Model: model, RunTimeout: 50 * time.Millisecond})
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "go"})
	if err == nil || result.Result.Status != gotato.RunDeadlineExceeded {
		t.Fatalf("Run = %+v, %v; want deadline_exceeded", result.Result, err)
	}
}
