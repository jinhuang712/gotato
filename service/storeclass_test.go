package service_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// wantNotRunStoreFailure checks that err is a store_failure that says it did
// not run and may be retried, and that the store's raw error is its cause.
func wantNotRunStoreFailure(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded, want a store failure", what)
	}
	if !gotato.IsCode(err, gotato.CodeStoreFailure) {
		t.Fatalf("%s = %v (%T), want code store_failure", what, err, err)
	}
	if !gotato.NotRun(err) || !gotato.Retryable(err) {
		t.Fatalf("%s = %v: not_run=%v retryable=%v, want both", what, err, gotato.NotRun(err), gotato.Retryable(err))
	}
}

// Every store call a request makes before it writes any step of a Run is
// classified: a raw error of the store is store_failure, marked not run and
// retryable, as the write of a step is store_failure.
func TestStoreErrorsBeforeAnyRunWriteAreClassified(t *testing.T) {
	ctx := context.Background()
	newRunner := func(t *testing.T) (*service.Runner, *faultStore, string) {
		store := &faultStore{Store: session.NewMemoryStore()}
		runner := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
		s, err := runner.CreateSession(ctx, "echo", nil)
		if err != nil {
			t.Fatal(err)
		}
		return runner, store, s.ID()
	}

	t.Run("Load", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Load", true)
		_, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hi"})
		wantNotRunStoreFailure(t, "Run with Load failing", err)
		store.fail("Load", false)
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hi"}); err != nil {
			t.Fatalf("the same request after the store returned: %v", err)
		}
	})
	t.Run("Create by Run", func(t *testing.T) {
		runner, store, _ := newRunner(t)
		store.fail("Create", true)
		_, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		wantNotRunStoreFailure(t, "Run that creates, with Create failing", err)
	})
	t.Run("Create", func(t *testing.T) {
		runner, store, _ := newRunner(t)
		store.fail("Create", true)
		_, err := runner.CreateSession(ctx, "echo", nil)
		wantNotRunStoreFailure(t, "CreateSession with Create failing", err)
	})
	t.Run("Beat", func(t *testing.T) {
		store := &faultStore{Store: session.NewMemoryStore()}
		runner := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
		store.fail("Beat", true)
		_, err := runner.CreateSession(ctx, "echo", nil)
		wantNotRunStoreFailure(t, "CreateSession with the first Beat failing", err)
		_, err = runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		wantNotRunStoreFailure(t, "Run with the first Beat failing", err)
	})
	t.Run("Delete", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Delete", true)
		wantNotRunStoreFailure(t, "DeleteSession with Delete failing", runner.DeleteSession(ctx, id))
	})
	t.Run("Fork", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Log", true)
		_, err := runner.Fork(ctx, id, "")
		wantNotRunStoreFailure(t, "Fork with the read failing", err)
	})
	t.Run("Record", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Log", true)
		_, err := runner.Record(ctx, id)
		wantNotRunStoreFailure(t, "Record with the read failing", err)
		_, err = runner.Events(ctx, id)
		wantNotRunStoreFailure(t, "Events with the read failing", err)
	})
}

// A missing Session stays not_found: the store's own answer is not a failure.
func TestStoreAnswersKeepTheirCode(t *testing.T) {
	ctx := context.Background()
	store := &faultStore{Store: session.NewMemoryStore()}
	runner := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: "missing", Prompt: "hi"}); !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("Run on a missing Session = %v, want not_found", err)
	}
	if err := runner.DeleteSession(ctx, "missing"); !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("DeleteSession of a missing Session = %v, want not_found", err)
	}
}
