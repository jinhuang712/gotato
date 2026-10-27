package service_test

import (
	"context"
	"slices"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// limitedInstance is an Instance over store that keeps at most limit Sessions,
// warm ones included, and queues a Run on a busy Session.
func limitedInstance(t *testing.T, store session.Store, clock service.Clock, model gotato.Model, limit int) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: "a", Clock: clock,
		Admission: service.Admission{MaxActiveSessions: limit, Queue: service.WaitWhileBusy},
		Agents:    []service.Agent{{Name: "echo", Model: model, Warm: service.WarmPolicy{NoIdleCompaction: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// drain empties a channel of the model's started prompts.
func drain(started chan string) {
	for len(started) > 0 {
		<-started
	}
}

// evictedIDs returns the Sessions the recorder saw leave memory as evicted, in
// order.
func evictedIDs(rec *testkit.EventRecorder) []string {
	var out []string
	for _, event := range rec.OfKind(gotato.KindSessionUnloaded) {
		if unloaded := event.(gotato.SessionUnloaded); unloaded.Why == "evicted" {
			out = append(out, string(unloaded.SessionID))
		}
	}
	return out
}

// The Phase 8 exit test for the memory limit: warm Sessions count toward
// MaxActiveSessions. At the limit a new Run evicts the longest-idle warm
// Session and runs; it is refused as not run only when every Session is
// running a Run.
func TestMemoryLimitEvictsTheLongestIdleWarmSessionAndRefusesOnlyWhenAllRun(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	model := &gatedEcho{gate: make(chan struct{}), started: make(chan string, 8)}
	runner := limitedInstance(t, session.NewMemoryStore(), clock, model, 2)
	instance := instanceRecorder(t, runner)

	// Two Sessions run and idle, one after the other: the first idles longest.
	close(model.gate)
	first := runOn(t, runner, "", "one")
	clock.Advance(time.Minute)
	second := runOn(t, runner, "", "two")
	clock.Advance(time.Minute)
	if runner.WarmSessions() != 2 {
		t.Fatalf("WarmSessions = %d, want 2", runner.WarmSessions())
	}

	// The instance is full of warm Sessions, and a new Run still runs.
	third := runOn(t, runner, "", "three")
	if got := awaitUnloads(t, instance, 1); !slices.Equal(got, []string{"evicted"}) {
		t.Fatalf("session_unloaded = %v, want [evicted]", got)
	}
	// The Instance stream is delivered after the Run returns.
	waitFor(t, func() bool { return len(evictedIDs(instance)) >= 1 })
	if got := evictedIDs(instance); !slices.Equal(got, []string{first.SessionID}) {
		t.Fatalf("evicted %v, want the longest-idle Session %q", got, first.SessionID)
	}
	if runner.WarmSessions() != 2 {
		t.Fatalf("WarmSessions = %d, want the second and the third", runner.WarmSessions())
	}

	// Every Session runs: hold the two Runs at the model.
	drain(model.started)
	model.gate = make(chan struct{})
	done := make(chan error, 2)
	for _, id := range []string{second.SessionID, third.SessionID} {
		go func() {
			_, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "again"})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-model.started:
		case <-time.After(5 * time.Second):
			t.Fatal("the model was not called twice")
		}
	}
	for _, request := range []service.RunRequest{{Prompt: "four"}, {SessionID: first.SessionID, Prompt: "wake"}} {
		_, err := runner.Run(ctx, request)
		if !gotato.IsCode(err, gotato.ErrOverloaded) || !gotato.NotRun(err) || !gotato.Retryable(err) {
			t.Fatalf("Run %+v with every Session running: %v, want overloaded, not run, retryable", request, err)
		}
	}
	close(model.gate)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// A running Session is never evicted: with one running and one warm, a new Run
// evicts the warm one.
func TestMemoryLimitNeverEvictsARunningSession(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	model := &gatedEcho{gate: make(chan struct{}), started: make(chan string, 8)}
	runner := limitedInstance(t, session.NewMemoryStore(), clock, model, 2)
	instance := instanceRecorder(t, runner)
	close(model.gate)
	idle := runOn(t, runner, "", "idle")
	drain(model.started)
	model.gate = make(chan struct{})
	done := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{Prompt: "busy"})
		done <- result
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the busy Run did not start")
	}
	// The busy Session and the idle one fill the instance.
	if runner.WarmSessions() != 1 || runner.ActiveSessions() != 1 {
		t.Fatalf("warm %d, active %d, want 1 and 1", runner.WarmSessions(), runner.ActiveSessions())
	}
	go func() { close(model.gate) }()
	runOn(t, runner, "", "new")
	busy := <-done
	if busy.Result.Status != gotato.RunCompleted {
		t.Fatalf("the running Session ended %+v", busy.Result)
	}
	// The Instance stream is delivered after the Run returns.
	waitFor(t, func() bool { return len(evictedIDs(instance)) >= 1 })
	if got := evictedIDs(instance); !slices.Equal(got, []string{idle.SessionID}) {
		t.Fatalf("evicted %v, want only the idle Session %q", got, idle.SessionID)
	}
}

// Eviction takes the Session idle longest, where a Run restarts the clock.
func TestMemoryLimitEvictsInOrderOfIdleTime(t *testing.T) {
	clock := newFakeClock()
	runner := limitedInstance(t, session.NewMemoryStore(), clock, testkit.EchoModel{}, 3)
	instance := instanceRecorder(t, runner)

	var ids []string
	for _, prompt := range []string{"a", "b", "c"} {
		ids = append(ids, runOn(t, runner, "", prompt).SessionID)
		clock.Advance(time.Minute)
	}
	// Touching the first makes the second the longest-idle.
	runOn(t, runner, ids[0], "again")
	clock.Advance(time.Minute)
	runOn(t, runner, "", "d")
	clock.Advance(time.Minute)
	runOn(t, runner, "", "e")

	waitFor(t, func() bool { return len(evictedIDs(instance)) >= 2 })
	if want, got := []string{ids[1], ids[2]}, evictedIDs(instance); !slices.Equal(got, want) {
		t.Fatalf("evicted %v, want %v (longest idle first)", got, want)
	}
	if runner.WarmSessions() != 3 {
		t.Fatalf("WarmSessions = %d, want 3", runner.WarmSessions())
	}
}

// An evicted Session loses nothing: its next Run loads it from the store, and
// its timers are gone with it.
func TestEvictedSessionReloadsFromTheStore(t *testing.T) {
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	runner := limitedInstance(t, store, clock, testkit.EchoModel{}, 1)

	first := runOn(t, runner, "", "one")
	second := runOn(t, runner, "", "two")
	if clock.Pending() != 1 {
		t.Fatalf("%d timers pending, want the TTL timer of the one warm Session", clock.Pending())
	}
	loads := store.Loads()
	again := runOn(t, runner, first.SessionID, "three")
	if store.Loads() != loads+1 {
		t.Fatalf("loads = %d, want one more than %d", store.Loads(), loads)
	}
	if again.Messages != 4 {
		t.Fatalf("the reloaded Session has %d messages, want 4", again.Messages)
	}
	if runner.WarmSessions() != 1 || clock.Pending() != 1 {
		t.Fatalf("warm %d, timers %d, want 1 and 1", runner.WarmSessions(), clock.Pending())
	}
	// The other Session is still whole in the store.
	if got := runOn(t, runner, second.SessionID, "four"); got.Messages != 4 {
		t.Fatalf("the evicted Session has %d messages, want 4", got.Messages)
	}
}

// Eviction cancels an idle compaction in progress, as a message does: the
// compaction leaves nothing half-written, is not retried, and the new Run goes
// on once it has unwound.
func TestEvictionCancelsAnIdleCompactionInProgress(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	model := &askModel{started: make(chan struct{}), reply: recapModel()}
	runner, err := service.New(service.Config{
		Store: store, Instance: "a", Clock: clock,
		Admission: service.Admission{MaxActiveSessions: 1},
		Agents: []service.Agent{{
			Name: "echo", Model: model,
			Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}},
			Warm:    service.WarmPolicy{IdleMinTokens: -1},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(ctx) })
	instance := instanceRecorder(t, runner)
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	runOn(t, runner, first.SessionID, "three")

	compacting := make(chan struct{})
	go func() {
		clock.Advance(55 * time.Minute)
		close(compacting)
	}()
	select {
	case <-model.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle compaction never asked for a Recap")
	}
	// A new Session needs the room the compacting one holds.
	runOn(t, runner, "", "other")
	select {
	case <-compacting:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle compaction did not end")
	}
	// The Instance stream is delivered after the Run returns.
	waitFor(t, func() bool { return len(evictedIDs(instance)) >= 1 })
	if got := evictedIDs(instance); !slices.Equal(got, []string{first.SessionID}) {
		t.Fatalf("evicted %v, want [%s]", got, first.SessionID)
	}
	kinds := storedKinds(t, store, first.SessionID)
	if count(kinds, gotato.KindCompactionStarted) != 1 || count(kinds, gotato.KindCompactionFailed) != 1 || count(kinds, gotato.KindSegmentClosed) != 0 {
		t.Fatalf("stored Events %v, want one cancelled compaction and no Segment", kinds)
	}
	if runner.WarmSessions() != 1 || clock.Pending() != 2 {
		t.Fatalf("warm %d, timers %d, want only the new Session's", runner.WarmSessions(), clock.Pending())
	}
	// The evicted Session is intact in the store.
	if got := runOn(t, runner, first.SessionID, "four"); got.Messages != 8 {
		t.Fatalf("the evicted Session has %d messages, want 8", got.Messages)
	}
}
