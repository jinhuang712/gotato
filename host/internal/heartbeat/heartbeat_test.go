package heartbeat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/host/internal/clock"
	"github.com/jinhuang712/gotato/store"
)

// manual is a clock a test advances itself; its timers fire inside Advance, on
// the caller's goroutine, so every renewal has finished when Advance returns.
type manual struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	c       *manual
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func (m *manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *manual) AfterFunc(d time.Duration, f func()) clock.Timer {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := &manualTimer{c: m, at: m.now.Add(d), f: f}
	m.timers = append(m.timers, t)
	return t
}

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

func (m *manual) Advance(d time.Duration) {
	m.mu.Lock()
	target := m.now.Add(d)
	m.mu.Unlock()
	for {
		m.mu.Lock()
		var next *manualTimer
		for _, t := range m.timers {
			if !t.stopped && !t.fired && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			m.now = target
			m.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(m.now) {
			m.now = next.at
		}
		m.mu.Unlock()
		next.f()
	}
}

// flaky is a Liveness store a test can make refuse a renewal.
type flaky struct {
	store.Liveness
	down atomic.Bool
	// expire makes a renewal of the named row answer store.ErrExpired.
	expire atomic.Value
}

func (f *flaky) Beat(ctx context.Context, row store.InstanceRow, ttl time.Duration) error {
	if f.down.Load() {
		return errors.New("store is down")
	}
	if name, _ := f.expire.Load().(string); name != "" && name == row.ID && ttl > 0 {
		return store.ErrExpired
	}
	return f.Liveness.Beat(ctx, row, ttl)
}

type recorder struct {
	mu      sync.Mutex
	lapsed  []string
	renamed int
	beats   int
}

func (r *recorder) config(st store.Liveness, c clock.Clock) Config {
	return Config{
		Store: st, Clock: c, Label: "pod", Address: "http://pod:8080", TTL: 9 * time.Second,
		OnLapse:  func(name string) { r.mu.Lock(); r.lapsed = append(r.lapsed, name); r.mu.Unlock() },
		OnRename: func() { r.mu.Lock(); r.renamed++; r.mu.Unlock() },
		OnBeat:   func() { r.mu.Lock(); r.beats++; r.mu.Unlock() },
	}
}

func TestLiveWritesTheRowOnceAndRenewsItEveryThirdOfTheTTL(t *testing.T) {
	ctx := context.Background()
	c := &manual{now: time.Unix(1000, 0)}
	st := &flaky{Liveness: store.NewMemoryStore()}
	rec := &recorder{}
	row := New(rec.config(st, c))
	t.Cleanup(func() { _ = row.Close(ctx) })

	if state, _ := st.Instance(ctx, row.Name()); state.Alive {
		t.Fatal("a Row wrote its row before Live")
	}
	if row.Lapsed() {
		t.Fatal("a Row that has not written reports lapsed")
	}
	if err := row.Live(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := st.Instance(ctx, row.Name())
	if !state.Alive || state.Address != "http://pod:8080" {
		t.Fatalf("row = %+v, want alive with the address", state)
	}
	c.Advance(10 * time.Second)
	if rec.beats != 3 {
		t.Fatalf("%d renewals in 10s at a TTL of 9s, want 3", rec.beats)
	}
	if err := row.Guard(row.Name()); err != nil {
		t.Fatalf("Guard of a renewed row = %v", err)
	}
}

func TestGuardRefusesOnceRenewalsFailAndTellsOnce(t *testing.T) {
	ctx := context.Background()
	c := &manual{now: time.Unix(1000, 0)}
	st := &flaky{Liveness: store.NewMemoryStore()}
	rec := &recorder{}
	row := New(rec.config(st, c))
	t.Cleanup(func() { _ = row.Close(ctx) })
	if err := row.Live(ctx); err != nil {
		t.Fatal(err)
	}

	st.down.Store(true)
	c.Advance(9 * time.Second)
	for range 3 {
		if err := row.Guard(row.Name()); err != ErrLapsed {
			t.Fatalf("Guard = %v, want ErrLapsed", err)
		}
	}
	if !row.Lapsed() {
		t.Fatal("Lapsed = false with the renewals failing for a whole TTL")
	}
	if len(rec.lapsed) != 1 || rec.lapsed[0] != row.Name() {
		t.Fatalf("OnLapse calls = %v, want one for %s", rec.lapsed, row.Name())
	}

	st.down.Store(false)
	c.Advance(3 * time.Second)
	if err := row.Guard(row.Name()); err != nil {
		t.Fatalf("Guard after the store answered again = %v", err)
	}
}

func TestExpiredRowTakesANewNameAndRefusesTheOldOne(t *testing.T) {
	ctx := context.Background()
	c := &manual{now: time.Unix(1000, 0)}
	st := &flaky{Liveness: store.NewMemoryStore()}
	rec := &recorder{}
	row := New(rec.config(st, c))
	t.Cleanup(func() { _ = row.Close(ctx) })
	if err := row.Live(ctx); err != nil {
		t.Fatal(err)
	}
	old := row.Name()

	st.expire.Store(old)
	c.Advance(3 * time.Second)
	if row.Name() == old {
		t.Fatal("the Row kept its name after its row expired")
	}
	if rec.renamed != 1 {
		t.Fatalf("OnRename calls = %d, want 1", rec.renamed)
	}
	if err := row.Guard(old); err != ErrLapsed {
		t.Fatalf("Guard of the old name = %v, want ErrLapsed for good", err)
	}
	if err := row.Guard(row.Name()); err != nil {
		t.Fatalf("Guard of the new name = %v", err)
	}
	if state, _ := st.Instance(ctx, row.Name()); !state.Alive {
		t.Fatal("the new name has no live row")
	}
}

func TestMarkDrainingIsCarriedByTheRow(t *testing.T) {
	ctx := context.Background()
	c := &manual{now: time.Unix(1000, 0)}
	st := &flaky{Liveness: store.NewMemoryStore()}
	row := New((&recorder{}).config(st, c))
	t.Cleanup(func() { _ = row.Close(ctx) })

	// Before the first write there is no row to mark: the mark rides on the first renewal.
	if err := row.MarkDraining(ctx); err != nil {
		t.Fatal(err)
	}
	if err := row.Live(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.Instance(ctx, row.Name()); !state.Draining {
		t.Fatal("the row does not say draining")
	}
}

func TestCloseRemovesTheRowAndRefusesLaterWrites(t *testing.T) {
	ctx := context.Background()
	c := &manual{now: time.Unix(1000, 0)}
	st := &flaky{Liveness: store.NewMemoryStore()}
	rec := &recorder{}
	row := New(rec.config(st, c))
	if err := row.Live(ctx); err != nil {
		t.Fatal(err)
	}
	name := row.Name()
	if err := row.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := row.Close(ctx); err != nil {
		t.Fatalf("closing twice = %v", err)
	}
	if state, _ := st.Instance(ctx, name); state.Alive {
		t.Fatal("the row is alive after Close")
	}
	if err := row.Live(ctx); err != ErrClosed {
		t.Fatalf("Live after Close = %v, want ErrClosed", err)
	}
	before := rec.beats
	c.Advance(time.Minute)
	if rec.beats != before {
		t.Fatal("the renewals went on after Close")
	}
}

func TestLapsedAndNotRunIfLapsedTellTheGuardFromOtherConflicts(t *testing.T) {
	if !Lapsed(ErrLapsed) || !Lapsed(ErrLapsedNotRun) {
		t.Fatal("Lapsed missed the guard's own refusal")
	}
	if Lapsed(ErrClosed) || Lapsed(store.ErrConflict) || Lapsed(nil) {
		t.Fatal("Lapsed matched a refusal that is not the guard's")
	}
	if got := NotRunIfLapsed(ErrLapsed); got != ErrLapsedNotRun || !ErrLapsedNotRun.NotRun {
		t.Fatalf("NotRunIfLapsed = %v", got)
	}
	if got := NotRunIfLapsed(ErrClosed); got != ErrClosed {
		t.Fatalf("NotRunIfLapsed changed another error: %v", got)
	}
	if !errors.Is(ErrLapsed, store.ErrConflict) {
		t.Fatal("ErrLapsed does not match session.ErrConflict, so a lost-race check would miss it")
	}
}
