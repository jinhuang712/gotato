package service_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

var fastLiveness = service.Liveness{TTL: 150 * time.Millisecond, Wait: 3 * time.Second}

func TestIdleSessionMovesToAnyInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, runner := range []*service.Runner{a, b, a} {
		began := time.Now()
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: fmt.Sprintf("p%d", i)}); err != nil {
			t.Fatal(err)
		}
		// The holder is alive but idle, so nothing waits.
		if waited := time.Since(began); waited > steadyLiveness.Wait/2 {
			t.Fatalf("Run %d waited %v for an idle Session", i, waited)
		}
	}
	view, err := b.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := view.Claim(); !isInstance(claim.Instance, "instance-a") || claim.Epoch != 3 {
		t.Fatalf("claim = %+v, want instance-a at epoch 3", claim)
	}
	if got := len(view.Messages()); got != 6 {
		t.Fatalf("messages = %d, want 6", got)
	}
}

func TestBusyElsewhereWaitsThenRefuses(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	a := instanceRunner(t, store, "instance-a", held, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, service.Liveness{TTL: steadyLiveness.TTL, Wait: 200 * time.Millisecond})
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
		done <- err
	}()
	<-held.started

	began := time.Now()
	_, err = b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "meanwhile"})
	if !errors.Is(err, service.ErrBusy) {
		t.Fatalf("a Run on a Session running elsewhere = %v, want busy", err)
	}
	var runtimeErr *gotato.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Details["retry_after_ms"] == "" {
		t.Fatalf("busy error = %+v, want a retry_after_ms", runtimeErr)
	}
	if waited := time.Since(began); waited < 150*time.Millisecond {
		t.Fatalf("refused after %v, want a wait first", waited)
	}

	// Released within the wait, the other instance goes on.
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(held.gate)
	}()
	b2 := instanceRunner(t, store, "instance-c", testkit.EchoModel{}, steadyLiveness)
	if _, err := b2.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "after"}); err != nil {
		t.Fatalf("a Run that waited for the holder's Run = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	view, err := a.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, 4)
	for _, m := range view.Messages() {
		texts = append(texts, gotato.TextOf(m))
	}
	if strings.Join(texts, ",") != "slow,released,after,echo: after" {
		t.Fatalf("transcript = %v; the refused Run must leave nothing", texts)
	}
}

// pausableStore stops renewing Liveness rows while paused, as a frozen or
// cut-off instance would.
type pausableStore struct {
	session.Store
	paused atomic.Bool
}

func (s *pausableStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.paused.Load() {
		return errors.New("store unreachable")
	}
	return s.Store.Beat(ctx, row, ttl)
}

func TestLapsedHeartbeatFencesLateWrites(t *testing.T) {
	ctx := context.Background()
	backing := session.NewMemoryStore()
	paused := &pausableStore{Store: backing}
	model := testkit.NewFakeModel(testkit.Text("hi"))
	a := instanceRunner(t, paused, "instance-a", model, fastLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	paused.paused.Store(true)
	time.Sleep(2 * fastLiveness.TTL)

	// instance-a cannot show its row is alive: it writes nothing and never
	// calls the model.
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "late"}); !gotato.IsCode(err, gotato.CodeConflict) {
		t.Fatalf("a Run after the row lapsed = %v, want a conflict", err)
	}
	if len(model.Requests()) != 0 {
		t.Fatalf("the model was called %d times after the row lapsed", len(model.Requests()))
	}
	// Its row has expired, so another instance takes the Session at once.
	b := instanceRunner(t, backing, "instance-b", testkit.EchoModel{}, steadyLiveness)
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "taken over"}); err != nil {
		t.Fatal(err)
	}
}

var errKilled = errors.New("the instance was killed")

