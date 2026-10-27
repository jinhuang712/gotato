package workload_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// An embedding service, such as a chat bot, calls a Runner in process and never
// meets HTTP. These tests read only what that caller gets back: the
// error and the RunResult.

func TestEmbeddedCallerSeesARefusalAsNotRun(t *testing.T) {
	ctx := context.Background()

	t.Run("a draining instance", func(t *testing.T) {
		r, _ := newRig(t)
		id, _ := r.seed()
		if err := r.runner.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		res, err := r.runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hello"})
		if !gotato.NotRun(err) {
			t.Fatalf("Run on a draining instance = %v, want an error that says not run", err)
		}
		if !gotato.Retryable(err) || !gotato.IsCode(err, gotato.CodeDraining) {
			t.Fatalf("Run on a draining instance = %v, want draining and retryable", err)
		}
		if res.Result.Status != "" {
			t.Fatalf("a refused Run has the status %q, want none", res.Result.Status)
		}
	})

	t.Run("a Session that does not exist", func(t *testing.T) {
		r, _ := newRig(t)
		res, err := r.runner.Run(ctx, service.RunRequest{SessionID: "missing", Prompt: "hello"})
		if !gotato.IsCode(err, gotato.CodeNotFound) || !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Run on a missing Session = %v, want not_found", err)
		}
		if !gotato.NotRun(err) || gotato.Retryable(err) || res.Result.Status != "" {
			t.Fatalf("Run on a missing Session: not run %v, retryable %v, status %q", gotato.NotRun(err), gotato.Retryable(err), res.Result.Status)
		}
	})
}

func TestEmbeddedCallerSeesALostFinalWrite(t *testing.T) {
	// A clean play counts the Run's writes. The last releases the Session; the
	// one before it ends the Run.
	clean, cleanStore := newRig(t)
	cleanID, _ := clean.seed()
	cleanStore.reset(0)
	if _, err := clean.settled(service.RunRequest{SessionID: cleanID, Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	last := cleanStore.writes

	t.Run("the write that ends the Run fails", func(t *testing.T) {
		r, store := newRig(t)
		id, _ := r.seed()
		store.reset(last - 1)
		res, err := r.settled(service.RunRequest{SessionID: id, Prompt: "hello"})
		if res.Result.Status != gotato.RunStatusFailed {
			t.Fatalf("status = %q (err %v), want %q", res.Result.Status, err, gotato.RunStatusFailed)
		}
		if res.Result.Error == nil || !gotato.IsCode(res.Result.Error, gotato.CodeStoreFailure) {
			t.Fatalf("result error = %v, want store_failure", res.Result.Error)
		}
		if !errors.Is(err, service.ErrNotPersisted) {
			t.Fatalf("err = %v, want ErrNotPersisted", err)
		}
		if !gotato.Retryable(err) || gotato.NotRun(err) {
			t.Fatalf("a failed final write: retryable %v, not run %v, want retryable and not known to be not run", gotato.Retryable(err), gotato.NotRun(err))
		}
	})

	t.Run("only the release of the Session fails", func(t *testing.T) {
		r, store := newRig(t)
		id, _ := r.seed()
		store.reset(last)
		res, err := r.settled(service.RunRequest{SessionID: id, Prompt: "hello"})
		if err != nil || res.Result.Status != gotato.RunStatusCompleted || res.Result.Error != nil {
			t.Fatalf("a Run whose Events are all stored = %q, %v, %v, want completed without an error", res.Result.Status, res.Result.Error, err)
		}
		if res.FinalText == "" {
			t.Fatal("the reply is lost")
		}
	})
}
