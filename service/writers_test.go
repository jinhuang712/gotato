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

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

var fastLiveness = service.Liveness{TTL: 150 * time.Millisecond, Wait: 3 * time.Second}

func instanceRunner(t *testing.T, store session.Store, instance string, model gotato.Model, liveness service.Liveness) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: instance, Liveness: liveness,
		Admission: service.Admission{Queue: service.WaitWhileBusy},
		Agents:    []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

func TestIdleSessionMovesToAnyInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, fastLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, fastLiveness)
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
		if waited := time.Since(began); waited > fastLiveness.TTL {
			t.Fatalf("Run %d waited %v for an idle Session", i, waited)
		}
	}
	view, err := b.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if claim := view.Claim(); claim.Instance != "instance-a" || claim.Epoch != 3 {
		t.Fatalf("claim = %+v, want instance-a at epoch 3", claim)
	}
	if got := len(view.Messages()); got != 6 {
		t.Fatalf("messages = %d, want 6", got)
	}
}

// gateModel holds every reply until its gate opens.
type gateModel struct {
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (m *gateModel) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.NewFakeModel(testkit.Text("released")).Stream(ctx, gotato.ModelRequest{})
}

func TestBusyElsewhereWaitsThenRefuses(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	a := instanceRunner(t, store, "instance-a", held, fastLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, service.Liveness{TTL: fastLiveness.TTL, Wait: 200 * time.Millisecond})
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
	b2 := instanceRunner(t, store, "instance-c", testkit.EchoModel{}, fastLiveness)
	if _, err := b2.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "after"}); err != nil {
		t.Fatalf("a Run that waited for the holder's Run = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	view, err := a.Session(ctx, s.ID())
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
	if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "late"}); !gotato.IsCode(err, gotato.ErrConflict) {
		t.Fatalf("a Run after the row lapsed = %v, want a conflict", err)
	}
	if len(model.Requests()) != 0 {
		t.Fatalf("the model was called %d times after the row lapsed", len(model.Requests()))
	}
	// Its row has expired, so another instance takes the Session at once.
	b := instanceRunner(t, backing, "instance-b", testkit.EchoModel{}, fastLiveness)
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "taken over"}); err != nil {
		t.Fatal(err)
	}
}

// killableStore stops every write and renewal once its instance is killed.
type killableStore struct {
	session.Store
	dead atomic.Bool
}

var errKilled = errors.New("the instance was killed")

func (s *killableStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	if s.dead.Load() {
		return errKilled
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *killableStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	if s.dead.Load() {
		return errKilled
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func (s *killableStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.dead.Load() {
		return errKilled
	}
	return s.Store.Beat(ctx, row, ttl)
}

// ownerModel answers "<instance>: <prompt>", after checking in the store
// that its instance holds the Session it is called for. A killed instance's
// model fails.
type ownerModel struct {
	instance   string
	store      session.Store
	dead       *atomic.Bool
	violations *atomic.Int64
	calls      *atomic.Int64
}

func (m ownerModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if m.dead.Load() {
		return nil, errKilled
	}
	m.calls.Add(1)
	prompt := gotato.TextOf(request.Messages[len(request.Messages)-1])
	id, _, _ := strings.Cut(prompt, "/")
	stored, err := session.Open(ctx, m.store, id)
	if err != nil {
		return nil, err
	}
	if claim := stored.Claim(); claim.Instance != m.instance {
		m.violations.Add(1)
	}
	return testkit.NewFakeModel(testkit.Text(m.instance+": "+prompt)).Stream(ctx, gotato.ModelRequest{})
}

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
				runners[i] = instanceRunner(t, stores[i], model.instance, model, fastLiveness)
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
