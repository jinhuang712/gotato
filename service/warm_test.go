package service_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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

func (s *tallyStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
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

// warmInstance is an Instance over store with the warm Session's clock. Its
// Agent compacts an idle Session of any size, so a few short Runs are enough.
func warmInstance(t *testing.T, store session.Store, name string, clock service.Clock, model gotato.Model, warm service.WarmPolicy) *service.Runner {
	t.Helper()
	if warm.IdleMinTokens == 0 {
		warm.IdleMinTokens = -1
	}
	runner, err := service.New(service.Config{
		Store: store, Instance: name, Clock: clock,
		Admission: service.Admission{Queue: service.WaitWhileBusy},
		Agents:    []service.Agent{{Name: "echo", Model: model, Warm: warm}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

func runOn(t *testing.T, runner *service.Runner, id, prompt string) service.RunResult {
	t.Helper()
	result, err := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt})
	if err != nil || result.Result.Status != gotato.RunCompleted {
		t.Fatalf("Run %q = %+v, %v", prompt, result.Result, err)
	}
	return result
}

func storedKinds(t *testing.T, store session.Store, id string) []gotato.EventKind {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, 0)
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

var storeKinds = []string{"memory", "file"}

// After a Run the Session stays in memory. The next Run finds the store's head
// where the copy left it, and runs on the copy: nothing is loaded, and no
// session_loaded is sent.
func TestWarmSessionIsReusedWhenTheHeadIsUnchanged(t *testing.T) {
	for _, kind := range storeKinds {
		t.Run(kind, func(t *testing.T) {
			store := &tallyStore{Store: sharedStores(t, kind)()}
			runner := warmInstance(t, store, "a", newFakeClock(), testkit.EchoModel{}, service.WarmPolicy{})
			instance := instanceRecorder(t, runner)

			first := runOn(t, runner, "", "one")
			if runner.WarmSessions() != 1 {
				t.Fatalf("warm Sessions = %d after a Run, want 1", runner.WarmSessions())
			}
			second := runOn(t, runner, first.SessionID, "two")
			third := runOn(t, runner, first.SessionID, "three")

			if loads := store.Loads(); loads != 0 {
				t.Fatalf("the store loaded the Session %d times, want 0: the copy was warm", loads)
			}
			if second.FinalText != "echo: two" || third.Messages != 6 {
				t.Fatalf("the Runs on the copy = %q with %d messages", second.FinalText, third.Messages)
			}
			for _, event := range instance.Events() {
				if _, loaded := event.(gotato.SessionLoaded); loaded {
					t.Fatal("a Run on a warm copy sent session_loaded")
				}
			}
			// The copy is the stored Session: a reader that loads it sees the same.
			view, err := session.Open(context.Background(), store.Store, first.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(view.Messages()); got != 6 {
				t.Fatalf("stored messages = %d, want 6", got)
			}
			// Each Run claimed the Session again, with the Epoch it held.
			claims := claimsOf(t, runner, first.SessionID)
			if len(claims) != 3 || claims[2].Epoch != claims[0].Epoch {
				t.Fatalf("claims = %+v, want three with one Epoch", claims)
			}
		})
	}
}

// A warm copy and a Session loaded for the same Run hold the same Messages, so
// every Instance builds the same request.
func TestWarmCopyHoldsWhatALoadWouldBuild(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		return testkit.Text(fmt.Sprintf("seen %d", len(request.Messages))), nil
	})
	runner := warmInstance(t, store, "a", newFakeClock(), model, service.WarmPolicy{})
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	warm, err := runner.Inspect(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	cold := warmInstance(t, store, "b", newFakeClock(), model, service.WarmPolicy{})
	loaded, err := cold.Inspect(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if warm.PrefixHash != loaded.PrefixHash {
		t.Fatalf("the warm copy builds %s, a load builds %s", warm.PrefixHash, loaded.PrefixHash)
	}
	// The model saw 1, then 3 messages: the copy carried the first Run.
	if got := runOn(t, runner, first.SessionID, "three").FinalText; got != "seen 5" {
		t.Fatalf("third reply = %q, want %q", got, "seen 5")
	}
}

// Another Instance writes the Record while the first holds a warm copy. The
// first Instance's next Run finds the head changed, drops the copy, loads the
// Session and runs on the new Record.
func TestStaleWarmCopyIsReloaded(t *testing.T) {
	for _, kind := range storeKinds {
		t.Run(kind, func(t *testing.T) {
			open := sharedStores(t, kind)
			aStore := &tallyStore{Store: open()}
			model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
				return testkit.Text("ok"), nil
			})
			a := warmInstance(t, aStore, "a", newFakeClock(), model, service.WarmPolicy{})
			b := warmInstance(t, open(), "b", newFakeClock(), model, service.WarmPolicy{})
			instance := instanceRecorder(t, a)

			first := runOn(t, a, "", "from-a-1")
			runOn(t, a, first.SessionID, "from-a-2")
			if aStore.Loads() != 0 {
				t.Fatalf("A loaded the Session: %d", aStore.Loads())
			}
			// B takes the idle Session over: A's copy holds no Claim.
			runOn(t, b, first.SessionID, "from-b")
			if got := b.WarmSessions(); got != 1 {
				t.Fatalf("B keeps %d warm Sessions, want 1", got)
			}

			again := runOn(t, a, first.SessionID, "from-a-3")
			if conflicts := aStore.Conflicts(); conflicts != 0 {
				t.Fatalf("A made %d writes that the store refused: the head check should have found the copy stale first", conflicts)
			}
			if loads := aStore.Loads(); loads != 1 {
				t.Fatalf("A loaded the Session %d times at its next Run, want 1", loads)
			}
			if again.Messages != 8 {
				t.Fatalf("A's Run ended with %d messages, want 8: the Run did not use B's", again.Messages)
			}
			var sawB bool
			for _, request := range model.Requests() {
				for _, message := range request.Messages {
					sawB = sawB || strings.Contains(gotato.TextOf(message), "from-b")
				}
			}
			if !sawB {
				t.Fatal("A's last request did not hold B's message")
			}
			if got := awaitUnloads(t, instance, 1); !slices.Equal(got, []string{"taken_over"}) {
				t.Fatalf("A's session_unloaded = %v, want [taken_over]", got)
			}
			if a.WarmSessions() != 1 {
				t.Fatalf("A keeps %d warm Sessions after the Run, want 1", a.WarmSessions())
			}
			view, err := session.Open(context.Background(), open(), first.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(view.Messages()); got != 8 {
				t.Fatalf("stored messages = %d, want 8", got)
			}
		})
	}
}

// A copy of a Session that was deleted and created again elsewhere is of
// another Record: the Run drops it and loads the new one, whatever the new
// Record's length.
func TestWarmCopyOfARecreatedSessionIsDropped(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	a := warmInstance(t, store, "a", newFakeClock(), testkit.EchoModel{}, service.WarmPolicy{})
	b := warmInstance(t, store, "b", newFakeClock(), testkit.EchoModel{}, service.WarmPolicy{})
	first := runOn(t, a, "", "old")
	if err := b.DeleteSession(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSessionExclusive(ctx, "echo", first.SessionID, nil); err != nil {
		t.Fatal(err)
	}
	again := runOn(t, a, first.SessionID, "new")
	if again.Messages != 2 {
		t.Fatalf("the Run ended with %d messages, want 2: it ran on the old Record", again.Messages)
	}
	view, err := session.Open(ctx, store, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := gotato.TextOf(view.Messages()[0]); got != "new" {
		t.Fatalf("first stored message = %q, want the new Record's", got)
	}
}

// The TTL is per Agent, starts when a Run ends, and starts again with the next
// one. The Session then leaves memory and the next Run loads it.
func TestWarmSessionLeavesMemoryAtItsTTL(t *testing.T) {
	for _, kind := range storeKinds {
		t.Run(kind, func(t *testing.T) {
			clock := newFakeClock()
			store := &tallyStore{Store: sharedStores(t, kind)()}
			runner := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{NoIdleCompaction: true})
			instance := instanceRecorder(t, runner)

			first := runOn(t, runner, "", "one")
			clock.Advance(59 * time.Minute)
			if runner.WarmSessions() != 1 {
				t.Fatal("the Session left memory before its TTL")
			}
			// A Run starts the TTL again.
			runOn(t, runner, first.SessionID, "two")
			clock.Advance(59 * time.Minute)
			if runner.WarmSessions() != 1 {
				t.Fatal("the TTL did not start again with the Run")
			}
			clock.Advance(time.Minute)
			if runner.WarmSessions() != 0 {
				t.Fatal("the Session is still in memory after its TTL")
			}
			if got := awaitUnloads(t, instance, 1); !slices.Equal(got, []string{"ttl"}) {
				t.Fatalf("session_unloaded = %v, want [ttl]", got)
			}
			if clock.Pending() != 0 {
				t.Fatalf("%d timers are left after the Session left", clock.Pending())
			}
			runOn(t, runner, first.SessionID, "three")
			if store.Loads() != 1 {
				t.Fatalf("loads = %d, want the one the Run after the TTL made", store.Loads())
			}
		})
	}
}

func TestWarmTTLIsPerAgent(t *testing.T) {
	clock := newFakeClock()
	store := session.NewMemoryStore()
	runner, err := service.New(service.Config{Store: store, Clock: clock, Agents: []service.Agent{
		{Name: "short", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: 10 * time.Minute, NoIdleCompaction: true}},
		{Name: "never", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}},
		{Name: "default", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{NoIdleCompaction: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	for _, agent := range []string{"short", "never", "default"} {
		if _, err := runner.Run(context.Background(), service.RunRequest{Agent: agent, Prompt: agent}); err != nil {
			t.Fatal(err)
		}
	}
	if runner.WarmSessions() != 2 {
		t.Fatalf("warm Sessions = %d, want 2: a negative TTL keeps none", runner.WarmSessions())
	}
	clock.Advance(10 * time.Minute)
	if runner.WarmSessions() != 1 {
		t.Fatalf("warm Sessions = %d after 10 minutes, want the default Agent's", runner.WarmSessions())
	}
	clock.Advance(50 * time.Minute)
	if runner.WarmSessions() != 0 {
		t.Fatalf("warm Sessions = %d after 60 minutes, want 0", runner.WarmSessions())
	}
}

// retainingModel is a model whose provider keeps the prompt cache for a while.
type retainingModel struct {
	gotato.Model
	retention time.Duration
}

func (m retainingModel) CacheRetention() time.Duration { return m.retention }

// An idle Session is compacted by its own timer shortly before its prompt
// cache expires or it leaves memory, and leaves memory at its TTL. The
// compaction is the one a service's timer calls, and is recorded with the
// reason.
func TestIdleWarmSessionIsCompactedBeforeItLeaves(t *testing.T) {
	cases := []struct {
		name      string
		model     gotato.Model
		warm      service.WarmPolicy
		ttl       time.Duration
		compactAt time.Duration
		why       gotato.CompactionWhy
	}{
		{"cache unknown, before the TTL", testkit.EchoModel{}, service.WarmPolicy{}, time.Hour, 55 * time.Minute, gotato.CompactionBeforeUnload},
		{"cache expires first", retainingModel{testkit.EchoModel{}, time.Hour}, service.WarmPolicy{TTL: 3 * time.Hour}, 3 * time.Hour, 55 * time.Minute, gotato.CompactionBeforeCacheExpires},
		{"cache outlasts the TTL", retainingModel{testkit.EchoModel{}, 2 * time.Hour}, service.WarmPolicy{TTL: time.Hour}, time.Hour, 55 * time.Minute, gotato.CompactionBeforeUnload},
		{"a retention of five minutes is not used", retainingModel{testkit.EchoModel{}, 5 * time.Minute}, service.WarmPolicy{}, time.Hour, 55 * time.Minute, gotato.CompactionBeforeUnload},
		{"a lead of its own", testkit.EchoModel{}, service.WarmPolicy{IdleLead: 10 * time.Minute}, time.Hour, 50 * time.Minute, gotato.CompactionBeforeUnload},
	}
	for _, kind := range storeKinds {
		for _, c := range cases {
			t.Run(kind+"/"+c.name, func(t *testing.T) {
				clock := newFakeClock()
				store := &tallyStore{Store: sharedStores(t, kind)()}
				runner := warmInstance(t, store, "a", clock, c.model, c.warm)
				instance := instanceRecorder(t, runner)
				first := runOn(t, runner, "", "one")
				runOn(t, runner, first.SessionID, "two")
				runOn(t, runner, first.SessionID, "three")
				before := storedKinds(t, store, first.SessionID)

				clock.Advance(c.compactAt - time.Second)
				if got := storedKinds(t, store, first.SessionID); len(got) != len(before) {
					t.Fatalf("the Session was written before its idle lead: %v", got[len(before):])
				}
				clock.Advance(time.Second)
				kinds := storedKinds(t, store, first.SessionID)[len(before):]
				if count(kinds, gotato.KindCompactionEnded) != 1 || count(kinds, gotato.KindSegmentClosed) != 1 {
					t.Fatalf("after its idle lead the Session wrote %v, want one compaction with a Segment", kinds)
				}
				record, err := runner.Session(context.Background(), first.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				var whys []gotato.CompactionWhy
				for _, event := range record.Events() {
					if started, ok := event.(gotato.CompactionStarted); ok {
						whys = append(whys, started.Why)
					}
				}
				if !slices.Equal(whys, []gotato.CompactionWhy{c.why}) {
					t.Fatalf("compaction reasons = %v, want [%s]", whys, c.why)
				}
				// The compaction released the Session: it is idle again.
				checkReleased(t, store, first.SessionID, "a")
				if runner.WarmSessions() != 1 {
					t.Fatal("the compaction dropped the copy")
				}
				if len(unloads(instance)) != 0 {
					t.Fatalf("session_unloaded before the TTL: %v", unloads(instance))
				}

				clock.Advance(c.ttl - c.compactAt)
				if runner.WarmSessions() != 0 {
					t.Fatal("the Session is in memory after its TTL")
				}
				if got := awaitUnloads(t, instance, 1); !slices.Equal(got, []string{"ttl"}) {
					t.Fatalf("session_unloaded = %v, want [ttl]", got)
				}
				// The next Run loads the compacted Session and runs on its Segment.
				loadsBefore := store.Loads()
				result := runOn(t, runner, first.SessionID, "four")
				if store.Loads() != loadsBefore+1 {
					t.Fatalf("loads = %d, want one for the Run after the TTL", store.Loads()-loadsBefore)
				}
				if result.Messages <= 0 {
					t.Fatal("empty Run")
				}
			})
		}
	}
}

// The compaction the timer makes is the compaction a service's timer asks for:
// min_tokens skips a stretch too short to be worth a model call, and nothing is
// written for the skipped one.
func TestIdleCompactionSkipsAShortStretch(t *testing.T) {
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	runner := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{IdleMinTokens: 20000})
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	runOn(t, runner, first.SessionID, "three")
	writes := store.Appends()
	clock.Advance(56 * time.Minute)
	if store.Appends() != writes {
		t.Fatalf("a stretch below the minimum was written: %d writes", store.Appends()-writes)
	}
	if runner.WarmSessions() != 1 {
		t.Fatal("the copy was dropped")
	}
	clock.Advance(4 * time.Minute)
	if runner.WarmSessions() != 0 {
		t.Fatal("the copy outlived its TTL")
	}
}

// A setting turns the idle compaction off; the Session still leaves at its TTL.
func TestIdleCompactionCanBeTurnedOff(t *testing.T) {
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	runner := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{NoIdleCompaction: true})
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	runOn(t, runner, first.SessionID, "three")
	writes := store.Appends()
	clock.Advance(time.Hour)
	if store.Appends() != writes {
		t.Fatal("an idle Session was written with idle compaction off")
	}
	if kinds := storedKinds(t, store, first.SessionID); count(kinds, gotato.KindCompactionStarted) != 0 {
		t.Fatalf("an idle compaction ran with the setting off: %v", kinds)
	}
	if runner.WarmSessions() != 0 {
		t.Fatal("the Session did not leave at its TTL")
	}
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

// A message that arrives while the idle Session compacts cancels the
// compaction and is served first, though the policy rejects a Run on a busy
// Session. The compaction leaves nothing half-written.
func TestRunDuringAnIdleCompactionCancelsIt(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	model := &askModel{started: make(chan struct{}), reply: recapModel()}
	runner, err := service.New(service.Config{
		Store: store, Instance: "a", Clock: clock,
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
	// The queue policy is to reject a Run on a busy Session; this one is served.
	result := runOn(t, runner, first.SessionID, "four")
	select {
	case <-compacting:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle compaction did not end")
	}
	if result.Messages != 8 {
		t.Fatalf("the Run ended with %d messages, want 8", result.Messages)
	}
	kinds := storedKinds(t, store, first.SessionID)
	if count(kinds, gotato.KindCompactionStarted) != 1 || count(kinds, gotato.KindCompactionFailed) != 1 || count(kinds, gotato.KindSegmentClosed) != 0 {
		t.Fatalf("stored Events %v, want one cancelled compaction and no Segment", kinds)
	}
	// The Run started the TTL again, and the compaction does not run twice.
	if runner.WarmSessions() != 1 {
		t.Fatal("the Run did not leave a warm copy")
	}
}

// A compaction that cannot be written stays local: nothing of it reaches the
// store, the copy is dropped, and the Session is usable on the next Run.
func TestFailedIdleCompactionIsDroppedAndTheSessionStaysUsable(t *testing.T) {
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	runner := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{})
	instance := instanceRecorder(t, runner)
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	runOn(t, runner, first.SessionID, "three")

	store.mu.Lock()
	store.failCheckpoint = true
	store.mu.Unlock()
	clock.Advance(55 * time.Minute)

	if got := awaitUnloads(t, instance, 1); !slices.Equal(got, []string{"failed"}) {
		t.Fatalf("session_unloaded = %v, want [failed]", got)
	}
	if runner.WarmSessions() != 0 {
		t.Fatal("a copy that is ahead of the store stayed warm")
	}
	if kinds := storedKinds(t, store, first.SessionID); count(kinds, gotato.KindCompactionEnded) != 0 {
		t.Fatalf("a failed compaction reached the store: %v", kinds)
	}
	result := runOn(t, runner, first.SessionID, "four")
	if result.Messages != 8 || store.Loads() != 1 {
		t.Fatalf("after the failure the Run ended with %d messages after %d loads, want 8 and 1", result.Messages, store.Loads())
	}
}

// Another Instance writes the Session during its idle time: the idle timer
// finds the head changed and drops the copy without a write.
func TestIdleCompactionDropsACopyAnotherInstanceReplaced(t *testing.T) {
	clock := newFakeClock()
	store := session.NewMemoryStore()
	a := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{})
	b := warmInstance(t, store, "b", newFakeClock(), testkit.EchoModel{}, service.WarmPolicy{})
	instance := instanceRecorder(t, a)
	first := runOn(t, a, "", "one")
	runOn(t, a, first.SessionID, "two")
	runOn(t, b, first.SessionID, "three")
	before := storedKinds(t, store, first.SessionID)
	clock.Advance(55 * time.Minute)
	if got := storedKinds(t, store, first.SessionID); len(got) != len(before) {
		t.Fatalf("A wrote a Session that B holds: %v", got[len(before):])
	}
	if got := awaitUnloads(t, instance, 1); !slices.Equal(got, []string{"taken_over"}) || a.WarmSessions() != 0 {
		t.Fatalf("session_unloaded = %v with %d warm Sessions, want [taken_over] and 0", got, a.WarmSessions())
	}
}

// The compact endpoint works on the warm copy, and leaves it warm.
func TestCompactUsesTheWarmCopy(t *testing.T) {
	ctx := context.Background()
	store := &tallyStore{Store: session.NewMemoryStore()}
	runner := warmInstance(t, store, "a", newFakeClock(), testkit.EchoModel{}, service.WarmPolicy{})
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	result, err := runner.Compact(ctx, first.SessionID, modelctx.CompactOptions{})
	if err != nil || !result.Replaced {
		t.Fatalf("Compact = %+v, %v", result, err)
	}
	if store.Loads() != 0 || runner.WarmSessions() != 1 {
		t.Fatalf("loads = %d, warm = %d, want 0 and 1", store.Loads(), runner.WarmSessions())
	}
	runOn(t, runner, first.SessionID, "three")
	if store.Loads() != 0 {
		t.Fatalf("the Run after the compaction loaded the Session %d times", store.Loads())
	}
}

// Deleting a Session drops its copy; draining and closing drop them all. An
// idle copy holds no Claim, so nothing is released or written.
func TestWarmCopiesAreDroppedOnDeleteDrainAndClose(t *testing.T) {
	ctx := context.Background()
	store := &tallyStore{Store: session.NewMemoryStore()}
	clock := newFakeClock()
	runner := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{NoIdleCompaction: true})
	instance := instanceRecorder(t, runner)
	a := runOn(t, runner, "", "a")
	b := runOn(t, runner, "", "b")
	c := runOn(t, runner, "", "c")
	if runner.WarmSessions() != 3 {
		t.Fatalf("warm Sessions = %d, want 3", runner.WarmSessions())
	}
	if err := runner.DeleteSession(ctx, a.SessionID); err != nil {
		t.Fatal(err)
	}
	if runner.WarmSessions() != 2 {
		t.Fatal("a deleted Session stayed warm")
	}
	writes := store.Appends()
	if err := runner.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if runner.WarmSessions() != 0 || store.Appends() != writes {
		t.Fatalf("after the drain: %d warm Sessions, %d writes", runner.WarmSessions(), store.Appends()-writes)
	}
	if clock.Pending() != 0 {
		t.Fatalf("%d timers are left", clock.Pending())
	}
	got := awaitUnloads(t, instance, 3)
	slices.Sort(got)
	if !slices.Equal(got, []string{"deleted", "drain", "drain"}) {
		t.Fatalf("session_unloaded = %v", got)
	}
	_, _ = b, c
}

func TestIdleScheduleOfAWarmPolicy(t *testing.T) {
	// The schedule is observable through the compaction time: a TTL at or
	// below the lead does not compact right after a Run.
	clock := newFakeClock()
	store := &tallyStore{Store: session.NewMemoryStore()}
	runner := warmInstance(t, store, "a", clock, testkit.EchoModel{}, service.WarmPolicy{TTL: 4 * time.Minute})
	first := runOn(t, runner, "", "one")
	runOn(t, runner, first.SessionID, "two")
	runOn(t, runner, first.SessionID, "three")
	before := store.Appends()
	clock.Advance(time.Minute)
	if store.Appends() != before {
		t.Fatal("the Session was compacted before half its short TTL")
	}
	clock.Advance(time.Minute)
	if store.Appends() == before {
		t.Fatal("the Session was not compacted at half its short TTL")
	}
	clock.Advance(2 * time.Minute)
	if runner.WarmSessions() != 0 {
		t.Fatal("the Session did not leave at its TTL")
	}
}
