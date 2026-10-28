package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// gatedEcho answers "done: <prompt>" once its gate opens, and reports each
// call as it starts. A call whose context ends first fails with it.
type gatedEcho struct {
	gate    chan struct{}
	started chan string
}

func (m *gatedEcho) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	prompt := gotato.TextOf(request.Messages[len(request.Messages)-1])
	m.started <- prompt
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.NewFakeModel(testkit.Text("done: "+prompt)).Stream(ctx, gotato.ModelRequest{})
}

// sharedStores returns a function that opens handles on one new store of the
// given kind, as several instances share one database.
func sharedStores(t *testing.T, kind string) func() session.Store {
	t.Helper()
	if kind == "memory" {
		backing := session.NewMemoryStore()
		return func() session.Store { return backing }
	}
	dir := t.TempDir()
	return func() session.Store {
		store, err := session.NewFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
}

// checkReleased fails unless instance released the Session after its last
// Run ended, and the store no longer marks it busy.
func checkReleased(t *testing.T, store session.Store, id, instance string) {
	t.Helper()
	ctx := context.Background()
	entries, err := store.Log(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runEnd, release session.Entry
	for _, entry := range entries {
		switch entry.Kind {
		case gotato.EventKindRunEnded:
			runEnd = entry
		case gotato.EventKindSessionReleased:
			release = entry
		}
	}
	if runEnd.Seq == 0 || release.Seq < runEnd.Seq || !isInstance(release.Instance, instance) {
		t.Fatalf("Session %s: run_ended at %d, release at %d by %q; want a release by %s after the Run", id, runEnd.Seq, release.Seq, release.Instance, instance)
	}
	if loaded, err := store.Load(ctx, id); err != nil || loaded.Info.Busy || loaded.Info.Running != "" {
		t.Fatalf("Session %s summary = %+v, %v; want idle", id, loaded.Info, err)
	}
}

// TestDrainCommitsRunsAndReleasesClaims is the Phase 5 exit test: a drained
// instance commits its Runs and releases its Claims before it leaves. While
// it drains, its Liveness row says so, it takes no new Session, and another
// instance still waits for a Session it is using.
func TestDrainCommitsRunsAndReleasesClaims(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			open := sharedStores(t, kind)
			model := &gatedEcho{gate: make(chan struct{}), started: make(chan string, 8)}
			openGate := sync.OnceFunc(func() { close(model.gate) })
			a := instanceRunner(t, open(), "instance-a", model, steadyLiveness)
			t.Cleanup(openGate)
			b := instanceRunner(t, open(), "instance-b", testkit.EchoModel{}, service.Liveness{TTL: steadyLiveness.TTL, Wait: 100 * time.Millisecond})

			const runs = 3
			ids := make([]string, runs)
			results := make([]chan error, runs)
			for i := range runs {
				s, err := a.CreateSession(ctx, "echo", nil)
				if err != nil {
					t.Fatal(err)
				}
				ids[i] = s.ID()
				results[i] = make(chan error, 1)
				go func() {
					result, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: fmt.Sprintf("p%d", i)})
					if err == nil && (result.Result.Status != gotato.RunStatusCompleted || result.Reconnect != nil) {
						err = fmt.Errorf("result = %+v, want completed and no reconnect", result)
					}
					results[i] <- err
				}()
			}
			for range runs {
				<-model.started
			}

			drained := make(chan error, 1)
			drainCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			go func() { drained <- a.Drain(drainCtx) }()
			waitFor(t, a.Draining)

			// The row says draining, so every instance can see it. Drain sets
			// its own flag first and then writes the row, so wait for the row.
			var state session.InstanceState
			waitFor(t, func() bool {
				var err error
				state, err = open().Instance(ctx, a.Instance())
				return err == nil && state.Alive && state.Draining
			})
			if !state.Alive || !state.Draining {
				t.Fatalf("instance-a while draining = %+v; want alive and draining", state)
			}
			// It takes no new Session: no new Run, and no new Claim.
			if _, err := a.Run(ctx, service.RunRequest{Prompt: "new"}); !errors.Is(err, service.ErrDraining) {
				t.Fatalf("a new Run while draining = %v, want ErrDraining", err)
			}
			// Creating or forking a Session writes the store and activates
			// nothing, so it still works.
			if _, err := a.CreateSessionExclusive(ctx, "echo", "created-"+kind, nil); err != nil {
				t.Fatalf("a new Session while draining = %v", err)
			}
			if _, err := a.Fork(ctx, ids[0], "forked-"+kind); err != nil {
				t.Fatalf("a fork while draining = %v", err)
			}
			other, err := b.CreateSession(ctx, "echo", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Run(ctx, service.RunRequest{SessionID: other.ID(), Prompt: "claim"}); err == nil {
				t.Fatal("a draining instance claimed a Session another instance holds")
			}
			// Another instance waits for a Session the draining one is using,
			// instead of taking it over mid-Run.
			if _, err := b.Run(ctx, service.RunRequest{SessionID: ids[0], Prompt: "meanwhile"}); !errors.Is(err, service.ErrBusy) {
				t.Fatalf("a Run on a Session the draining instance is using = %v, want busy", err)
			}

			openGate()
			if err := <-drained; err != nil {
				t.Fatalf("drain = %v", err)
			}
			for i := range runs {
				if err := <-results[i]; err != nil {
					t.Fatalf("Run %d = %v", i, err)
				}
			}

			// Every Run is committed, and its Claim released, before the
			// instance leaves: its row is still alive, yet another instance
			// claims each Session without waiting.
			for i, id := range ids {
				view, err := session.View(ctx, open(), id)
				if err != nil {
					t.Fatal(err)
				}
				messages := view.Messages()
				if len(messages) != 2 || gotato.TextOf(messages[1]) != fmt.Sprintf("done: p%d", i) {
					t.Fatalf("Session %d = %d messages, want the prompt and its reply", i, len(messages))
				}
				if runs := view.Runs(); len(runs) != 1 || runs[0].Status != gotato.RunStatusCompleted {
					t.Fatalf("Session %d runs = %+v, want one completed", i, runs)
				}
				checkReleased(t, open(), id, "instance-a")
			}
			if state, err := open().Instance(ctx, a.Instance()); err != nil || !state.Alive {
				t.Fatalf("instance-a after its drain = %+v, %v; want still alive", state, err)
			}
			for _, id := range ids {
				if _, err := b.Run(ctx, service.RunRequest{SessionID: id, Prompt: "next"}); err != nil {
					t.Fatalf("another instance after the drain = %v, want the Session at once", err)
				}
			}

			// It leaves: Close removes its row, and it writes nothing more.
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if state, err := open().Instance(ctx, a.Instance()); err != nil || state.Alive {
				t.Fatalf("instance-a after Close = %+v, %v; want not alive", state, err)
			}
		})
	}
}

