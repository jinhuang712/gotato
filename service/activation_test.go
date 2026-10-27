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

// TestActivationLimitRefusesANewSessionBeforeItRuns is the Phase 5 exit test
// for the activation limit: an instance filled to its hard limit refuses a new
// Session with a refusal that says it did not run, emits a run_rejected
// Instance Event with the reason, and activates nothing: no Session is
// created, an idle stored Session is not claimed, and the model is not called.
// A Run on a Session that is active already adds no activation, so the limit
// does not touch it.
func TestActivationLimitRefusesANewSessionBeforeItRuns(t *testing.T) {
	store := session.NewMemoryStore()
	model := &gatedEcho{gate: make(chan struct{}), started: make(chan string, 8)}
	runner, err := service.New(service.Config{
		Store:     store,
		Agents:    []service.Agent{{Name: "echo", Model: model}},
		Admission: service.Admission{MaxActiveSessions: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	instance := instanceRecorder(t, runner)

	// An idle stored Session that another request will try to wake up.
	idle, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	idleBefore, err := store.Log(ctx, idle.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Fill the instance: two Runs on two new Sessions, held by the model.
	results := make(chan service.RunResponse, 2)
	for _, prompt := range []string{"one", "two"} {
		go func() {
			result, _ := runner.Run(ctx, service.RunRequest{Prompt: prompt})
			results <- result
		}()
	}
	for range 2 {
		select {
		case <-model.started:
		case <-time.After(5 * time.Second):
			t.Fatal("the model was not called twice")
		}
	}
	if got := runner.ActiveSessions(); got != 2 {
		t.Fatalf("ActiveSessions = %d, want 2", got)
	}
	sessionsBefore, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A new Session, and the idle stored one, are refused: they did not run.
	refusals := []struct {
		name    string
		request service.RunRequest
	}{
		{"new session", service.RunRequest{Prompt: "three"}},
		{"idle stored Session", service.RunRequest{SessionID: idle.ID(), Prompt: "wake"}},
	}
	for _, refusal := range refusals {
		result, err := runner.Run(ctx, refusal.request)
		if !errors.Is(err, service.ErrOverloaded) || !gotato.IsCode(err, gotato.CodeOverloaded) {
			t.Fatalf("%s: err = %v, want ErrOverloaded", refusal.name, err)
		}
		if result.SessionID != "" || result.Result.RunID != "" {
			t.Fatalf("%s: a refused request returned a result: %+v", refusal.name, result)
		}
	}
	// The Instance Events reach the recorder on their own goroutine: wait for
	// both before counting them.
	// The two refusals differ by Session: the new one names none. Wait is
	// called again on the same Events, so count by what they name.
	seen := map[gotato.SessionID]bool{}
	if !instance.Wait(5*time.Second, func(e gotato.Event) bool {
		if r, ok := e.(gotato.RunRejected); ok {
			seen[r.SessionID] = true
		}
		return len(seen) >= 2 && seen[gotato.SessionID(idle.ID())]
	}) {
		t.Fatalf("want two run_rejected Events, one for the idle Session; got %v", instance.Kinds())
	}
	var reasons []string
	for _, event := range instance.OfKind(gotato.KindRunRejected) {
		reasons = append(reasons, event.(gotato.RunRejected).Why)
	}
	if len(reasons) != 2 || reasons[0] != "activation_limit" || reasons[1] != "activation_limit" {
		t.Fatalf("run_rejected reasons = %v, want two activation_limit", reasons)
	}

	// Nothing was activated: the model was not called, no Session was
	// created, the idle one was not claimed, and the instance is as full as before.
	select {
	case prompt := <-model.started:
		t.Fatalf("the model was called for %q after the limit was reached", prompt)
	default:
	}
	sessionsAfter, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionsAfter) != len(sessionsBefore) {
		t.Fatalf("sessions in the store: %d before the refusals, %d after", len(sessionsBefore), len(sessionsAfter))
	}
	idleAfter, err := store.Log(ctx, idle.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(idleAfter) != len(idleBefore) {
		t.Fatalf("the idle Session grew from %d to %d Entries: something activated it", len(idleBefore), len(idleAfter))
	}
	if got := runner.ActiveSessions(); got != 2 {
		t.Fatalf("ActiveSessions after refusals = %d, want 2", got)
	}

	// A Run on a Session that is active adds no activation: it reaches the
	// busy policy, which rejects it as busy, not as overloaded.
	var active string
	for _, summary := range sessionsBefore {
		if summary.Busy {
			active = summary.ID
			break
		}
	}
	if active == "" {
		t.Fatal("no busy Session in the store")
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: active, Prompt: "again"}); !errors.Is(err, service.ErrBusy) {
		t.Fatalf("Run on an active Session: err = %v, want ErrBusy", err)
	}

	// Creating a Session activates nothing, so it still works at the limit.
	if _, err := runner.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatalf("CreateSession at the limit: %v", err)
	}

	// Once the held Runs end, the instance has room again.
	close(model.gate)
	for range 2 {
		select {
		case result := <-results:
			if result.Result.Status != gotato.RunCompleted {
				t.Fatalf("held run = %+v", result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("held run did not end")
		}
	}
	if got := runner.ActiveSessions(); got != 0 {
		t.Fatalf("ActiveSessions after the Runs ended = %d, want 0", got)
	}
	if result, err := runner.Run(ctx, service.RunRequest{SessionID: idle.ID(), Prompt: "now"}); err != nil || result.Result.Status != gotato.RunCompleted {
		t.Fatalf("Run after the limit eased = %+v, %v", result, err)
	}
}

// TestActivationLimitIsOffByDefault: without a limit, no number of Sessions is
// refused as overloaded.
func TestActivationLimitIsOffByDefault(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: testkit.NewFakeModel(testkit.Text("ok"))})
	for range 5 {
		if _, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
}

// A negative limit is a mistake, not "off".
func TestActivationLimitMustNotBeNegative(t *testing.T) {
	_, err := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
		Admission: service.Admission{MaxActiveSessions: -1},
	})
	if err == nil {
		t.Fatal("service.New accepted a negative MaxActiveSessions")
	}
}
