package gotato_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// When the caller's ctx ends while a Run is in flight, Prompt returns the
// Run's settled result, not an empty one: the Run ends with the ctx, so the
// status says why, and the Session is idle again.
func TestPromptReturnsTheSettledResultWhenItsCtxEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ctx    func() (context.Context, context.CancelFunc)
		status gotato.RunStatus
		code   gotato.ErrorCode
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, gotato.RunStatusDeadlineExceeded, gotato.CodeDeadlineExceeded},
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, gotato.RunStatusCanceled, gotato.CodeCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The tool blocks until its ctx ends, as a well-behaved tool does.
			slow, err := gotato.NewFuncTool("slow", "waits for its ctx", func(ctx context.Context, _ struct{}) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			model := testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
				return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprint(time.Now().UnixNano())), ToolID: "slow", Arguments: []byte(`{}`)}), nil
			})
			session, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(slow))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			ctx, cancel := tc.ctx()
			defer cancel()
			result, err := session.Prompt(ctx, gotato.UserMessage("go"))
			if !gotato.IsCode(err, tc.code) {
				t.Fatalf("Prompt error = %v, want code %q", err, tc.code)
			}
			if result.Status != tc.status || result.RunID == "" {
				t.Fatalf("result = %+v, want status %q and a RunID", result, tc.status)
			}
			if status := session.Status(); status != gotato.SessionStatusIdle {
				t.Fatalf("Session status = %q after Prompt returned, want idle", status)
			}
		})
	}
}
