package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
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
	entries, err := store.Log(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runEnd, release session.Entry
	for _, entry := range entries {
		switch entry.Kind {
		case gotato.KindRunEnded:
			runEnd = entry
		case gotato.KindSessionReleased:
			release = entry
		}
	}
	if runEnd.Seq == 0 || release.Seq < runEnd.Seq || !isInstance(release.Instance, instance) {
		t.Fatalf("Session %s: run_ended at %d, release at %d by %q; want a release by %s after the Run", id, runEnd.Seq, release.Seq, release.Instance, instance)
	}
	if loaded, err := store.Load(ctx, id); err != nil || loaded.Summary.Busy || loaded.Summary.Running != "" {
		t.Fatalf("Session %s summary = %+v, %v; want idle", id, loaded.Summary, err)
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
			a := instanceRunner(t, open(), "instance-a", model, fastLiveness)
			t.Cleanup(openGate)
			b := instanceRunner(t, open(), "instance-b", testkit.EchoModel{}, service.Liveness{TTL: fastLiveness.TTL, Wait: 100 * time.Millisecond})

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
					if err == nil && (result.Result.Status != gotato.RunCompleted || result.Reconnect != nil) {
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

			// The row says draining, so every instance can see it.
			if state, err := open().Instance(ctx, a.Instance()); err != nil || !state.Alive || !state.Draining {
				t.Fatalf("instance-a while draining = %+v, %v; want alive and draining", state, err)
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
				if runs := view.Runs(); len(runs) != 1 || runs[0].Status != gotato.RunCompleted {
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
	a := instanceRunner(t, store, "instance-a", model, fastLiveness)

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
		result   service.RunResult
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
				if loaded, err := store.Load(ctx, id); err != nil || loaded.Summary.Busy {
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
		if o.result.Result.Status != gotato.RunCanceled || o.result.Result.RunID == "" {
			t.Fatalf("a Run past the deadline = %+v, want a cancelled Run", o.result.Result)
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
		if runs := view.Runs(); len(runs) != 1 || runs[0].Status != gotato.RunCanceled {
			t.Fatalf("runs = %+v, want one cancelled", runs)
		}
		checkReleased(t, store, id, "instance-a")
	}
}
