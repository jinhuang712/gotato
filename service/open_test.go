package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/store"
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
	store := memStore()
	model := &gatedEcho{gate: make(chan struct{}), started: make(chan string, 8)}
	runner, err := service.New(service.Config{
		Store:    store,
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: model}), DefaultAgent: "echo",
		Bounds: service.Bounds{MaxActiveSessions: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	instance := instanceRecorder(t, runner)

	// An idle stored Session that another request will try to wake up.
	idle, err := runner.CreateSession(ctx, service.CreateSessionRequest{Agent: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	idleBefore, err := store.Records.Log(ctx, idle.ID, 0, 0)
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
	sessionsBefore, err := listAll(ctx, store)
	if err != nil {
		t.Fatal(err)
	}

	// A new Session, and the idle stored one, are refused: they did not run.
	refusals := []struct {
		name    string
		request service.RunRequest
	}{
		{"new session", service.RunRequest{Prompt: "three"}},
		{"idle stored Session", service.RunRequest{SessionID: idle.ID, Prompt: "wake"}},
	}
	for _, refusal := range refusals {
		result, err := runner.Run(ctx, refusal.request)
		if !errors.Is(err, service.ErrOverloaded) || !gotato.IsCode(err, service.CodeOverloaded) {
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
		if r, ok := e.(service.RunRejected); ok {
			seen[r.SessionID] = true
		}
		return len(seen) >= 2 && seen[gotato.SessionID(idle.ID)]
	}) {
		t.Fatalf("want two run_rejected Events, one for the idle Session; got %v", instance.Kinds())
	}
	var reasons []string
	for _, event := range instance.OfKind(service.EventKindRunRejected) {
		reasons = append(reasons, event.(service.RunRejected).Why)
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
	sessionsAfter, err := listAll(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionsAfter) != len(sessionsBefore) {
		t.Fatalf("sessions in the store: %d before the refusals, %d after", len(sessionsBefore), len(sessionsAfter))
	}
	idleAfter, err := store.Records.Log(ctx, idle.ID, 0, 0)
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
		loaded, err := store.Records.Load(ctx, summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stateOf(loaded).Busy {
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
	if _, err := runner.CreateSession(ctx, service.CreateSessionRequest{Agent: "echo"}); err != nil {
		t.Fatalf("CreateSession at the limit: %v", err)
	}

	// Once the held Runs end, the instance has room again.
	close(model.gate)
	for range 2 {
		select {
		case result := <-results:
			if result.Result.Status != gotato.RunStatusCompleted {
				t.Fatalf("held run = %+v", result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("held run did not end")
		}
	}
	if got := runner.ActiveSessions(); got != 0 {
		t.Fatalf("ActiveSessions after the Runs ended = %d, want 0", got)
	}
	if result, err := runner.Run(ctx, service.RunRequest{SessionID: idle.ID, Prompt: "now"}); err != nil || result.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Run after the limit eased = %+v, %v", result, err)
	}
}

// TestActivationLimitIsOffByDefault: without a limit, no number of Sessions is
// refused as overloaded.
func TestActivationLimitIsOffByDefault(t *testing.T) {
	runner, _ := newRunner(t, profile.AgentProfile{Name: "echo", Model: testkit.NewFakeModel(testkit.Text("ok"))})
	for range 5 {
		if _, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
}

// A negative limit is a mistake, not "off".
func TestActivationLimitMustNotBeNegative(t *testing.T) {
	_, err := service.New(service.Config{
		Store:    memStore(),
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}), DefaultAgent: "echo",
		Bounds: service.Bounds{MaxActiveSessions: -1},
	})
	if err == nil {
		t.Fatal("service.New accepted a negative MaxActiveSessions")
	}
}

// gatedCreate holds every Create until its gate opens.
type gatedCreate struct {
	parts
	gate chan struct{}
}

func (s *gatedCreate) Create(ctx context.Context, id string, entries []record.Entry, summary record.Header) error {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.parts.Create(ctx, id, entries, summary)
}

// A burst of Runs that each create a Session cannot go past the limit while
// the first one is still writing its Session: the Runs have no ID yet, but
// each is a Session of its own.
func TestActivationLimitHoldsForABurstOfCreatingRuns(t *testing.T) {
	store := &gatedCreate{parts: partsOf(memStore()), gate: make(chan struct{})}
	runner, err := service.New(service.Config{
		Store:    asStore(store),
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}), DefaultAgent: "echo",
		Bounds: service.Bounds{MaxActiveSessions: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 5)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
			errs <- err
		}()
	}
	refused := 0
	deadline := time.After(5 * time.Second)
	for refused < 4 {
		select {
		case err := <-errs:
			if !errors.Is(err, service.ErrOverloaded) {
				t.Fatalf("a Run ended with %v while the limit was held, want ErrOverloaded", err)
			}
			refused++
		case <-deadline:
			t.Fatalf("only %d of 4 Runs were refused; ActiveSessions = %d", refused, runner.ActiveSessions())
		}
	}
	if got := runner.ActiveSessions(); got != 1 {
		t.Fatalf("ActiveSessions = %d, want 1", got)
	}
	close(store.gate)
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatalf("the admitted Run: %v", err)
	}
}

// Runner.Sessions pages through the Sessions, and is not_supported over
// Records that do not list.
func TestRunnerSessionsPagesAndNeedsALister(t *testing.T) {
	ctx := context.Background()
	runner, _ := newRunner(t)
	for range 3 {
		if _, err := runner.CreateSession(ctx, service.CreateSessionRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := runner.Sessions(ctx, store.ListQuery{Limit: 2})
	if err != nil || len(first.Infos) != 2 || first.Next == "" {
		t.Fatalf("first page = %+v, %v; want 2 Sessions and a Next", first, err)
	}
	rest, err := runner.Sessions(ctx, store.ListQuery{Limit: 2, Cursor: first.Next})
	if err != nil || len(rest.Infos) != 1 || rest.Next != "" {
		t.Fatalf("second page = %+v, %v; want the last Session", rest, err)
	}
	if _, err := runner.Sessions(ctx, store.ListQuery{Cursor: "nonsense"}); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("a cursor the store did not make = %v, want invalid_argument", err)
	}

	m := store.NewMemoryStore()
	hidden, err := service.New(service.Config{
		Store:    store.Store{Records: struct{ store.Records }{m}, Liveness: m},
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}), DefaultAgent: "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hidden.Close(ctx) })
	if _, err := hidden.Sessions(ctx, store.ListQuery{}); !gotato.IsCode(err, gotato.CodeNotSupported) {
		t.Fatalf("Sessions over Records that do not list = %v, want not_supported", err)
	}
}