// TestDrainDeadlineStopsRunsAndSaysReconnect: what a drain cannot finish by
// its deadline is stopped, saved and released, and each stopped Run returns
// with a reconnect hint only after every Run of the drain is released.
func TestDrainDeadlineStopsRunsAndSaysReconnect(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := &gatedEcho{gate: make(chan struct{}), started: make(chan string, 8)}
	defer close(model.gate)
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)

	const runs = 2
	ids := make([]string, runs)
	for i := range runs {
		s, err := a.CreateSession(ctx, "echo", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = s.ID()
	}
	type outcome struct {
		result   service.RunResponse
		busyLeft []string
	}
	outcomes := make(chan outcome, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _ := a.Run(ctx, service.RunRequest{SessionID: ids[i], Prompt: fmt.Sprintf("p%d", i)})
			// A stopped Run returns once every Session of the drain is
			// released.
			var busy []string
			for _, id := range ids {
				if loaded, err := store.Load(ctx, id); err != nil || loaded.Info.Busy {
					busy = append(busy, id)
				}
			}
			outcomes <- outcome{result: result, busyLeft: busy}
		}()
	}
	for range runs {
		<-model.started
	}
	drainCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := a.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain = %v, want its deadline", err)
	}
	wg.Wait()
	close(outcomes)
	for o := range outcomes {
		if o.result.Result.Status != gotato.RunStatusCanceled || o.result.Result.RunID == "" {
			t.Fatalf("a Run past the deadline = %+v, want a canceled Run", o.result.Result)
		}
		if o.result.Reconnect == nil || o.result.Reconnect.RetryAfterMS <= 0 {
			t.Fatalf("reconnect = %+v, want a delay", o.result.Reconnect)
		}
		if len(o.busyLeft) > 0 {
			t.Fatalf("a stopped Run returned while %v were still busy", o.busyLeft)
		}
	}
	for _, id := range ids {
		view, err := session.View(ctx, store, id)
		if err != nil {
			t.Fatal(err)
		}
		if runs := view.Runs(); len(runs) != 1 || runs[0].Status != gotato.RunStatusCanceled {
			t.Fatalf("runs = %+v, want one canceled", runs)
		}
		checkReleased(t, store, id, "instance-a")
	}
}

