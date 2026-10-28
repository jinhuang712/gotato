package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func newRunner(t *testing.T, specs ...service.Agent) (*service.Runner, session.Store) {
	t.Helper()
	store := session.NewMemoryStore()
	if len(specs) == 0 {
		specs = []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"}}
	}
	runner, err := service.New(service.Config{Store: store, Agents: specs})
	if err != nil {
		t.Fatal(err)
	}
	return runner, store
}

// instanceRecorder records the Runner's Instance stream from now on.
func instanceRecorder(t *testing.T, runner *service.Runner, options ...gotato.SubscribeOption) *testkit.EventRecorder {
	t.Helper()
	sub, err := runner.SubscribeInstance(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	rec := testkit.RecordSubscription(sub)
	t.Cleanup(rec.Close)
	return rec
}

func kindsOf(events []gotato.Event) []gotato.EventKind {
	kinds := make([]gotato.EventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind()
	}
	return kinds
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// errConnReset is the raw error a faulty store returns, as a driver would.
var errConnReset = errors.New("connection reset")

// faultStore fails the store calls a test names with a raw error, and passes
// the others through. A test turns a call on and off while Runs go on.
type faultStore struct {
	session.Store
	mu   sync.Mutex
	down map[string]bool
}

// fail makes the call named op ("Load", "Create", "Append", "Beat", "Delete",
// "Log", "Instance") fail while on is true.
func (s *faultStore) fail(op string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down == nil {
		s.down = map[string]bool{}
	}
	s.down[op] = on
}

func (s *faultStore) failing(op string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down[op]
}

func (s *faultStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	if s.failing("Load") {
		return session.Loaded{}, errConnReset
	}
	return s.Store.Load(ctx, id)
}

func (s *faultStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.failing("Create") {
		return errConnReset
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *faultStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	if s.failing("Append") {
		return errConnReset
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func (s *faultStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if s.failing("Beat") {
		return errConnReset
	}
	return s.Store.Beat(ctx, row, ttl)
}

func (s *faultStore) Delete(ctx context.Context, id string) error {
	if s.failing("Delete") {
		return errConnReset
	}
	return s.Store.Delete(ctx, id)
}

func (s *faultStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if s.failing("Log") {
		return nil, errConnReset
	}
	return s.Store.Log(ctx, id, after, limit)
}

func (s *faultStore) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if s.failing("Instance") {
		return session.InstanceState{}, errConnReset
	}
	return s.Store.Instance(ctx, id)
}

// watchedStore tells a test what a stream's catch-up has read, so the test
// goes on when the catch-up has, not after a delay. A stream reads its Record
// on a goroutine of its own: a Log read that returns Events (a batch), the
// Load that checks the Record's epoch, then a Log read that returns nothing,
// after which the stream is live.
type watchedStore struct {
	session.Store
	batches    atomic.Int64 // Log reads that returned Entries
	emptyReads atomic.Int64 // Log reads that returned none
	verifies   atomic.Int64 // Loads since the last batch
}

func (w *watchedStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	entries, err := w.Store.Log(ctx, id, after, limit)
	if err == nil {
		if len(entries) == 0 {
			w.emptyReads.Add(1)
		} else {
			w.verifies.Store(0)
			w.batches.Add(1)
		}
	}
	return entries, err
}

func (w *watchedStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	loaded, err := w.Store.Load(ctx, id)
	w.verifies.Add(1)
	return loaded, err
}

// waitLive waits until a catch-up that began after emptyReads was read as base
// has read its last, empty page.
func (w *watchedStore) waitLive(t *testing.T, base int64) {
	t.Helper()
	waitUntil(t, "the stream's catch-up to read its last, empty page", func() bool { return w.emptyReads.Load() > base })
}

// waitBatchRead waits until a catch-up that began after batches was read as
// base has read a batch and checked the Record's epoch, which leaves it
// holding the Events for the client.
func (w *watchedStore) waitBatchRead(t *testing.T, base int64) {
	t.Helper()
	waitUntil(t, "the stream's catch-up to read a batch and check the epoch", func() bool {
		return w.batches.Load() > base && w.verifies.Load() >= 1
	})
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// newRunnerOn is newRunner on the given store, with a short liveness, closed
// with the test.
func newRunnerOn(t *testing.T, store session.Store, agent service.Agent, mutate ...func(*service.Config)) *service.Runner {
	t.Helper()
	cfg := service.Config{Store: store, Agents: []service.Agent{agent}, Liveness: service.Liveness{TTL: time.Second, Wait: 30 * time.Millisecond}}
	for _, m := range mutate {
		m(&cfg)
	}
	r, err := service.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

func waitChan(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// kindGate blocks, once per kind, the first Append that carries an Entry of
// that kind, until the test lets it go.
type kindGate struct {
	session.Store
	mu    sync.Mutex
	holds map[gotato.EventKind]*hold
}

type hold struct{ entered, proceed chan struct{} }

func newKindGate(store session.Store, kinds ...gotato.EventKind) *kindGate {
	g := &kindGate{Store: store, holds: map[gotato.EventKind]*hold{}}
	for _, k := range kinds {
		g.holds[k] = &hold{entered: make(chan struct{}), proceed: make(chan struct{})}
	}
	return g
}

func (s *kindGate) Append(ctx context.Context, id string, after uint64, entries []session.Entry, info session.RecordInfo) error {
	var hit *hold
	s.mu.Lock()
	for _, e := range entries {
		if h := s.holds[e.Kind]; h != nil {
			hit = h
			delete(s.holds, e.Kind)
			break
		}
	}
	s.mu.Unlock()
	if hit != nil {
		close(hit.entered)
		<-hit.proceed
	}
	return s.Store.Append(ctx, id, after, entries, info)
}
