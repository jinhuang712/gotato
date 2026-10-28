package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// AbortRun(first) names a Run that already settled (its end is
// stored, only its save is pending). Its documented answer is ErrNoRun, "already
// stopped". The handle still carries the first Run's ID and its stopped flag
// is never cleared by rearm, so the call succeeds and cancels the Run that
// follows with the messages the first one left.
func TestAbortOfSettledRunMustNotStopTheFollowOnRun(t *testing.T) {
	store := newKindGate(session.NewMemoryStore(), gotato.EventKindSessionReleased)
	releaseHold := store.holds[gotato.EventKindSessionReleased]
	inFirst, failFirst := make(chan struct{}), make(chan struct{})
	inSecond, leaveSecond := make(chan struct{}), make(chan struct{})
	var model *testkit.FuncModel
	model = testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		if model.Calls() == 1 {
			close(inFirst)
			<-failFirst
			return nil, errors.New("provider down")
		}
		close(inSecond)
		select {
		case <-leaveSecond:
			return testkit.Text("second"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	runner := newRunnerOn(t, store, service.Agent{Name: "a", Model: model, Warm: service.WarmPolicy{TTL: -1}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.SubscribeInstance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := testkit.RecordSubscription(sub)
	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "one"}) }()
	waitChan(t, "the first model call", inFirst)
	// A message the first Run will not take before it fails: it is left for
	// the Run that follows.
	if r, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: service.MessageKindSteer, Text: "late"}); err != nil || !r.Queued {
		t.Fatalf("Send = %+v, %v", r, err)
	}
	var first gotato.RunID
	rec.Wait(5*time.Second, func(e gotato.Event) bool {
		if e.Kind() == gotato.EventKindRunStarted {
			first = e.Head().RunID
		}
		return first != ""
	})
	close(failFirst)
	waitChan(t, "the first Run's release write", releaseHold.entered)
	// The first Run is settled and its end is stored; only its save is held.
	abortErr := runner.AbortSession(ctx, s.ID(), service.AbortRun(first))
	close(releaseHold.proceed)
	if !errors.Is(abortErr, service.ErrNoRun) {
		t.Errorf("AbortRun(settled run) = %v, want ErrNoRun", abortErr)
	}
	waitChan(t, "second model call", inSecond)
	close(leaveSecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ended := rec.OfKind(gotato.EventKindRunEnded)
		if len(ended) >= 2 {
			if second := ended[1].(gotato.RunEnded); second.Status != gotato.RunStatusCompleted {
				t.Fatalf("the follow-on Run ended %q, want completed (it was never the target of the abort)", second.Status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow-on Run never ended; kinds %v, model calls %d", rec.Kinds(), model.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
