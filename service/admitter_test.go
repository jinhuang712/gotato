package service_test

import (
	"context"
	"errors"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/testkit"
)

// tenantKey carries who asks in a request's ctx, as a service's own middleware
// would put it there.
type tenantKey struct{}

func asTenant(tenant string) context.Context {
	return context.WithValue(context.Background(), tenantKey{}, tenant)
}

var errTenantFull = gotato.ErrorOf(gotato.CodeLimitExceeded, "tenant is at its limit of runs")

// asked is one call of an Admitter.
type asked struct {
	tenant    string
	admission service.Admission
	load      service.Load
}

// tenantAdmitter gives each tenant its own number of Runs, and counts what it
// admits and what is given back.
type tenantAdmitter struct {
	limits map[string]int

	mu       sync.Mutex
	held     map[string]int
	calls    []asked
	admitted int
	released int
}

func newTenantAdmitter(limits map[string]int) *tenantAdmitter {
	return &tenantAdmitter{limits: limits, held: map[string]int{}}
}

func (a *tenantAdmitter) Admit(ctx context.Context, admission service.Admission, load service.Load) (func(), error) {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, asked{tenant, admission, load})
	if a.held[tenant] >= a.limits[tenant] {
		return nil, errTenantFull
	}
	a.held[tenant]++
	a.admitted++
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.held[tenant]--
		a.released++
	}, nil
}

func (a *tenantAdmitter) snapshot() (held map[string]int, calls []asked, admitted, released int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held = maps.Clone(a.held)
	return held, append([]asked(nil), a.calls...), a.admitted, a.released
}

// countingAdmitter admits every Run and counts the admits and the releases,
// or refuses with refuse when it is set.
type countingAdmitter struct {
	refuse   error
	admits   atomic.Int32
	releases atomic.Int32
	loads    []service.Load
	mu       sync.Mutex
}

func (a *countingAdmitter) Admit(_ context.Context, _ service.Admission, load service.Load) (func(), error) {
	a.mu.Lock()
	a.loads = append(a.loads, load)
	a.mu.Unlock()
	a.admits.Add(1)
	if a.refuse != nil {
		return nil, a.refuse
	}
	return func() { a.releases.Add(1) }, nil
}

func (a *countingAdmitter) loadsSeen() []service.Load {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]service.Load(nil), a.loads...)
}

func blockedRunner(t *testing.T, admitter service.Admitter, bounds service.Bounds) (*service.Runner, *testkit.FakeModel, chan struct{}) {
	t.Helper()
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, err := service.New(service.Config{Store: memStore(), Agents: []service.Agent{{Name: "slow", Model: model}}, Admitter: admitter, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	return runner, model, block
}

func runAs(ctx context.Context, runner *service.Runner, prompt string) chan error {
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, service.RunRequest{Prompt: prompt, Agent: "slow"})
		done <- err
	}()
	return done
}

// The default Admitter is Limits, and Limits{MaxActiveRuns} is the old
// MaxActiveRuns: the Run past the number is refused as capacity, not run and
// retryable, and a run_rejected says at_capacity.
func TestLimitsIsTheDefaultAdmitter(t *testing.T) {
	cases := []struct {
		name     string
		admitter service.Admitter
		runs     int // Runs in flight that the Admitter lets in before it refuses; -1 for no refusal
	}{
		{"limits", service.Limits{MaxActiveRuns: 2}, 2},
		{"no admitter is no limit", nil, -1},
		{"zero limits is no limit", service.Limits{}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, model, block := blockedRunner(t, tc.admitter, service.Bounds{})
			rec := instanceRecorder(t, runner)
			ctx := context.Background()
			var done []chan error
			for i := range 3 {
				if tc.runs >= 0 && i == tc.runs {
					break
				}
				done = append(done, runAs(ctx, runner, "go"))
			}
			waitFor(t, func() bool { return model.Calls() == len(done) })
			if tc.runs < 0 {
				// Every Run is admitted: all are in flight at once.
				if runner.ActiveRuns() != len(done) {
					t.Fatalf("ActiveRuns = %d, want %d", runner.ActiveRuns(), len(done))
				}
			} else {
				_, err := runner.Run(ctx, service.RunRequest{Prompt: "one more", Agent: "slow"})
				if !errors.Is(err, service.ErrCapacity) || !gotato.NotRun(err) || !gotato.Retryable(err) || !gotato.IsCode(err, gotato.CodeLimitExceeded) {
					t.Fatalf("Run past MaxActiveRuns = %v, want ErrCapacity, limit_exceeded, not run and retryable", err)
				}
				rejections := func() (whys []string) {
					for _, event := range rec.Events() {
						if r, ok := event.(service.RunRejected); ok {
							whys = append(whys, r.Why)
						}
					}
					return whys
				}
				waitFor(t, func() bool { return len(rejections()) > 0 })
				if got := rejections(); len(got) != 1 || got[0] != "at_capacity" {
					t.Fatalf("run_rejected = %v, want [at_capacity]", got)
				}
			}
			close(block)
			for _, d := range done {
				<-d
			}
		})
	}
}