// TestManyInstancesOneWriter checks GOALS.md G-G08: several instances over one
// store take concurrent prompts for the same Sessions, sent to random
// instances, and one instance is killed partway. Every completed Run's prompt
// lands once, followed by its reply; nothing is duplicated or interleaved;
// and no instance calls the model for a Session it does not hold.
func TestManyInstancesOneWriter(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var open func() session.Store
			if kind == "memory" {
				backing := session.NewMemoryStore()
				open = func() session.Store { return backing }
			} else {
				dir := t.TempDir()
				open = func() session.Store {
					store, err := session.NewFileStore(dir)
					if err != nil {
						t.Fatal(err)
					}
					return store
				}
			}
			var violations, calls atomic.Int64
			const instances = 3
			runners := make([]*service.Runner, instances)
			stores := make([]*killableStore, instances)
			for i := range instances {
				stores[i] = &killableStore{Store: open()}
				model := ownerModel{instance: fmt.Sprintf("instance-%d", i), store: open(), dead: &stores[i].dead, violations: &violations, calls: &calls}
				// Only the instance that is killed has a row that must expire;
				// the others must not lapse on a late renewal.
				liveness := steadyLiveness
				if i == instances-1 {
					liveness = fastLiveness
				}
				runners[i] = instanceRunner(t, stores[i], model.instance, model, liveness)
			}
			var ids []string
			for range 2 {
				s, err := runners[0].CreateSession(ctx, "echo", nil)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, s.ID())
			}

			type outcome struct {
				prompt, session string
				err             error
			}
			outcomes := make(chan outcome, 64)
			var wg sync.WaitGroup
			for worker := range 6 {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					random := rand.New(rand.NewPCG(uint64(worker), 7))
					for k := range 5 {
						id := ids[random.IntN(len(ids))]
						prompt := fmt.Sprintf("%s/p-%d-%d", id, worker, k)
						runner := runners[random.IntN(instances)]
						_, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
						outcomes <- outcome{prompt: prompt, session: id, err: err}
					}
				}(worker)
			}
			time.Sleep(20 * time.Millisecond)
			stores[instances-1].dead.Store(true)
			wg.Wait()
			close(outcomes)

			completed := map[string]bool{}
			failures := map[string]int{}
			for o := range outcomes {
				if o.err == nil {
					completed[o.prompt] = true
					continue
				}
				var runtimeErr *gotato.RuntimeError
				switch {
				case errors.As(o.err, &runtimeErr):
					failures[string(runtimeErr.Code)]++
				default:
					failures[o.err.Error()]++
				}
			}
			t.Logf("failures: %v", failures)
			if len(completed) == 0 {
				t.Fatal("no Run completed")
			}
			if n := violations.Load(); n != 0 {
				t.Fatalf("%d of %d model calls came from an instance that did not hold the Session", n, calls.Load())
			}
			seen := map[string]int{}
			for _, id := range ids {
				view, err := session.View(ctx, open(), id)
				if err != nil {
					t.Fatal(err)
				}
				messages := view.Messages()
				for i, message := range messages {
					if message.Role != gotato.RoleUser {
						continue
					}
					prompt := gotato.TextOf(message)
					seen[prompt]++
					if !completed[prompt] {
						continue
					}
					if i+1 >= len(messages) || !strings.HasSuffix(gotato.TextOf(messages[i+1]), ": "+prompt) {
						t.Fatalf("the reply to %q does not follow it", prompt)
					}
				}
			}
			for prompt, n := range seen {
				if n > 1 {
					t.Fatalf("prompt %q landed %d times", prompt, n)
				}
			}
			for prompt := range completed {
				if seen[prompt] != 1 {
					t.Fatalf("completed prompt %q landed %d times", prompt, seen[prompt])
				}
			}
			t.Logf("%d of 30 Runs completed, %d model calls, one instance killed", len(completed), calls.Load())
		})
	}
}

// TestClosedRunnerNeverBeatsAgain: after Close removes its row, a Runner
// writes nothing, so its row never comes back.
func TestClosedRunnerNeverBeatsAgain(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "late"}); err == nil {
		t.Fatal("a closed Runner ran")
	}
	if _, err := a.CreateSessionExclusive(ctx, "echo", "after-close", nil); err == nil || errors.Is(err, service.ErrSessionExists) {
		t.Fatalf("a closed Runner creating a Session = %v, want a refusal that is not ErrSessionExists", err)
	}
	if state, err := store.Instance(ctx, a.Instance()); err != nil || state.Alive {
		t.Fatalf("instance-a after Close and a late write = %+v, %v; want not alive", state, err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatalf("a second Close = %v", err)
	}
}

// TestTwoInstancesOpenOneIdleSessionAtOnce: when the instance that holds an
// idle Session and another one open it at the same time, one of them runs
// first and the other waits for it. Neither fails with a conflict.
//
// The Liveness TTL is long here. A Runner refuses a write once its last
// renewal is older than 4/5 of the TTL (Runner.guard), which is right, and
// with fastLiveness's 150 ms that is 120 ms: a renewal tick that a loaded
// machine runs 40 to 80 ms late, with the file store's disk writes, passes
// it, and a Run then fails with "liveness row may have expired". This test
// is about two Instances opening a Session, not about liveness lapsing, which
// the tests that use fastLiveness cover.
func TestTwoInstancesOpenOneIdleSessionAtOnce(t *testing.T) {
	liveness := service.Liveness{TTL: 3 * time.Second, Wait: 5 * time.Second}
	for _, kind := range []struct {
		name   string
		rounds int
	}{{"memory", 50}, {"file", 15}} {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			open := sharedStores(t, kind.name)
			a := instanceRunner(t, open(), "instance-a", testkit.EchoModel{}, liveness)
			b := instanceRunner(t, open(), "instance-b", testkit.EchoModel{}, liveness)
			s, err := a.CreateSession(ctx, "echo", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}); err != nil {
				t.Fatal(err)
			}
			var failed []error
			for round := range kind.rounds {
				errs := make(chan error, 2)
				for _, runner := range []*service.Runner{a, b} {
					go func() {
						_, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: fmt.Sprintf("%s-%d", runner.Instance(), round)})
						errs <- err
					}()
				}
				for range 2 {
					if err := <-errs; err != nil {
						failed = append(failed, err)
					}
				}
			}
			if len(failed) > 0 {
				t.Fatalf("%d of %d Runs failed, first: %v", len(failed), 2*kind.rounds, failed[0])
			}
			view, err := session.View(ctx, open(), s.ID())
			if err != nil {
				t.Fatal(err)
			}
			if got, want := len(view.Messages()), 2+4*kind.rounds; got != want {
				t.Fatalf("messages = %d, want %d", got, want)
			}
		})
	}
}