func TestCapacityAndDrain(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	store := session.NewMemoryStore()
	runner, _ := service.New(service.Config{
		Store:     store,
		Agents:    []service.Agent{{Name: "slow", Model: model}},
		Admission: service.Admission{MaxActiveRuns: 1},
	})
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(ctx, service.RunRequest{Prompt: "first"})
	}()
	waitFor(t, func() bool { return runner.ActiveRuns() == 1 })
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "second"}); !errors.Is(err, service.ErrCapacity) {
		t.Fatalf("capacity err = %v", err)
	}
	drainCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := runner.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain err = %v, want deadline (run was canceled after grace)", err)
	}
	<-done
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "after"}); !errors.Is(err, service.ErrDraining) {
		t.Fatalf("after drain err = %v", err)
	}
}

func TestDrainCancelsQueuedRun(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, _ := service.New(service.Config{
		Store:     session.NewMemoryStore(),
		Agents:    []service.Agent{{Name: "slow", Model: model}},
		Admission: service.Admission{Queue: service.QueuePolicyWait, MaxActiveRuns: 4},
	})
	ctx := context.Background()
	s, _ := runner.CreateSession(ctx, "slow", nil)

	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}) }()
	waitFor(t, func() bool { return model.Calls() == 1 })

	queued := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"})
		queued <- result
	}()
	waitFor(t, func() bool { return runner.ActiveRuns() == 2 })

	drainCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := runner.Drain(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain err = %v, want deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain waited %v for a queued run", elapsed)
	}
	select {
	case result := <-queued:
		if result.Result.Status != gotato.RunStatusCanceled {
			t.Fatalf("queued status = %q, want canceled", result.Result.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain left the queued run running")
	}
	close(block)
}

// Drain "finishes every Run in flight, saved and released", but
// only Runs are counted (inflight). A Compact (and a Notice, a SetToolActive, a
// Delete) holds the Session's Claim without being admitted, so Drain returns
// nil while it is still going; Close then removes the Liveness row and the
// compaction's own write is refused. A deploy loses the compaction it was
// asked to wait for.
func TestDrainMustWaitForACompactionInFlight(t *testing.T) {
	store := newKindGate(session.NewMemoryStore(), gotato.EventKindCompactionEnded)
	hold := store.holds[gotato.EventKindCompactionEnded]
	runner := newRunnerOn(t, store, service.Agent{Name: "a", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"one", "two", "three"} {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: p}); err != nil {
			t.Fatal(err)
		}
	}
	compacted := make(chan error, 1)
	go func() {
		_, err := runner.Compact(ctx, s.ID(), modelctx.CompactConfig{KeepMessages: 1})
		compacted <- err
	}()
	waitChan(t, "the compaction's write", hold.entered)
	drained := make(chan error, 1)
	go func() { drained <- runner.Drain(ctx) }()
	select {
	case err := <-drained:
		loaded, loadErr := store.Load(ctx, s.ID())
		t.Errorf("Drain returned (%v) while a compaction still holds the Claim (busy=%v, load error %v)", err, loaded.Info.Busy, loadErr)
	case <-time.After(300 * time.Millisecond):
	}
	close(hold.proceed)
	if err := <-compacted; err != nil {
		t.Errorf("Compact = %v", err)
	}
}

// slowBeatStore holds every Liveness write while hold is set, until the
// write's context ends, as a slow store would.
type slowBeatStore struct {
	session.Store
	hold atomic.Bool
}

func (s *slowBeatStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.hold.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Store.Beat(ctx, row, ttl)
}

// Drain says it drains before it writes the row, so a slow store does not
// leave a window in which Runs are admitted on an Instance the others already
// see leaving.
func TestDrainMarksDrainingBeforeTheRowIsWritten(t *testing.T) {
	ctx := context.Background()
	store := &slowBeatStore{Store: session.NewMemoryStore()}
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, service.Liveness{TTL: 3 * time.Second, Wait: time.Second})
	t.Cleanup(func() { store.hold.Store(false) })
	if _, err := a.CreateSession(ctx, "echo", nil); err != nil {
		t.Fatal(err)
	}
	store.hold.Store(true)
	done := make(chan error, 1)
	go func() { done <- a.Drain(ctx) }()
	deadline := time.Now().Add(400 * time.Millisecond)
	for !a.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the Runner is not draining while the write of its row is on its way")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := a.Run(ctx, service.RunRequest{Prompt: "new"}); !errors.Is(err, service.ErrDraining) {
		t.Fatalf("a Run while the row is being written = %v, want ErrDraining", err)
	}
	store.hold.Store(false)
	<-done
}