func TestLimitsAdmitComparesTheLoad(t *testing.T) {
	limits := service.Limits{MaxActiveRuns: 2}
	if _, err := limits.Admit(context.Background(), service.Admission{}, service.Load{ActiveRuns: 1}); err != nil {
		t.Fatalf("one Run in flight under a limit of 2 = %v", err)
	}
	if _, err := limits.Admit(context.Background(), service.Admission{}, service.Load{ActiveRuns: 2}); !errors.Is(err, service.ErrCapacity) {
		t.Fatalf("two Runs in flight under a limit of 2 = %v, want ErrCapacity", err)
	}
	if err := (service.Limits{MaxActiveRuns: -1}).Validate(); err == nil {
		t.Fatal("a negative limit passed Validate")
	}
}

// A service's own Admitter reads who asks from the ctx, keeps its own count
// per tenant, and gets it back through release.
func TestAdmitterHonoursWhoAsksAndTheCountComesBackOnRelease(t *testing.T) {
	admitter := newTenantAdmitter(map[string]int{"a": 1, "b": 2})
	runner, model, block := blockedRunner(t, admitter, service.Bounds{})

	a1 := runAs(asTenant("a"), runner, "a1")
	waitFor(t, func() bool { return model.Calls() == 1 })
	if _, err := runner.Run(asTenant("a"), service.RunRequest{Prompt: "a2", Agent: "slow"}); !errors.Is(err, errTenantFull) {
		t.Fatalf("second Run of tenant a = %v, want its own error", err)
	} else if !gotato.NotRun(err) || !gotato.IsCode(err, gotato.CodeLimitExceeded) {
		t.Fatalf("the refusal is %v, want limit_exceeded and not run (nothing started)", err)
	}
	// Another tenant is not held back by it.
	b1 := runAs(asTenant("b"), runner, "b1")
	b2 := runAs(asTenant("b"), runner, "b2")
	waitFor(t, func() bool { return model.Calls() == 3 })
	if _, err := runner.Run(asTenant("b"), service.RunRequest{Prompt: "b3", Agent: "slow"}); !errors.Is(err, errTenantFull) {
		t.Fatalf("third Run of tenant b = %v", err)
	}
	if _, err := runner.Run(context.Background(), service.RunRequest{Prompt: "nobody", Agent: "slow"}); !errors.Is(err, errTenantFull) {
		t.Fatalf("a Run with no tenant = %v, want refusal (limit 0)", err)
	}

	_, calls, _, released := admitter.snapshot()
	if released != 0 {
		t.Fatalf("%d releases while every admitted Run is in flight", released)
	}
	// The Admitter is told the Run and the Instance's load, without the Run itself.
	if first := calls[0]; first.tenant != "a" || first.admission.Agent != "slow" || first.admission.SessionID != "" || first.load != (service.Load{}) {
		t.Fatalf("first call = %+v, want tenant a, Agent slow, a new Session and an empty Instance", first)
	}
	if third := calls[3]; third.tenant != "b" || third.load.ActiveRuns != 2 || third.load.ActiveSessions != 2 {
		t.Fatalf("call for b2 = %+v, want the load of the two Runs in flight", third)
	}

	close(block)
	for _, d := range []chan error{a1, b1, b2} {
		if err := <-d; err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return runner.ActiveRuns() == 0 })
	held, _, admitted, released := admitter.snapshot()
	if admitted != 3 || released != 3 || held["a"] != 0 || held["b"] != 0 {
		t.Fatalf("admitted %d, released %d, held %v: every admitted Run gives its place back once", admitted, released, held)
	}
	// Its place is free again.
	if _, err := runner.Run(asTenant("a"), service.RunRequest{Prompt: "a3", Agent: "slow"}); err != nil {
		t.Fatalf("tenant a after its Run ended = %v", err)
	}
}

