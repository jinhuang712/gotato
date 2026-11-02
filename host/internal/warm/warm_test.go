package warm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/host/internal/clock"
)

// manual is a clock a test advances itself; its timers fire inside Advance.
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

func none(string) bool { return false }

func TestVictimIsTheLongestIdleThatIsNotBusy(t *testing.T) {
	c := &manual{now: time.Unix(0, 0)}
	cache := New(c)
	cache.Put("a", "agent", nil, Plan{TTL: time.Hour})
	c.Advance(time.Minute)
	cache.Put("b", "agent", nil, Plan{TTL: time.Hour})
	cache.Put("c", "agent", nil, Plan{TTL: time.Hour}) // idle at the same time as b, put after it

	if got := cache.Victim(none); got == nil || got.ID != "a" {
		t.Fatalf("Victim = %v, want a, the longest idle", got)
	}
	busy := func(id string) bool { return id == "a" }
	if got := cache.Victim(busy); got == nil || got.ID != "b" {
		t.Fatalf("Victim with a busy = %v, want b, which went idle before c at the same time", got)
	}
	if got := cache.Idle(busy); got != 2 {
		t.Fatalf("Idle = %d, want 2", got)
	}
	if got := cache.Victim(func(string) bool { return true }); got != nil {
		t.Fatalf("Victim with every Session busy = %v, want none", got)
	}
}

func TestPutReplacesAndStopsTheOlderCopy(t *testing.T) {
	c := &manual{now: time.Unix(0, 0)}
	cache := New(c)
	expired := map[*Entry]bool{}
	var mu sync.Mutex
	plan := Plan{TTL: time.Hour, OnExpire: func(e *Entry) { mu.Lock(); expired[e] = true; mu.Unlock() }}
	first := cache.Put("a", "agent", 1, plan)
	second := cache.Put("a", "agent", 2, plan)
	if cache.Len() != 1 || cache.Get("a") != second {
		t.Fatalf("the cache holds %d copies, want only the second", cache.Len())
	}
	c.Advance(time.Hour)
	mu.Lock()
	defer mu.Unlock()
	if expired[first] || !expired[second] {
		t.Fatalf("expired = %v: the replaced copy's timer must be stopped, the new one's must fire", expired)
	}
}

func TestIdleTimerIsArmedBeforeTheTTLOnly(t *testing.T) {
	c := &manual{now: time.Unix(0, 0)}
	cache := New(c)
	idle := 0
	cache.Put("a", "agent", nil, Plan{TTL: time.Hour, IdleAfter: 55 * time.Minute, OnIdle: func(*Entry) { idle++ }})
	cache.Put("b", "agent", nil, Plan{TTL: time.Hour, IdleAfter: 2 * time.Hour, OnIdle: func(*Entry) { idle += 10 }})
	c.Advance(56 * time.Minute)
	if idle != 1 {
		t.Fatalf("idle = %d, want only the first copy's compaction to be called", idle)
	}
}

func TestTakeAndRemoveStopTheTimersAndMarkTheEntryDead(t *testing.T) {
	c := &manual{now: time.Unix(0, 0)}
	cache := New(c)
	fired := false
	plan := Plan{TTL: time.Hour, OnExpire: func(*Entry) { fired = true }}
	entry := cache.Put("a", "agent", nil, plan)
	if got := cache.Take("a"); got != entry || cache.Len() != 0 {
		t.Fatalf("Take = %v", got)
	}
	c.Advance(2 * time.Hour)
	if fired {
		t.Fatal("the TTL of a taken copy fired")
	}
	if _, _, ok := entry.BeginIdle(); ok {
		t.Fatal("a dead entry began an idle compaction")
	}
	if cache.Remove(entry) {
		t.Fatal("Remove of a copy that is not the cached one reported true")
	}
	again := cache.Put("a", "agent", nil, plan)
	if !cache.Remove(again) || cache.Get("a") != nil {
		t.Fatal("Remove did not remove the cached copy")
	}
}

func TestClosedCacheKeepsNothing(t *testing.T) {
	cache := New(clock.System())
	cache.Close()
	if cache.Put("a", "agent", nil, Plan{TTL: time.Hour}) != nil || cache.Len() != 0 {
		t.Fatal("a closed cache kept a copy")
	}
}

func TestHoldCancelsTheIdleCompactionAndKeepsANewOneAway(t *testing.T) {
	cache := New(clock.System())
	entry := cache.Put("a", "agent", nil, Plan{TTL: time.Hour})
	ctx, end, ok := entry.BeginIdle()
	if !ok {
		t.Fatal("no idle compaction began")
	}
	if _, _, again := entry.BeginIdle(); again {
		t.Fatal("two idle compactions ran at once")
	}
	go func() {
		<-ctx.Done() // the compaction notices it was canceled and lets go
		end()
	}()
	unhold, err := entry.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("Hold did not cancel the idle compaction")
	}
	if _, _, held := entry.BeginIdle(); held {
		t.Fatal("an idle compaction began while a caller held the entry")
	}
	unhold()
	if _, end, ok := entry.BeginIdle(); !ok {
		t.Fatal("no idle compaction after the hold ended")
	} else {
		end()
	}
}

func TestHoldGivesUpWhenItsContextEnds(t *testing.T) {
	cache := New(clock.System())
	entry := cache.Put("a", "agent", nil, Plan{TTL: time.Hour})
	_, end, _ := entry.BeginIdle() // a compaction that never lets go
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := entry.Hold(ctx); err == nil {
		t.Fatal("Hold returned no error with its context canceled and a compaction running")
	}
	// The failed Hold released its hold: once the compaction ends, a new one may begin.
	end()
	if _, end2, ok := entry.BeginIdle(); !ok {
		t.Fatal("a failed Hold left the entry held")
	} else {
		end2()
	}
}