// A Run whose Record is deleted, and created again at the same length, under
// it, fails and writes nothing into the new Record.
func TestRunOfADeletedSessionNeverWritesToTheSessionCreatedAgain(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	a := instanceRunner(t, store, "instance-a", held, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
		done <- err
	}()
	<-held.started
	old, err := store.Load(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}

	// Another process deletes the Record and creates it again, as long as the
	// old one is now.
	if err := store.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	fresh, err := session.CreateWriter(ctx, store, session.New(session.WithID(s.ID())), "other", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	for {
		loaded, err := store.Load(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Head >= old.Head {
			if loaded.Head != old.Head {
				t.Skipf("the new Record cannot be made exactly %d long", old.Head)
			}
			break
		}
		commit(t, fresh, gotato.UserMessage("x"))
	}
	before, err := store.Log(ctx, s.ID(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	close(held.gate)
	if err := <-done; err == nil {
		t.Fatal("the Run of the deleted Session succeeded")
	}
	after, err := store.Log(ctx, s.ID(), 0, 0)
	if err != nil || len(after) != len(before) {
		t.Fatalf("the new Record holds %d Entries after the Run (%v), want %d", len(after), err, len(before))
	}
	loaded, err := store.Load(ctx, s.ID())
	if err != nil || loaded.Info.RecordEpoch != fresh.Record().RecordEpoch() {
		t.Fatalf("the new Record's epoch = %q (%v), want %q", loaded.Info.RecordEpoch, err, fresh.Record().RecordEpoch())
	}
}

func commit(t *testing.T, w *session.Writer, message gotato.Message) {
	t.Helper()
	if err := w.Append(message); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(context.Background(), []gotato.Event{gotato.MessageCommitted{Header: gotato.NewHeader("", "", 0), Message: message, Source: gotato.MessageSourcePrompt}}); err != nil {
		t.Fatal(err)
	}
}

// The first Run of a new Session writes session_claimed (new_session) before
// the Run's own Events; a Run on an existing Session claims it again (idle),
// and the end of that Run writes session_released.
func TestRunWritesClaimAndRelease(t *testing.T) {
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if claims := claimsOf(t, a, first.SessionID); len(claims) != 1 || claims[0].Why != gotato.ClaimWhyNewSession ||
		!isInstance(claims[0].Instance, "instance-a") || claims[0].Epoch != 1 || claims[0].From != "" {
		t.Fatalf("claims = %+v, want one: instance-a at epoch 1 for a new_session", claims)
	}
	// The first Run of a new Session releases it too: it is busy from its
	// creation until then.
	if firstRecord, err := a.Record(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	} else if events := firstRecord.Events(); events[len(events)-1].Kind() != gotato.EventKindSessionReleased {
		t.Fatalf("the first Run left the Record at %v, want session_released last", numbered(events))
	}
	result, err := a.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.Record(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	events := record.Events()
	claimed, started, ended, released := -1, -1, -1, -1
	for i, event := range events {
		switch event.Kind() {
		case gotato.EventKindSessionClaimed:
			claimed = i
		case gotato.EventKindRunStarted:
			started = i
		case gotato.EventKindRunEnded:
			ended = i
		case gotato.EventKindSessionReleased:
			released = i
		}
	}
	if claimed < 0 || started < 0 || ended < 0 || released < 0 || claimed >= started || started >= ended || ended >= released {
		t.Fatalf("Events = %v, want session_claimed, run_started, run_ended, session_released in that order", numbered(events))
	}
	claim := events[claimed].(gotato.SessionClaimed)
	if claim.Why != gotato.ClaimWhyIdle || !isInstance(claim.Instance, "instance-a") || claim.Epoch != 1 {
		t.Fatalf("claim = %+v, want instance-a at epoch 1, why idle", claim)
	}
	release := events[released].(gotato.SessionReleased)
	if !isInstance(release.Instance, "instance-a") || release.Epoch != 1 {
		t.Fatalf("release = %+v, want instance-a at epoch 1", release)
	}
	if released != len(events)-1 {
		t.Fatalf("the Record goes on after session_released: %v", numbered(events))
	}
}

// Another Instance that takes over an idle Session writes session_claimed at
// the next Epoch and names the Instance it took the Session from.
func TestTakeoverWritesClaimWithNextEpochAndFrom(t *testing.T) {
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	claims := claimsOf(t, b, first.SessionID)
	if len(claims) != 2 {
		t.Fatalf("claims = %+v, want two", claims)
	}
	if !isInstance(claims[0].Instance, "instance-a") || claims[0].Epoch != 1 || claims[0].Why != gotato.ClaimWhyNewSession {
		t.Fatalf("first claim = %+v", claims[0])
	}
	if got := claims[1]; !isInstance(got.Instance, "instance-b") || got.Epoch != 2 || !isInstance(got.From, "instance-a") || got.Why != gotato.ClaimWhyIdle {
		t.Fatalf("takeover claim = %+v, want instance-b at epoch 2, from instance-a, why idle", got)
	}
}

// deadHolders makes an Instance see every other Instance as not alive, as if
// their Liveness rows had expired, so it takes over a Session that is busy.
type deadHolders struct {
	session.Store
	self string
}

func (s deadHolders) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if !isInstance(id, s.self) {
		return session.InstanceState{ID: id}, nil
	}
	return s.Store.Instance(ctx, id)
}

// A write the store refuses because another Instance took the Claim is
// published as a session_fenced Instance Event, and the fenced Instance writes
// nothing more.
func TestFencedWritePublishesSessionFenced(t *testing.T) {
	ctx := context.Background()
	backing := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	a := instanceRunner(t, backing, "instance-a", held, steadyLiveness)
	b := instanceRunner(t, deadHolders{Store: backing, self: "instance-b"}, "instance-b", testkit.EchoModel{}, steadyLiveness)
	fenced := instanceRecorder(t, a, gotato.SubscribeFilter(func(event gotato.Event) bool {
		return event.Kind() == gotato.EventKindSessionFenced
	}))
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
		done <- err
	}()
	<-held.started
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "taken"}); err != nil {
		t.Fatalf("instance-b taking over the Session = %v", err)
	}
	close(held.gate)
	if err := <-done; err == nil {
		t.Fatal("the fenced Run succeeded")
	}
	if !fenced.Wait(5*time.Second, func(event gotato.Event) bool { return true }) {
		t.Fatal("no session_fenced was published to instance-a's Instance stream")
	}
	event, ok := fenced.Events()[0].(gotato.SessionFenced)
	if !ok || !isInstance(event.Instance, "instance-a") || string(event.SessionID) != s.ID() {
		t.Fatalf("event = %+v, want session_fenced by instance-a for %s", fenced.Events()[0], s.ID())
	}
	// The fenced Instance wrote nothing after the takeover: its reply is not
	// in the Record.
	record, err := b.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range record.Messages() {
		if gotato.TextOf(message) == "released" {
			t.Fatalf("the fenced Instance's reply is in the Record: %v", record.Messages())
		}
	}
}

// A Liveness row that lapsed publishes liveness_lapsed once, however many
// writes are then refused.
func TestLapsedLivenessPublishesOnce(t *testing.T) {
	ctx := context.Background()
	backing := session.NewMemoryStore()
	paused := &pausableStore{Store: backing}
	a := instanceRunner(t, paused, "instance-a", testkit.EchoModel{}, fastLiveness)
	lapsed := instanceRecorder(t, a, gotato.SubscribeFilter(func(event gotato.Event) bool {
		return event.Kind() == gotato.EventKindLivenessLapsed
	}))
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	paused.paused.Store(true)
	time.Sleep(2 * fastLiveness.TTL)
	var refused atomic.Int32
	for range 3 {
		if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "late"}); err != nil && !errors.Is(err, context.Canceled) {
			refused.Add(1)
		}
	}
	if refused.Load() != 3 {
		t.Fatalf("%d of 3 late Runs were refused", refused.Load())
	}
	if !lapsed.Wait(5*time.Second, func(gotato.Event) bool { return true }) {
		t.Fatal("no liveness_lapsed was published")
	}
	time.Sleep(100 * time.Millisecond)
	events := lapsed.Events()
	if len(events) != 1 {
		t.Fatalf("liveness_lapsed was published %d times, want once", len(events))
	}
	if event, ok := events[0].(gotato.LivenessLapsed); !ok || !isInstance(event.Instance, "instance-a") {
		t.Fatalf("event = %+v, want liveness_lapsed of instance-a", events[0])
	}
}