// release runs exactly once when the Run ends, however it ends.
func TestAdmitterReleaseRunsOnceWhenTheRunEnds(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(t *testing.T, runner *service.Runner, model *testkit.FakeModel, block chan struct{})
		fail bool
	}{
		{"a Run that completes", func(t *testing.T, runner *service.Runner, _ *testkit.FakeModel, block chan struct{}) {
			close(block)
			if _, err := runner.Run(ctx, service.RunRequest{Prompt: "go"}); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a Run that fails", func(t *testing.T, runner *service.Runner, model *testkit.FakeModel, block chan struct{}) {
			close(block)
			model.Err = errors.New("the provider is down")
			if _, err := runner.Run(ctx, service.RunRequest{Prompt: "go"}); err == nil {
				t.Fatal("the Run did not fail")
			}
		}, true},
		{"a Run whose context ends", func(t *testing.T, runner *service.Runner, model *testkit.FakeModel, _ chan struct{}) {
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { _, err := runner.Run(runCtx, service.RunRequest{Prompt: "go"}); done <- err }()
			waitFor(t, func() bool { return model.Calls() == 1 })
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled Run = %v", err)
			}
		}, false},
		{"a Run that is aborted", func(t *testing.T, runner *service.Runner, model *testkit.FakeModel, _ chan struct{}) {
			s, err := runner.CreateSession(ctx, service.CreateSessionRequest{})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID, Prompt: "go"}); done <- err }()
			waitFor(t, func() bool { return model.Calls() == 1 })
			if err := runner.AbortSession(ctx, s.ID); err != nil {
				t.Fatal(err)
			}
			<-done
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admitter := &countingAdmitter{}
			runner, model, block := blockedRunner(t, admitter, service.Bounds{})
			tc.run(t, runner, model, block)
			if admitter.admits.Load() != 1 || admitter.releases.Load() != 1 {
				t.Fatalf("admits %d, releases %d, want 1 and 1 when the Run ended", admitter.admits.Load(), admitter.releases.Load())
			}
			if runner.ActiveRuns() != 0 {
				t.Fatalf("ActiveRuns = %d after the Run ended", runner.ActiveRuns())
			}
			// Nothing gives it back a second time later.
			time.Sleep(20 * time.Millisecond)
			if admitter.releases.Load() != 1 {
				t.Fatalf("releases = %d, want exactly 1", admitter.releases.Load())
			}
		})
	}
}

// A Run the Admitter refuses holds nothing, so there is nothing to release.
func TestAdmitterRefusalIsNotReleased(t *testing.T) {
	admitter := &countingAdmitter{refuse: errors.New("no room for you")}
	runner, _, _ := blockedRunner(t, admitter, service.Bounds{})
	_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "go"})
	if !gotato.IsCode(err, gotato.CodeLimitExceeded) || !gotato.NotRun(err) {
		t.Fatalf("a refusal that is not typed = %v, want limit_exceeded and not run", err)
	}
	if !errors.Is(err, admitter.refuse) {
		t.Fatalf("the Admitter's own error is not in the chain of %v", err)
	}
	if admitter.releases.Load() != 0 || runner.ActiveRuns() != 0 {
		t.Fatalf("releases %d, ActiveRuns %d after a refusal", admitter.releases.Load(), runner.ActiveRuns())
	}
}

