package gotato

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// errModel fails every call with err, or waits for its ctx when err is nil.
type errModel struct{ err error }

func (m errModel) Stream(ctx context.Context, _ ModelRequest) (ModelStream, error) {
	if m.err != nil {
		return nil, m.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func runWithModel(t *testing.T, model Model, limits CoreLimits) (RunResult, error) {
	t.Helper()
	session, err := NewSession(context.Background(), WithModel(model), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	return session.Prompt(context.Background(), UserMessage("go"))
}

// A Model that returns a deadline error of its own (an HTTP client timeout)
// while no context of the call ended has failed: it is a retryable
// model_failure, not the caller's deadline.
func TestProviderTimeoutIsAModelFailure(t *testing.T) {
	result, err := runWithModel(t, errModel{err: fmt.Errorf("call: %w", context.DeadlineExceeded)}, CoreLimits{})
	if !IsCode(err, CodeModelFailure) || !Retryable(err) {
		t.Fatalf("err = %v, want a retryable model_failure", err)
	}
	if result.Status != RunStatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
}

// The ModelCallDeadline ends a call as deadline_exceeded, says which limit
// did, and a negative value turns it off.
func TestModelCallDeadlineNamesItsLimitAndNegativeDisablesIt(t *testing.T) {
	result, err := runWithModel(t, errModel{}, CoreLimits{ModelCallDeadline: 50 * time.Millisecond})
	if !IsCode(err, CodeDeadlineExceeded) || result.Status != RunStatusDeadlineExceeded {
		t.Fatalf("status %q, err %v", result.Status, err)
	}
	if got := result.Error.Details["limit"]; got != "50ms" {
		t.Fatalf("Details[limit] = %q", got)
	}

	session, err := NewSession(context.Background(), WithModel(errModel{}), WithLimits(CoreLimits{ModelCallDeadline: -1}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if session.limits.ModelCallDeadline >= 0 {
		t.Fatalf("a negative deadline was replaced by %v", session.limits.ModelCallDeadline)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, _ = session.Prompt(ctx, UserMessage("go"))
	if result.Status != RunStatusDeadlineExceeded || time.Since(started) < 100*time.Millisecond {
		t.Fatalf("status %q after %v: only the caller's ctx should end the call", result.Status, time.Since(started))
	}
	if _, has := result.Error.Details["limit"]; has {
		t.Fatalf("the caller's deadline is reported as the limit: %v", result.Error.Details)
	}
}
