package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	itext "github.com/jinhuang712/gotato/internal/text"
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

type filesToolSet struct{}

func (filesToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "files"} }

func (filesToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{testkit.NewFakeTool("read", "contents")}, nil
}

func toolIDsOf(request gotato.ModelRequest) []string {
	ids := make([]string, 0, len(request.Tools))
	for _, spec := range request.Tools {
		ids = append(ids, spec.ID)
	}
	return ids
}

// toolList is a ToolSource with a fixed set of Tools.
type toolList []gotato.Tool

func (l toolList) Tools(context.Context) ([]gotato.Tool, error) { return l, nil }

type stranger struct{}

func refused(ctx context.Context) bool { return ctx.Value(stranger{}) != nil }

// fakeClock is a Clock a test advances itself: no test waits for a TTL. Advance
// calls the timers that fall due, in order, on the caller's goroutine, so it
// returns when the work they start is done.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock   *fakeClock
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) service.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

// Pending counts the timers that have neither fired nor been stopped.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired {
			n++
		}
	}
	return n
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, timer := range c.timers {
			if !timer.stopped && !timer.fired && !timer.at.After(target) && (next == nil || timer.at.Before(next.at)) {
				next = timer
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// tallyStore counts the reads and writes of a Store.
type tallyStore struct {
	session.Store
	mu      sync.Mutex
	loads   int
	appends int
	// failCheckpoint makes the next Append of a Checkpoint fail once.
	failCheckpoint bool
	// conflicts counts the Appends the store refused as a conflict.
	conflicts int
}

func (s *tallyStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	s.mu.Lock()
	s.loads++
	s.mu.Unlock()
	return s.Store.Load(ctx, id)
}

func (s *tallyStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	s.mu.Lock()
	s.appends++
	fail := false
	if s.failCheckpoint {
		for _, entry := range entries {
			fail = fail || entry.IsCheckpoint()
		}
		if fail {
			s.failCheckpoint = false
		}
	}
	s.mu.Unlock()
	if fail {
		return errors.New("the store could not write the checkpoint")
	}
	err := s.Store.Append(ctx, id, after, entries, summary)
	if errors.Is(err, session.ErrConflict) {
		s.mu.Lock()
		s.conflicts++
		s.mu.Unlock()
	}
	return err
}

func (s *tallyStore) Conflicts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conflicts
}

func (s *tallyStore) Loads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func (s *tallyStore) Appends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appends
}

func runOn(t *testing.T, runner *service.Runner, id, prompt string) service.RunResponse {
	t.Helper()
	result, err := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt})
	if err != nil || result.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Run %q = %+v, %v", prompt, result.Result, err)
	}
	return result
}

func storedKinds(t *testing.T, store session.Store, id string) []gotato.EventKind {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return kindsOf(events)
}

func count(kinds []gotato.EventKind, kind gotato.EventKind) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}

// awaitUnloads waits for the recorder to have seen n session_unloaded Events,
// which reach it from the Instance's stream, and returns their why.
func awaitUnloads(t *testing.T, rec *testkit.EventRecorder, n int) []string {
	t.Helper()
	waitFor(t, func() bool { return len(unloads(rec)) >= n })
	return unloads(rec)
}

// unloads returns the why of every session_unloaded the recorder has seen.
func unloads(rec *testkit.EventRecorder) []string {
	var out []string
	for _, event := range rec.Events() {
		if unloaded, ok := event.(gotato.SessionUnloaded); ok {
			out = append(out, unloaded.Why)
		}
	}
	return out
}

// askModel answers every request with a short reply, but holds a Recap's ask
// until its context ends, after saying it has started.
type askModel struct {
	started chan struct{}
	once    sync.Once
	reply   *testkit.FuncModel
}

