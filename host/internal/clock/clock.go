// Package clock is the one clock of the host: every time the Runner reads
// or waits for goes through a Clock, so a test that supplies its own advances
// a TTL, a Liveness renewal and a wait together, and never sleeps.
//
// A Clock is Now and AfterFunc. The rest (Since, After, Sleep, Every) is built
// on those two here, so a fake clock implements two methods and nothing else.
// It is internal: the clock is not a seam for a user (decision 048).
package clock

import (
	"context"
	"sync"
	"time"
)

// Clock is the time the host waits by.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f once, in a goroutine of its own, after d.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending call of Clock.AfterFunc.
type Timer interface {
	// Stop prevents the call when it has not happened yet.
	Stop() bool
}

type system struct{}

func (system) Now() time.Time { return time.Now() }

func (system) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// System is the machine's clock.
func System() Clock { return system{} }

// Since is the time elapsed since t, by c.
func Since(c Clock, t time.Time) time.Duration { return c.Now().Sub(t) }

// After returns a channel that is closed after d, and a func that stops the
// wait and frees its timer when the channel is no longer wanted.
func After(c Clock, d time.Duration) (done <-chan struct{}, stop func()) {
	ch := make(chan struct{})
	timer := c.AfterFunc(d, func() { close(ch) })
	return ch, func() { timer.Stop() }
}

// Sleep waits d by c, or until ctx ends, and returns the error of ctx in that
// case.
func Sleep(ctx context.Context, c Clock, d time.Duration) error {
	done, stop := After(c, d)
	defer stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Ticker calls a func at an interval of a Clock until it is stopped.
type Ticker struct {
	mu      sync.Mutex
	clock   Clock
	every   time.Duration
	f       func()
	timer   Timer
	stopped bool
	running sync.WaitGroup
}

// Every calls f every d by c, the next call d after the last one returned, so
// calls never overlap. Stop ends it.
func Every(c Clock, d time.Duration, f func()) *Ticker {
	t := &Ticker{clock: c, every: d, f: f}
	t.mu.Lock()
	t.timer = c.AfterFunc(d, t.tick)
	t.mu.Unlock()
	return t
}

func (t *Ticker) tick() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.running.Add(1)
	t.mu.Unlock()
	defer t.running.Done()
	t.f()
	t.mu.Lock()
	if !t.stopped {
		t.timer = t.clock.AfterFunc(t.every, t.tick)
	}
	t.mu.Unlock()
}

// Stop ends the calls and returns once one that is running has returned. It
// is safe to call twice, and must not be called from f.
func (t *Ticker) Stop() {
	t.mu.Lock()
	t.stopped = true
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	t.running.Wait()
}
