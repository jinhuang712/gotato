package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// heldRun starts a Run on a that stays in the model call until release is
// closed, and returns once the model has been called.
func heldRun(t *testing.T, a *service.Runner, model *testkit.FakeModel, sessionID string) (release func(), result chan error) {
	t.Helper()
	block := make(chan struct{})
	model.Block = block
	result = make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), service.RunRequest{SessionID: sessionID, Prompt: "first"})
		result <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var once bool
	return func() {
		if !once {
			once = true
			close(block)
		}
	}, result
}

// A Run going on another Instance is not deleted from under it: the delete is
// refused as not_writer, as a message is, and the Session is still there.
func TestDeleteSessionRefusesARunOnAnotherInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	release, result := heldRun(t, a, model, s.ID())
	defer release()

	err = b.DeleteSession(ctx, s.ID())
	if !gotato.IsCode(err, gotato.CodeNotWriter) || !gotato.NotRun(err) {
		t.Fatalf("DeleteSession of a Session with a Run on another Instance = %v, want not_writer, not run", err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("the Run that was not deleted from under it: %v", err)
	}
	if _, err := b.Record(ctx, s.ID()); err != nil {
		t.Fatalf("the Session is gone: %v", err)
	}
	// With the Run over, the delete goes through.
	if err := b.DeleteSession(ctx, s.ID()); err != nil {
		t.Fatalf("DeleteSession of an idle Session: %v", err)
	}
}

// A Session deleted under a Run (by a store operation, or an Instance that
// did not check) ends that Run as not_found, not as a store_failure that
// asks the caller to retry a request that cannot succeed.
func TestRunOfASessionDeletedUnderItIsNotAStoreFailure(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	release, result := heldRun(t, a, model, s.ID())
	if err := store.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	release()
	err = <-result
	if err == nil {
		t.Fatal("a Run whose Session was deleted under it returned no error")
	}
	if gotato.IsCode(err, gotato.CodeStoreFailure) && !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Run error = %v: a deleted Session is not a store failure", err)
	}
	if !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("Run error = %v, want not_found", err)
	}
	if gotato.Retryable(err) || gotato.NotRun(err) {
		t.Fatalf("Run error = %v: retryable=%v not_run=%v; the Run ran and a retry finds nothing", err, gotato.Retryable(err), gotato.NotRun(err))
	}
}

// A Compact that waits for a Session whose Run is on another Instance answers
// busy after the wait, as a Run does; it does not wait for ever or compact
// under the Run.
func TestCompactOfASessionRunningElsewhereAnswersBusy(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, service.Liveness{TTL: 5 * time.Second, Wait: 200 * time.Millisecond})
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	release, result := heldRun(t, a, model, s.ID())
	defer release()
	_, err = b.Compact(ctx, s.ID(), modelctx.CompactConfig{})
	if !gotato.IsCode(err, gotato.CodeBusy) || !gotato.NotRun(err) {
		t.Fatalf("Compact of a Session with a Run on another Instance = %v, want busy, not run", err)
	}
	release()
	<-result
}

// A Fork taken while a Run is going on a live Instance copies what the Record
// holds; it must not carry the tool result "the instance running this tool
// stopped", which is true only of a Record whose writer is gone.
func TestForkMidRunDoesNotCarryAFalseStoppedToolResult(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	started := make(chan struct{})
	finish := make(chan struct{})
	tool, err := gotato.NewFuncTool("slow", "slow tool", func(context.Context, struct{}) (string, error) {
		close(started)
		<-finish
		return "done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: "slow", Arguments: []byte(`{}`)}), testkit.Text("ok"))
	a, err := service.New(service.Config{
		Store: store, Instance: "instance-a", Liveness: steadyLiveness,
		Agents: []service.Agent{{Name: "echo", Model: model, Tools: []gotato.Tool{tool}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(ctx) })
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the tool did not start")
	}
	child, err := b.Fork(ctx, s.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	for _, message := range child.Messages() {
		if text := gotato.TextOf(message); len(text) > 0 && containsStopped(text) {
			t.Fatalf("the Fork carries a false tool result: %q", text)
		}
	}
}

func containsStopped(text string) bool {
	const marker = "stopped before the tool finished"
	for i := 0; i+len(marker) <= len(text); i++ {
		if text[i:i+len(marker)] == marker {
			return true
		}
	}
	return false
}
