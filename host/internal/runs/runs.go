// Package runs is the table of the Runs an Instance has admitted: which
// Sessions they are on, how many there are, and the wait of a drain.
//
// A Table is generic over the handle of a Run, which it only stores and
// hands back; what a handle can do is the Runner's. It holds one lock, mu,
// and admission, registration and the counts all happen under it, so a limit
// counts a Run the moment it is admitted and Drain never misses one.
//
// Lock order: runs, then warm. Admit and Victim ask the warm cache while they
// hold the table's lock; nothing in warm calls into the table. The Admitter's
// decision is made under the table's lock too, so its Counts and the Run it
// admits are one step: an Admitter must not block or call the Runner.
package runs

import (
	"errors"
	"sync"

	"github.com/jinhuang712/gotato/host/internal/warm"
)

// ErrDraining refuses a Run because the Instance drains.
var ErrDraining = errors.New("runs: draining")

// ErrOverloaded refuses a Run that would activate a Session past the memory
// bound when no warm copy can give room.
var ErrOverloaded = errors.New("runs: at the limit of active sessions")

// Counts is what an Instance holds, as the table sees it.
type Counts struct {
	// ActiveRuns is the Runs admitted, and ActiveSessions the Sessions they
	// are on, each counted once. WarmSessions is the idle copies.
	ActiveRuns     int
	ActiveSessions int
	WarmSessions   int
}

// Memory is the bound on the Sessions an Instance keeps, with the cache whose
// copies count toward it.
type Memory struct {
	// MaxSessions is the hard limit on the Sessions the Instance keeps: those
	// with an admitted Run and the warm ones without. Zero means no limit.
	MaxSessions int
	Cache       *warm.Cache
}

// Table is the admitted Runs of one Instance.
type Table[H comparable] struct {
	mu       sync.Mutex
	handles  map[string][]H
	inflight int
	// locked counts the callers inside the Session lock, Runs among them:
	// Compact, Notice, SetToolActive and DeleteSession hold a Claim without
	// being admitted as Runs, and a drain waits for them too.
	locked   int
	draining bool
	// drained is made by the first Drain and closed once every Run admitted
	// before it has settled and released its Session.
	drained chan struct{}
	idle    *sync.Cond
}

// New returns an empty table.
func New[H comparable]() *Table[H] {
	t := &Table[H]{handles: map[string][]H{}}
	t.idle = sync.NewCond(&t.mu)
	return t
}

// Admit is one pass of admission for the Run h on session id ("" for a Run
// that creates one), under the table's lock, in this order:
//
//  1. a draining Instance refuses (ErrDraining);
//  2. the memory bound: a Run that would activate a Session past
//     mem.MaxSessions is refused (ErrOverloaded), unless a warm copy can be
//     evicted to make room, in which case Admit registers nothing and returns
//     that copy, for the caller to drop and Admit again;
//  3. decide, the Admitter's policy, is given the Instance's counts and may
//     refuse; its release is returned for the caller to run when the Run ends.
//
// Only then is the Run registered and counted. decide may be nil.
func (t *Table[H]) Admit(h H, id string, mem Memory, decide func(Counts) (release func(), err error)) (victim *warm.Entry, release func(), err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.draining {
		return nil, nil, ErrDraining
	}
	if limit := mem.MaxSessions; limit > 0 {
		has := mem.Cache.Get(id) != nil
		evictable := mem.Cache.Victim(t.busy)
		if (id == "" || (len(t.handles[id]) == 0 && !has)) && t.liveLocked(mem.Cache) >= limit {
			if evictable == nil {
				return nil, nil, ErrOverloaded
			}
			return evictable, nil, nil
		}
	}
	if decide != nil {
		counts := Counts{ActiveRuns: t.inflight, ActiveSessions: t.activeLocked()}
		if mem.Cache != nil {
			counts.WarmSessions = mem.Cache.Len()
		}
		if release, err = decide(counts); err != nil {
			return nil, nil, err
		}
	}
	t.inflight++
	t.handles[id] = append(t.handles[id], h)
	return nil, release, nil
}