// The drain and the memory bound guard the Instance, not a policy: they run
// before the Admitter, which is never asked about a Run they refuse.
func TestMemoryBoundAndDrainRunBeforeTheAdmitter(t *testing.T) {
	t.Run("the memory bound refuses first", func(t *testing.T) {
		admitter := &countingAdmitter{}
		runner, model, block := blockedRunner(t, admitter, service.Bounds{MaxActiveSessions: 1})
		first := runAs(context.Background(), runner, "first")
		waitFor(t, func() bool { return model.Calls() == 1 })
		// An Admitter that would refuse changes nothing: the bound answers.
		admitter.refuse = errTenantFull
		_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "second", Agent: "slow"})
		if !errors.Is(err, service.ErrOverloaded) {
			t.Fatalf("Run past MaxActiveSessions = %v, want ErrOverloaded and not the Admitter's", err)
		}
		if admitter.admits.Load() != 1 {
			t.Fatalf("the Admitter was asked %d times, want only for the first Run", admitter.admits.Load())
		}
		close(block)
		<-first
	})

	t.Run("an evicted warm copy is gone when the Admitter is asked", func(t *testing.T) {
		admitter := &countingAdmitter{}
		runner, _, block := blockedRunner(t, admitter, service.Bounds{MaxActiveSessions: 1})
		close(block)
		ctx := context.Background()
		if _, err := runner.Run(ctx, service.RunRequest{Prompt: "first", Agent: "slow"}); err != nil {
			t.Fatal(err)
		}
		if runner.WarmSessions() != 1 {
			t.Fatalf("WarmSessions = %d, want the first Session warm", runner.WarmSessions())
		}
		if _, err := runner.Run(ctx, service.RunRequest{Prompt: "second", Agent: "slow"}); err != nil {
			t.Fatalf("the second Session did not evict the warm one: %v", err)
		}
		loads := admitter.loadsSeen()
		if len(loads) != 2 || loads[1].WarmSessions != 0 {
			t.Fatalf("loads the Admitter saw = %+v, want the second with no warm Session: the eviction ran before it", loads)
		}
	})

	t.Run("a draining Instance refuses first", func(t *testing.T) {
		admitter := &countingAdmitter{}
		runner, _, _ := blockedRunner(t, admitter, service.Bounds{})
		if err := runner.Drain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(context.Background(), service.RunRequest{Prompt: "late"}); !errors.Is(err, service.ErrDraining) {
			t.Fatalf("Run after Drain = %v, want ErrDraining", err)
		}
		if admitter.admits.Load() != 0 {
			t.Fatal("the Admitter was asked about a Run on a draining Instance")
		}
	})
}

// An Admitter that panics refuses the Run; the Runner goes on.
func TestAdmitterThatPanicsRefusesTheRun(t *testing.T) {
	var explode atomic.Bool
	explode.Store(true)
	admitter := admitFunc(func(context.Context, service.Admission, service.Load) (func(), error) {
		if explode.Load() {
			panic("a bug in the service's policy")
		}
		return nil, nil
	})
	runner, _, block := blockedRunner(t, admitter, service.Bounds{})
	close(block)
	_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "go", Agent: "slow"})
	if !gotato.NotRun(err) || !gotato.IsCode(err, gotato.CodeLimitExceeded) {
		t.Fatalf("Run with a panicking Admitter = %v, want a refusal, not run", err)
	}
	if runner.ActiveRuns() != 0 {
		t.Fatalf("ActiveRuns = %d", runner.ActiveRuns())
	}
	explode.Store(false)
	if _, err := runner.Run(context.Background(), service.RunRequest{Prompt: "again", Agent: "slow"}); err != nil {
		t.Fatalf("the Runner did not recover from the panic: %v", err)
	}
}

type admitFunc func(context.Context, service.Admission, service.Load) (func(), error)

func (f admitFunc) Admit(ctx context.Context, a service.Admission, l service.Load) (func(), error) {
	return f(ctx, a, l)
}
