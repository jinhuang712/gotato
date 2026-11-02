package clock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// manual is a Clock a test advances itself; its timers fire inside Advance.
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

func (m *manual) AfterFunc(d time.Duration, f func()) Timer {
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

func TestEveryCallsAtItsInterval(t *testing.T) {
	c := &manual{now: time.Unix(0, 0)}
	calls := 0
	ticker := Every(c, 10*time.Second, func() { calls++ })
	c.Advance(35 * time.Second)
	if calls != 3 {
		t.Fatalf("%d calls in 35s at every 10s, want 3", calls)
	}
	ticker.Stop()
	c.Advance(time.Minute)
	if calls != 3 {
		t.Fatalf("%d calls after Stop, want 3", calls)
	}
	ticker.Stop() // twice is a no-op
}

func TestStopWaitsForTheCallThatIsRunning(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var finished bool
	ticker := Every(System(), time.Millisecond, func() {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		finished = true
	})
	<-started
	stopped := make(chan struct{})
	go func() { ticker.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a call was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-stopped
	if !finished {
		t.Fatal("Stop returned before the call ended")
	}
}

func TestSleepEndsWithTheClockOrTheContext(t *testing.T) {
	c := &manual{now: time.Unix(0, 0)}
	done := make(chan error, 1)
	go func() { done <- Sleep(context.Background(), c, time.Minute) }()
	for {
		c.mu.Lock()
		n := len(c.timers)
		c.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.Advance(time.Minute)
	if err := <-done; err != nil {
		t.Fatalf("Sleep = %v after the clock reached it", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, c, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep on a canceled ctx = %v", err)
	}
}

func TestSinceReadsTheClock(t *testing.T) {
	c := &manual{now: time.Unix(100, 0)}
	start := c.Now()
	c.Advance(90 * time.Second)
	if got := Since(c, start); got != 90*time.Second {
		t.Fatalf("Since = %v, want 90s", got)
	}
}