// busy says a Session has an admitted Run. The caller holds the lock.
func (t *Table[H]) busy(id string) bool { return len(t.handles[id]) > 0 }

// liveLocked counts the Sessions the Instance keeps: those with an admitted
// Run, and the warm ones without.
func (t *Table[H]) liveLocked(cache *warm.Cache) int {
	n := t.activeLocked()
	if cache != nil {
		n += cache.Idle(t.busy)
	}
	return n
}

// activeLocked counts the Sessions with an admitted Run. Runs that create a
// Session have no ID yet, and each counts as one Session of its own.
func (t *Table[H]) activeLocked() int {
	n := len(t.handles)
	if fresh := len(t.handles[""]); fresh > 1 {
		n += fresh - 1
	}
	return n
}

// Victim returns the warm copy to evict when the cache holds more than limit
// (a negative limit is none), skipping the Sessions with an admitted Run, or
// nil.
func (t *Table[H]) Victim(cache *warm.Cache, limit int) *warm.Entry {
	if limit < 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cache.Len() <= limit {
		return nil
	}
	return cache.Victim(t.busy)
}

// Done ends the count of one admitted Run, after the Run has released its
// Session.
func (t *Table[H]) Done() {
	t.mu.Lock()
	t.inflight--
	if t.inflight == 0 {
		t.idle.Broadcast()
	}
	t.mu.Unlock()
}

// Locked counts a caller that takes, or waits for, a Session lock; the
// returned func ends it. A drain waits until none is left.
func (t *Table[H]) Locked() func() {
	t.mu.Lock()
	t.locked++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		t.locked--
		if t.locked == 0 {
			t.idle.Broadcast()
		}
		t.mu.Unlock()
	}
}

// Of returns the Runs admitted on session id.
func (t *Table[H]) Of(id string) []H {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]H(nil), t.handles[id]...)
}

// All returns every admitted Run.
func (t *Table[H]) All() []H {
	t.mu.Lock()
	defer t.mu.Unlock()
	var all []H
	for _, list := range t.handles {
		all = append(all, list...)
	}
	return all
}

// Move re-registers h from session from to session to: a Run is admitted
// before its Session is loaded or created, so the ID it turned out to own is
// attached afterwards.
func (t *Table[H]) Move(h H, from, to string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if from == to {
		return
	}
	t.removeLocked(h, from)
	t.handles[to] = append(t.handles[to], h)
}

// Remove unregisters h from session id.
func (t *Table[H]) Remove(h H, id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removeLocked(h, id)
}

func (t *Table[H]) removeLocked(h H, id string) {
	list := t.handles[id]
	for i, candidate := range list {
		if candidate == h {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(t.handles, id)
	} else {
		t.handles[id] = list
	}
}

// ActiveRuns is the number of Runs admitted.
func (t *Table[H]) ActiveRuns() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inflight
}

// ActiveSessions is the number of Sessions with an admitted Run.
func (t *Table[H]) ActiveSessions() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeLocked()
}

// Draining reports whether the table refuses new Runs.
func (t *Table[H]) Draining() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.draining
}

// Drain makes the table refuse new Runs, and returns a channel closed once
// every Run admitted before, and every caller of Locked, is done. The first
// call starts the wait and reports first; a later one returns the same
// channel.
func (t *Table[H]) Drain() (done <-chan struct{}, first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.draining = true
	if t.drained != nil {
		return t.drained, false
	}
	drained := make(chan struct{})
	t.drained = drained
	go func() {
		t.mu.Lock()
		for t.inflight > 0 || t.locked > 0 {
			t.idle.Wait()
		}
		t.mu.Unlock()
		close(drained)
	}()
	return drained, true
}

// Drained returns the channel Drain made, or nil before the first Drain.
func (t *Table[H]) Drained() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.drained
}