func (m *askModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if isAsk(request) {
		m.once.Do(func() { close(m.started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return m.reply.Stream(ctx, request)
}

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

// steadyLiveness is for a test that is not about a row expiring. A Runner
// refuses a write once its last renewal is older than 4/5 of the TTL
// (Runner.guard), so with fastLiveness's 150 ms a renewal tick that a loaded
// machine runs 40 ms late fails a Run with "liveness row may have expired".
// Only a test that waits for a row to expire, or pauses the renewals, uses
// fastLiveness.
var steadyLiveness = service.Liveness{TTL: 5 * time.Second, Wait: 3 * time.Second}

// isInstance reports whether got is the name of the Instance that was
// configured with label: the label and the per-start suffix.
func isInstance(got, label string) bool {
	suffix, ok := strings.CutPrefix(got, label+"-")
	return ok && len(suffix) == 8
}

func instanceRunner(t *testing.T, store session.Store, instance string, model gotato.Model, liveness service.Liveness) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: instance, Liveness: liveness,
		Admission: service.Admission{Queue: service.QueuePolicyWait},
		Agents:    []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
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

// killableStore stops every write and renewal once its instance is killed.
type killableStore struct {
	session.Store
	dead atomic.Bool
}

func (s *killableStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.dead.Load() {
		return errKilled
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *killableStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
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
	if claim := stored.Claim(); !isInstance(claim.Instance, m.instance) {
		m.violations.Add(1)
	}
	return testkit.NewFakeModel(testkit.Text(m.instance+": "+prompt)).Stream(ctx, gotato.ModelRequest{})
}

// claimsOf returns the session_claimed Events of the Record.
func claimsOf(t *testing.T, runner *service.Runner, sessionID string) []gotato.SessionClaimed {
	t.Helper()
	record, err := runner.Record(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var claims []gotato.SessionClaimed
	for _, event := range record.Events() {
		if claimed, ok := event.(gotato.SessionClaimed); ok {
			claims = append(claims, claimed)
		}
	}
	return claims
}

// holdAtTheEnd holds a Run after it has settled and before its live Session
// stops taking messages, once: the last moment a message is accepted as queued
// by a Run that no longer takes any.
type holdAtTheEnd struct {
	reached chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func newHold() *holdAtTheEnd {
	return &holdAtTheEnd{reached: make(chan struct{}), proceed: make(chan struct{})}
}

func (h *holdAtTheEnd) hook() {
	h.once.Do(func() {
		close(h.reached)
		<-h.proceed
	})
}

// holdBeforeReleaseOn is holdBeforeRelease over the given Store.
func holdBeforeReleaseOn(t *testing.T, store session.Store) (runner *service.Runner, id string, hold *holdAtTheEnd, done chan service.RunResponse) {
	t.Helper()
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	hold = newHold()
	runner.SetBeforeRelease(hold.hook)
	s, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	id = s.ID()
	done = make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "hello"})
		done <- result
	}()
	select {
	case <-hold.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached the end")
	}
	return runner, id, hold, done
}

// stagedModel answers call n with replies[n] once release[n] is closed, and
// says which call started on started. With hang set, call 0 instead streams
// "partial" and waits for its context, so there is a model call in flight to
// interrupt or stop.
type stagedModel struct {
	hang    bool
	started chan int
	release []chan struct{}
	replies []string

	mu       sync.Mutex
	calls    int
	requests [][]gotato.Message
}

func newStagedModel(hang bool, replies ...string) *stagedModel {
	m := &stagedModel{hang: hang, started: make(chan int, 16), replies: replies}
	for range replies {
		m.release = append(m.release, make(chan struct{}))
	}
	return m
}

func (m *stagedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	index := m.calls
	m.calls++
	m.requests = append(m.requests, append([]gotato.Message(nil), request.Messages...))
	m.mu.Unlock()
	m.started <- index
	if m.hang && index == 0 {
		return &hangingStream{}, nil
	}
	select {
	case <-m.release[index]:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.NewFakeModel(testkit.Text(m.replies[index])).Stream(ctx, request)
}

func (m *stagedModel) request(call int) []gotato.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[call]
}

func waitCall(t *testing.T, m *stagedModel, want int) {
	t.Helper()
	select {
	case got := <-m.started:
		if got != want {
			t.Fatalf("model call %d started, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("model call %d never started", want)
	}
}

func texts(messages []gotato.Message) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, string(m.Role)+":"+gotato.TextOf(m))
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// startRun runs a prompt on the Session in the background.
func startRun(t *testing.T, runner *service.Runner, id, prompt string) chan service.RunResponse {
	t.Helper()
	done := make(chan service.RunResponse, 1)
	go func() {
		result, err := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- result
	}()
	return done
}

func settled(t *testing.T, done chan service.RunResponse) service.RunResponse {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the Run did not settle")
		return service.RunResponse{}
	}
}

func transcriptOf(t *testing.T, runner *service.Runner, id string) []string {
	t.Helper()
	record, err := runner.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return texts(record.Messages())
}

func messageRunner(t *testing.T, store session.Store, instance, address string, model gotato.Model) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: instance, Address: address, Liveness: steadyLiveness, Admission: service.Admission{Queue: service.QueuePolicyWait},
		Agents: []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// readNumbers reads count stored Events from a stream and returns their
// numbers, failing if they do not come.
func readNumbers(t *testing.T, stream *service.SessionStream, count int) []uint64 {
	t.Helper()
	var out []uint64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for len(out) < count {
		event, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("after %v: %v", out, err)
		}
		if event.Kind().Class() == gotato.EventClassStored {
			out = append(out, event.Head().Number)
		}
	}
	return out
}

func consecutive(from uint64, count int) []uint64 {
	out := make([]uint64, count)
	for i := range out {
		out[i] = from + uint64(i)
	}
	return out
}

func recreatedRunner(t *testing.T, name string, store session.Store, poll time.Duration) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: name, StreamPoll: poll,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, ToolSources: []gotato.ToolSource{toolList{testkit.NewFakeTool("probe", "ok")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// endsWithoutNewEvents reads sub until it fails, and fails the test if a
// stored Event comes first or the error is not cannot_resume or not found.
func endsWithoutNewEvents(t *testing.T, sub gotato.Subscription) {
	t.Helper()
	read, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		event, err := sub.Next(read)
		if err != nil {
			if !errors.Is(err, service.ErrCannotResume) && !errors.Is(err, session.ErrNotFound) {
				t.Fatalf("the stream ended with %v, want cannot_resume or not found", err)
			}
			return
		}
		if event.Kind().Class() == gotato.EventClassStored {
			t.Fatalf("the stream delivered Event %d (%s) of the Record created again", event.Head().Number, event.Kind())
		}
	}
}

// storedOnly keeps the Events that have a number in the Record.
func storedOnly(events []gotato.Event) []gotato.Event {
	var out []gotato.Event
	for _, event := range events {
		if event.Kind().Class() == gotato.EventClassStored {
			out = append(out, event)
		}
	}
	return out
}

// numbered renders Events as "kind#number", to compare two lists of them.
func numbered(events []gotato.Event) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = fmt.Sprintf("%s#%d", event.Kind(), event.Head().Number)
	}
	return out
}

// failingSaveStore wraps a Store and fails the write that ends a Run.
type failingSaveStore struct {
	session.Store
	mu    sync.Mutex
	saves int
	// failFinal fails the write that ends a Run: the one holding run_ended.
	failFinal bool
	// failRelease fails the write that releases the Session, and only that one.
	failRelease bool
}

func (s *failingSaveStore) fail(entries []session.Entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	for _, entry := range entries {
		if s.failFinal && entry.Kind == gotato.EventKindRunEnded {
			return true
		}
		if s.failRelease && entry.Kind == gotato.EventKindSessionReleased {
			return true
		}
	}
	return false
}

func (s *failingSaveStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if s.fail(entries) {
		return errors.New("store: disk full")
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *failingSaveStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	if s.fail(entries) {
		return errors.New("store: disk full")
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

const agentRecap = "## What happened\n- the user asked three things\n## Key facts\n- order 42 is shipped\n"

// isAsk reports whether the last message of a request is the Recap's ask.
func isAsk(request gotato.ModelRequest) bool {
	n := len(request.Messages)
	return n > 0 && strings.HasPrefix(gotato.TextOf(request.Messages[n-1]), itext.RecapAskPrefix)
}

// recapModel answers a Recap ask with agentRecap and every other request with
// a short reply.
func recapModel() *testkit.FuncModel {
	return testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		if isAsk(request) {
			return testkit.Text(agentRecap), nil
		}
		return testkit.Text("ok"), nil
	})
}