// A tool that ignores its context holds a Run, and Drain after its ctx ended
// waits for the Run only a bounded time.
func TestDrainReturnsAfterItsContextEvenForAToolThatIgnoresIt(t *testing.T) {
	ctx := context.Background()
	stuck := make(chan struct{})
	started := make(chan struct{})
	tool, err := gotato.NewFuncTool("stuck", "ignores its context", func(context.Context, struct{}) (string, error) {
		close(started)
		<-stuck
		return "late", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "stuck", Arguments: []byte(`{}`)}), testkit.Text("ok"))
	a, err := service.New(service.Config{
		Store: session.NewMemoryStore(), Liveness: steadyLiveness,
		Agents: []service.Agent{{Name: "echo", Model: model, Tools: []gotato.Tool{tool}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(stuck); _ = a.Close(ctx) })
	a.SetDrainStopWait(300 * time.Millisecond)
	go func() { _, _ = a.Run(ctx, service.RunRequest{Prompt: "go"}) }()
	<-started

	drainCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	err = a.Drain(drainCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want its context's error", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("Drain took %v after its context ended; a tool that ignores its context must not hold it", took)
	}
}

// A draining Runner does not claim an idle Session for a compaction.
func TestCompactRefusesWhileDraining(t *testing.T) {
	ctx := context.Background()
	a := instanceRunner(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = a.Compact(ctx, s.ID(), modelctx.CompactConfig{})
	if !errors.Is(err, service.ErrDraining) || !gotato.NotRun(err) {
		t.Fatalf("Compact while draining = %v, want ErrDraining, not run", err)
	}
}

// Drain has a bound of its own: a Run still going when Config.DrainTimeout is
// over is stopped, with a ctx that has no deadline.
func TestDrainHasATimeoutOfItsOwn(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	block := make(chan struct{})
	model.Block = block
	t.Cleanup(func() { close(block) })
	a, err := service.New(service.Config{
		Store: session.NewMemoryStore(), Liveness: steadyLiveness, DrainTimeout: 200 * time.Millisecond,
		Agents: []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(ctx) })
	result := make(chan service.RunResponse, 1)
	go func() {
		response, _ := a.Run(ctx, service.RunRequest{Prompt: "go"})
		result <- response
	}()
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	began := time.Now()
	err = a.Drain(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want the timeout", err)
	}
	if took := time.Since(began); took < 150*time.Millisecond || took > 5*time.Second {
		t.Fatalf("Drain took %v, want about its 200ms timeout", took)
	}
	if response := <-result; response.Reconnect == nil {
		t.Fatalf("the Run the drain stopped = %+v, want a reconnect", response)
	}
}

func TestDrainTimeoutDefaultsToTenSeconds(t *testing.T) {
	if service.DefaultDrainTimeout != 10*time.Second {
		t.Fatalf("DefaultDrainTimeout = %v, want 10s", service.DefaultDrainTimeout)
	}
}

// stubbornModel blocks in Stream until released, whatever its context says.
type stubbornModel struct {
	started chan struct{}
	release chan struct{}
}

func (m *stubbornModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	close(m.started)
	<-m.release
	return testkit.NewFakeModel(testkit.Text("late")).Stream(context.Background(), gotato.ModelRequest{})
}

// A model call that ignores its context keeps the Run going after Drain
// stopped it; Drain still returns after the stop wait.
func TestDrainReturnsAfterTheStopWaitForAModelThatIgnoresItsContext(t *testing.T) {
	ctx := context.Background()
	model := &stubbornModel{started: make(chan struct{}), release: make(chan struct{})}
	a, err := service.New(service.Config{
		Store: session.NewMemoryStore(), Liveness: steadyLiveness,
		Agents: []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(model.release); _ = a.Close(ctx) })
	a.SetDrainStopWait(300 * time.Millisecond)
	go func() { _, _ = a.Run(ctx, service.RunRequest{Prompt: "go"}) }()
	<-model.started

	drainCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Drain(drainCtx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Drain = %v, want its context's error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Drain is held by a model call that ignores its context")
	}
}
