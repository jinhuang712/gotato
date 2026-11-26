package host_test

import (
	"slices"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/host"
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
	after   time.Duration
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

func (c *fakeClock) AfterFunc(d time.Duration, f func()) host.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, at: c.now.Add(d), after: d, f: f}
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

// Pending counts the warm Sessions' timers that have neither fired nor been
// stopped. The Runner's renewals arm one every few seconds for as long as it
// lives; a timer of under a minute is not a warm Session's, and not counted.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired && timer.after >= time.Minute {
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
		// Keep only the timers that can still fire: the Runner's renewals arm
		// one every few seconds, and a long Advance would scan them all.
		c.timers = slices.DeleteFunc(c.timers, func(t *fakeTimer) bool { return t.stopped || t.fired })
		c.mu.Unlock()
		next.f()
	}
}
