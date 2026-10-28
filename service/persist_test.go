package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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
	model.Err = errors.New("model exploded")
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
	if !strings.Contains(err.Error(), "model exploded") {
		t.Fatalf("run error lost from the joined error: %v", err)
	}
	if result.Result.Status != gotato.RunStatusFailed {
		t.Fatalf("status = %q err=%v, want failed", result.Result.Status, result.Result.Error)
	}
}
