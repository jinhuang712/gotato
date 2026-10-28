package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// TestEverySentinelCarriesACode holds the naming rule: a sentinel error is
// Err…, and it carries the Code… that classifies it.
func TestEverySentinelCarriesACode(t *testing.T) {
	cases := []struct {
		err  error
		code gotato.ErrorCode
	}{
		{session.ErrNotFound, gotato.CodeNotFound},
		{session.ErrExists, gotato.CodeInvalidState},
		{session.ErrConflict, gotato.CodeConflict},
		{session.ErrExpired, gotato.CodeInvalidState},
		{service.ErrNotPersisted, gotato.CodeStoreFailure},
		{service.ErrBusy, gotato.CodeBusy},
		{modelctx.ErrBadRecap, gotato.CodeModelProtocolFailure},
		{modelctx.ErrNoRecapContext, gotato.CodeInvalidArgument},
		{testkit.ErrScriptExhausted, gotato.CodeModelFailure},
		{gotato.ErrEventsLost, gotato.CodeCannotResume},
	}
	for _, c := range cases {
		if !gotato.IsCode(c.err, c.code) {
			t.Errorf("%v does not carry %q", c.err, c.code)
		}
	}
}

// TestSentinelsThatShareACodeAreToldApart: errors.Is matches a sentinel by
// identity, and gotato.IsCode matches by code.
func TestSentinelsThatShareACodeAreToldApart(t *testing.T) {
	if errors.Is(session.ErrExpired, session.ErrExists) || errors.Is(session.ErrExists, session.ErrExpired) {
		t.Error("session.ErrExpired and session.ErrExists match each other")
	}
	if !errors.Is(fmt.Errorf("x: %w", session.ErrExpired), session.ErrExpired) {
		t.Error("a wrapped session.ErrExpired does not match itself")
	}
	if errors.Is(service.ErrNoRun, service.ErrSessionExists) || errors.Is(service.ErrSessionExists, service.ErrNoRun) {
		t.Error("service.ErrNoRun and service.ErrSessionExists match each other")
	}
	if !gotato.IsCode(session.ErrExpired, gotato.CodeInvalidState) {
		t.Error("session.ErrExpired does not carry invalid_state")
	}
	if gotato.Retryable(service.ErrNotPersisted) {
		t.Error("service.ErrNotPersisted is retryable; it was a plain error before it carried a code")
	}
}

// TestCodeStringsMatchTheirNames keeps a code's name and its wire string in step.
func TestCodeStringsMatchTheirNames(t *testing.T) {
	if gotato.CodeInternalInvariant != "internal_invariant" || gotato.CodeToolArgumentFailure != "tool_argument_failure" || gotato.CodeCanceled != "canceled" {
		t.Fatalf("code strings = %q %q %q", gotato.CodeInternalInvariant, gotato.CodeToolArgumentFailure, gotato.CodeCanceled)
	}
}

// failingSaveStore wraps a Store and fails the write that ends a Run.
type failingSaveStore struct {
	session.Store
	mu    sync.Mutex
	saves int
	// failFinal fails the write that ends a Run: the one holding run_ended.
	failFinal bool
	// failRelease fails the write that releases the Session, and only that one.
	failRelease bool
}

func (s *failingSaveStore) fail(entries []session.Entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	for _, entry := range entries {
		if s.failFinal && entry.Kind == gotato.EventKindRunEnded {
			return true
		}
		if s.failRelease && entry.Kind == gotato.EventKindSessionReleased {
			return true
		}
	}
	return false
}

func (s *failingSaveStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.fail(entries) {
		return errors.New("store: disk full")
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *failingSaveStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	if s.fail(entries) {
		return errors.New("store: disk full")
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestUnpersistedRunIsReported(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true}
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if !errors.Is(err, service.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted", err)
	}
	if result.Result.Status != gotato.RunStatusFailed || !gotato.IsCode(result.Result.Error, gotato.CodeStoreFailure) || result.FinalText != "echo: hi" {
		t.Fatalf("run outcome lost: %+v", result)
	}
}

func TestLostReleaseKeepsTheRunCompletedAndWarns(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failRelease: true}
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := instanceRecorder(t, runner)
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if err != nil || result.Result.Status != gotato.RunStatusCompleted || result.Result.Error != nil || result.FinalText != "echo: hi" {
		t.Fatalf("a Run whose Events are all stored = %+v, %v, want completed without an error", result, err)
	}
	instance.Wait(5*time.Second, func(event gotato.Event) bool { return event.Kind() == gotato.EventKindStoreWriteFailed })
	if len(instance.OfKind(gotato.EventKindStoreWriteFailed)) == 0 {
		t.Fatalf("no store_write_failed warning among %v", instance.Kinds())
	}
}

// unbuildableSpec is an Agent whose live Session cannot be built: a Run creates
// the Record, then fails before the live Session starts.
func unbuildableSpec() service.Agent {
	negative := -1.0
	return service.Agent{Name: "echo", Model: testkit.EchoModel{}, Options: []gotato.SessionOption{
		gotato.WithModelConfig(gotato.ModelConfig{Temperature: &negative}),
	}}
}

func TestFailedOneShotLeavesNoSession(t *testing.T) {
	runner, store := newRunner(t, unbuildableSpec())
	_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "x"})
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("failed one-shot left %d sessions (err=%v)", len(list), err)
	}
}

// failingDeleteStore fails every Delete, standing in for a Store whose cleanup
// cannot remove the one-shot Session.
type failingDeleteStore struct {
	session.Store
	err error
}

func (s *failingDeleteStore) Delete(context.Context, string) error { return s.err }

func TestFailedOneShotCleanupIsReported(t *testing.T) {
	delErr := errors.New("store: delete denied")
	runner, err := service.New(service.Config{
		Store:  &failingDeleteStore{Store: session.NewMemoryStore(), err: delErr},
		Agents: []service.Agent{unbuildableSpec()},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), service.RunRequest{Prompt: "x"})
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if !errors.Is(err, delErr) {
		t.Fatalf("cleanup failure not reported: %v", err)
	}
}

func TestUnpersistedFailedRunStillReportsSaveFailure(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true}
	model := testkit.NewFakeModel(testkit.Text("x"))
	modelErr := errors.New("model exploded")
	model.Err = modelErr
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "boom", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if !errors.Is(err, service.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted in the chain", err)
	}
	// The Run's error is in the chain; its foreign text stays on the Cause.
	if !errors.Is(err, modelErr) {
		t.Fatalf("run error lost from the joined error: %v", err)
	}
	if result.Result.Status != gotato.RunStatusFailed {
		t.Fatalf("status = %q err=%v, want failed", result.Result.Status, result.Result.Error)
	}
}
