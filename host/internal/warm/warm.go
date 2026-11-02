// Package warm is the cache of the idle Sessions an Instance keeps in memory
// after a Run (decision 036): each is a copy of a Record, never a lock, with a
// TTL timer and an optional timer for an idle compaction.
//
// The cache knows no Session, Agent or Writer: an Entry carries what the
// Runner puts in it as Value. It does one job per call, and evicts in one
// place, Victim: the admission of a Run (the memory bound) and the trim after
// a Run (the bound on warm copies) both ask it.
//
// Lock order: the Runner's runs table, then this cache. A call here never
// calls back into the table, so a caller may hold the table's lock.
package warm

import (
	"context"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/host/internal/clock"
)

// Entry is the copy of one idle Session. It holds no Claim.
type Entry struct {
	// ID is the Session's ID and Agent the name of its Agent.
	ID    string
	Agent string
	// Value is what the Runner keeps with the copy.
	Value any

	timers []clock.Timer
	// since is when the Session went idle, and seq orders copies that went
	// idle at the same time; eviction takes the least of them first.
	since time.Time
	seq   uint64

	mu sync.Mutex
	// holds counts the callers about to use the Session, so no idle
	// compaction starts meanwhile; idle is the idle compaction in progress.
	holds int
	idle  *idleRun
	dead  bool
}

// idleRun is an idle compaction in progress. A caller that needs the Session
// cancels it and waits for done.
type idleRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Plan says when a copy leaves memory and when it is compacted while it idles.
type Plan struct {
	// TTL is how long the copy stays.
	TTL time.Duration
	// OnExpire is called, in a goroutine of its own, when the TTL ends.
	OnExpire func(*Entry)
	// OnIdle, when set, is called in a goroutine of its own IdleAfter after
	// the copy was put, unless that is not before the TTL ends.
	OnIdle    func(*Entry)
	IdleAfter time.Duration
}

// Cache holds the warm copies of one Instance.
type Cache struct {
	clock clock.Clock

	mu      sync.Mutex
	entries map[string]*Entry
	closed  bool
	seq     uint64
}

// New returns an empty cache that reads and waits by c.
func New(c clock.Clock) *Cache {
	return &Cache{clock: c, entries: map[string]*Entry{}}
}

// Len is the number of copies.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Get returns the copy of Session id, or nil.
func (c *Cache) Get(id string) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[id]
}

// Entries returns every copy.
func (c *Cache) Entries() []*Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Entry, 0, len(c.entries))
	for _, entry := range c.entries {
		out = append(out, entry)
	}
	return out
}

// Put keeps a copy of Session id for plan.TTL, replacing and stopping an older
// one of the same Session. A cache that is closed keeps none: Put returns nil.
func (c *Cache) Put(id, agent string, value any, plan Plan) *Entry {
	entry := &Entry{ID: id, Agent: agent, Value: value, since: c.clock.Now()}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.seq++
	entry.seq = c.seq
	if old := c.entries[id]; old != nil {
		old.stopTimers()
	}
	c.entries[id] = entry
	if plan.OnExpire != nil {
		entry.timers = append(entry.timers, c.clock.AfterFunc(plan.TTL, func() { plan.OnExpire(entry) }))
	}
	if plan.OnIdle != nil && plan.IdleAfter < plan.TTL {
		entry.timers = append(entry.timers, c.clock.AfterFunc(plan.IdleAfter, func() { plan.OnIdle(entry) }))
	}
	return entry
}

// Take removes the Session's copy, if there is one, and stops its timers. The
// caller holds the Session lock, so no idle compaction is running on it.
func (c *Cache) Take(id string) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[id]
	if entry == nil {
		return nil
	}
	c.drop(entry)
	return entry
}

// Remove removes entry if it is still its Session's copy, and says so.
func (c *Cache) Remove(entry *Entry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries[entry.ID] != entry {
		return false
	}
	c.drop(entry)
	return true
}

func (c *Cache) drop(entry *Entry) {
	delete(c.entries, entry.ID)
	entry.stopTimers()
	entry.mu.Lock()
	entry.dead = true
	entry.mu.Unlock()
}

// Close says the Runner has let every copy go: from now on Put keeps none. It
// removes none itself, since dropping a copy needs its idle compaction
// canceled first, which the Runner does.
func (c *Cache) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

// Victim returns the copy to evict: the longest-idle one whose Session busy
// does not name, and nil when there is none. Eviction is chosen here and
// nowhere else; the caller drops the copy it returns (it is not removed).
func (c *Cache) Victim(busy func(id string) bool) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var victim *Entry
	for _, entry := range c.entries {
		if busy(entry.ID) {
			continue
		}
		if victim == nil || entry.since.Before(victim.since) || (entry.since.Equal(victim.since) && entry.seq < victim.seq) {
			victim = entry
		}
	}
	return victim
}

// Idle counts the copies whose Session busy does not name.
func (c *Cache) Idle(busy func(id string) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for id := range c.entries {
		if !busy(id) {
			n++
		}
	}
	return n
}

func (e *Entry) stopTimers() {
	for _, timer := range e.timers {
		timer.Stop()
	}
	e.timers = nil
}

// CancelIdle cancels the entry's idle compaction and waits until it has let go
// of the Writer.
func (e *Entry) CancelIdle() {
	e.mu.Lock()
	run := e.idle
	e.mu.Unlock()
	if run != nil {
		run.cancel()
		<-run.done
	}
}

// Hold is called before a caller takes the Session lock. It cancels an idle
// compaction of the Session, waits for it to finish, and keeps a new one from
// starting until the returned func is called, so a message that arrives while
// the Session compacts is served first and never finds it busy. It returns
// ctx's error when ctx ends while it waits.
func (e *Entry) Hold(ctx context.Context) (unhold func(), err error) {
	e.mu.Lock()
	e.holds++
	run := e.idle
	e.mu.Unlock()
	unhold = func() {
		e.mu.Lock()
		e.holds--
		e.mu.Unlock()
	}
	if run != nil {
		run.cancel()
		select {
		case <-run.done:
		case <-ctx.Done():
			unhold()
			return nil, ctx.Err()
		}
	}
	return unhold, nil
}

// BeginIdle makes the entry's idle compaction: it returns the context the
// compaction runs under, canceled by CancelIdle and Hold, and the func that
// ends it. It reports false, and starts nothing, when a caller holds the
// entry, the entry is dead, or another idle compaction is going.
func (e *Entry) BeginIdle() (ctx context.Context, end func(), ok bool) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &idleRun{cancel: cancel, done: make(chan struct{})}
	e.mu.Lock()
	if e.holds > 0 || e.dead || e.idle != nil {
		e.mu.Unlock()
		cancel()
		return nil, nil, false
	}
	e.idle = run
	e.mu.Unlock()
	return ctx, func() {
		e.mu.Lock()
		e.idle = nil
		e.mu.Unlock()
		cancel()
		close(run.done)
	}, true
}
