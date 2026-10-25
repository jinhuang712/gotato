package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// holdBeforeRelease starts a Run and holds it after run_ended, when it takes no
// message any more and still holds the Session.
func holdBeforeRelease(t *testing.T) (runner *service.Runner, id string, hold *holdAtTheEnd, done chan service.RunResult) {
	t.Helper()
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	hold = newHold()
	runner.SetBeforeRelease(hold.hook)
	s, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	id = s.ID()
	done = make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "hello"})
		done <- result
	}()
	select {
	case <-hold.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached the end")
	}
	return runner, id, hold, done
}

// A message that arrives after run_ended and before the Run releases the
// Session waits for the release and runs in the next Run; it is not busy.
func TestAMessageWhileARunIsEndingRunsInTheNextRun(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	type sent struct {
		result service.MessageResult
		err    error
	}
	answered := make(chan sent, 1)
	go func() {
		result, err := runner.Send(context.Background(), service.MessageRequest{SessionID: id, Kind: service.MessageFollowUp, Text: "again"})
		answered <- sent{result, err}
	}()
	select {
	case got := <-answered:
		t.Fatalf("Send answered before the Run released the Session: %+v, %v", got.result, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(hold.proceed)
	if first := <-done; first.FinalText != "echo: hello" {
		t.Fatalf("first Run = %q", first.FinalText)
	}
	select {
	case got := <-answered:
		if got.err != nil || got.result.Queued || got.result.Run == nil || got.result.Run.FinalText != "echo: again" {
			t.Fatalf("Send = %+v, %v; want a following Run answering \"echo: again\"", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never answered")
	}
}

// The wait is bounded by the caller's context.
func TestAMessageWhileARunIsEndingGivesUpWithItsContext(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := runner.Send(ctx, service.MessageRequest{SessionID: id, Kind: service.MessageSteer, Text: "again"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send = %v; want the context's deadline", err)
	}
	close(hold.proceed)
	<-done
}

// A Stop in the same window finds no Run to stop, and a notice waits for the
// next Run like any notice between Runs.
func TestAStopAndANoticeWhileARunIsEnding(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	err := runner.CancelSession(context.Background(), id)
	t.Logf("CancelSession in the window: %v", err)
	if err != nil && !errors.Is(err, service.ErrNoRun) {
		t.Fatalf("CancelSession = %v; want nil or ErrNoRun", err)
	}
	answered := make(chan error, 1)
	go func() {
		_, err := runner.Notice(context.Background(), service.NoticeRequest{SessionID: id, Kind: "info", Text: "fyi"})
		answered <- err
	}()
	close(hold.proceed)
	<-done
	if err := <-answered; err != nil {
		t.Fatalf("Notice = %v", err)
	}
}
