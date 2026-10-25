package workload_test

import (
	"context"
	"errors"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// An embedding service, such as a chat bot, calls a Runner in process and never
// meets HTTP or gRPC. These tests read only what that caller gets back: the
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
		if !gotato.Retryable(err) || !gotato.IsCode(err, gotato.ErrDraining) {
			t.Fatalf("Run on a draining instance = %v, want draining and retryable", err)
		}
		if res.Result.Status != "" {
			t.Fatalf("a refused Run has the status %q, want none", res.Result.Status)
		}
	})

	t.Run("a Session that does not exist", func(t *testing.T) {
		r, _ := newRig(t)
		res, err := r.runner.Run(ctx, service.RunRequest{SessionID: "missing", Prompt: "hello"})
		if !gotato.IsCode(err, gotato.ErrNotFound) || !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Run on a missing Session = %v, want not_found", err)
		}
		if !gotato.NotRun(err) || gotato.Retryable(err) || res.Result.Status != "" {
			t.Fatalf("Run on a missing Session: not run %v, retryable %v, status %q", gotato.NotRun(err), gotato.Retryable(err), res.Result.Status)
		}
	})
}

func TestEmbeddedCallerSeesALostFinalWriteAsNotSaved(t *testing.T) {
	// A clean play counts the Run's writes; the last one ends the Run.
	clean, cleanStore := newRig(t)
	cleanID, _ := clean.seed()
	cleanStore.reset(0)
	if _, err := clean.settled(service.RunRequest{SessionID: cleanID, Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	last := cleanStore.writes

	r, store := newRig(t)
	id, _ := r.seed()
	store.reset(last)
	res, err := r.settled(service.RunRequest{SessionID: id, Prompt: "hello"})
	if res.Result.Status != gotato.RunNotSaved {
		t.Fatalf("status = %q (err %v), want %q", res.Result.Status, err, gotato.RunNotSaved)
	}
	if !errors.Is(err, service.ErrNotPersisted) || res.Result.Error == nil {
		t.Fatalf("err = %v, result error = %v, want both to say the Run was not saved", err, res.Result.Error)
	}
	if !gotato.Retryable(err) || gotato.NotRun(err) {
		t.Fatalf("a failed final write: retryable %v, not run %v, want retryable and not known to be not run", gotato.Retryable(err), gotato.NotRun(err))
	}
	if res.FinalText == "" {
		t.Fatal("the reply that was made is lost")
	}
}
