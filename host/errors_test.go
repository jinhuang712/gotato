package host_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// TestEverySentinelCarriesACode holds the naming rule: a sentinel error is
// Err…, and it carries the Code… that classifies it.
func TestEverySentinelCarriesACode(t *testing.T) {
	cases := []struct {
		err  error
		code gotato.ErrorCode
	}{
		{record.ErrNotFound, gotato.CodeNotFound},
		{store.ErrExists, gotato.CodeInvalidState},
		{store.ErrConflict, gotato.CodeConflict},
		{store.ErrExpired, gotato.CodeInvalidState},
		{host.ErrNotPersisted, gotato.CodeStoreFailure},
		{host.ErrBusy, gotato.CodeBusy},
		{record.ErrBadRecap, gotato.CodeModelProtocolFailure},
		{record.ErrNoRecapContext, gotato.CodeInvalidArgument},
		{testkit.ErrScriptExhausted, gotato.CodeModelFailure},
		{gotato.ErrEventsLost, host.CodeCannotResume},
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
	if errors.Is(store.ErrExpired, store.ErrExists) || errors.Is(store.ErrExists, store.ErrExpired) {
		t.Error("store.ErrExpired and store.ErrExists match each other")
	}
	if !errors.Is(fmt.Errorf("x: %w", store.ErrExpired), store.ErrExpired) {
		t.Error("a wrapped store.ErrExpired does not match itself")
	}
	if errors.Is(host.ErrNoRun, host.ErrSessionExists) || errors.Is(host.ErrSessionExists, host.ErrNoRun) {
		t.Error("host.ErrNoRun and host.ErrSessionExists match each other")
	}
	if !gotato.IsCode(store.ErrExpired, gotato.CodeInvalidState) {
		t.Error("store.ErrExpired does not carry invalid_state")
	}
	if gotato.Retryable(host.ErrNotPersisted) {
		t.Error("host.ErrNotPersisted is retryable; it was a plain error before it carried a code")
	}
}

// TestCodeStringsMatchTheirNames keeps a code's name and its wire string in step.
func TestCodeStringsMatchTheirNames(t *testing.T) {
	if gotato.CodeInternalInvariant != "internal_invariant" || gotato.CodeToolArgumentFailure != "tool_argument_failure" || gotato.CodeCanceled != "canceled" {
		t.Fatalf("code strings = %q %q %q", gotato.CodeInternalInvariant, gotato.CodeToolArgumentFailure, gotato.CodeCanceled)
	}
}

func TestUnpersistedRunIsReported(t *testing.T) {
	store := &failingSaveStore{parts: partsOf(memStore()), failFinal: true}
	runner, err := host.New(host.Config{
		Store:    asStore(store),
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}), DefaultAgent: "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), host.RunRequest{Prompt: "hi"})
	if !errors.Is(err, host.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted", err)
	}
	if result.Result.Status != gotato.RunStatusFailed || !gotato.IsCode(result.Result.Error, gotato.CodeStoreFailure) || result.FinalText != "echo: hi" {
		t.Fatalf("run outcome lost: %+v", result)
	}
}

func TestLostReleaseKeepsTheRunCompletedAndWarns(t *testing.T) {
	store := &failingSaveStore{parts: partsOf(memStore()), failRelease: true}
	runner, err := host.New(host.Config{
		Store:    asStore(store),
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}), DefaultAgent: "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	instance := instanceRecorder(t, runner)
	result, err := runner.Run(context.Background(), host.RunRequest{Prompt: "hi"})
	if err != nil || result.Result.Status != gotato.RunStatusCompleted || result.Result.Error != nil || result.FinalText != "echo: hi" {
		t.Fatalf("a Run whose Events are all stored = %+v, %v, want completed without an error", result, err)
	}
	instance.Wait(5*time.Second, func(event gotato.Event) bool { return event.Kind() == host.EventKindStoreWriteFailed })
	if len(instance.OfKind(host.EventKindStoreWriteFailed)) == 0 {
		t.Fatalf("no store_write_failed warning among %v", instance.Kinds())
	}
}

// unbuildableSpec is an Agent whose live Session cannot be built: a Run creates
// the Record, then fails before the live Session starts.
func unbuildableSpec() profile.AgentProfile {
	negative := -1.0
	return profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}, Sampling: gotato.Sampling{Temperature: &negative}}
}

func TestFailedOneShotLeavesNoSession(t *testing.T) {
	runner, store := newRunner(t, unbuildableSpec())
	_, err := runner.Run(context.Background(), host.RunRequest{Prompt: "x"})
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	list, err := listAll(context.Background(), store)
	if err != nil || len(list) != 0 {
		t.Fatalf("failed one-shot left %d sessions (err=%v)", len(list), err)
	}
}

// failingDeleteStore fails every Delete, standing in for a Store whose cleanup
// cannot remove the one-shot Session.
type failingDeleteStore struct {
	parts
	err error
}

func (s *failingDeleteStore) Delete(context.Context, string) error { return s.err }

func TestFailedOneShotCleanupIsReported(t *testing.T) {
	delErr := errors.New("store: delete denied")
	runner, err := host.New(host.Config{
		Store:    asStore(&failingDeleteStore{parts: partsOf(memStore()), err: delErr}),
		Profiles: registry(t, unbuildableSpec()), DefaultAgent: unbuildableSpec().Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), host.RunRequest{Prompt: "x"})
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if !errors.Is(err, delErr) {
		t.Fatalf("cleanup failure not reported: %v", err)
	}
}

func TestUnpersistedFailedRunStillReportsSaveFailure(t *testing.T) {
	store := &failingSaveStore{parts: partsOf(memStore()), failFinal: true}
	model := testkit.NewFakeModel(testkit.Text("x"))
	modelErr := errors.New("model exploded")
	model.Err = modelErr
	runner, err := host.New(host.Config{
		Store:    asStore(store),
		Profiles: registry(t, profile.AgentProfile{Name: "boom", Model: model}), DefaultAgent: "boom",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), host.RunRequest{Prompt: "hi"})
	if !errors.Is(err, host.ErrNotPersisted) {
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
