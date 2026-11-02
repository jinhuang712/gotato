package runs

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/host/internal/clock"
	"github.com/jinhuang712/gotato/host/internal/warm"
)

type handle struct{ name string }

func newCache() *warm.Cache { return warm.New(clock.System()) }

func mustAdmit(t *testing.T, tb *Table[*handle], h *handle, id string, mem Memory, decide func(Counts) (func(), error)) func() {
	t.Helper()
	victim, release, err := tb.Admit(h, id, mem, decide)
	if victim != nil || err != nil {
		t.Fatalf("Admit(%s) = victim %v, err %v, want admitted", h.name, victim, err)
	}
	return release
}

func TestAdmitRegistersAndCounts(t *testing.T) {
	tb := New[*handle]()
	cache := newCache()
	mem := Memory{Cache: cache}
	a, b, c := &handle{"a"}, &handle{"b"}, &handle{"c"}
	mustAdmit(t, tb, a, "s1", mem, nil)
	mustAdmit(t, tb, b, "s1", mem, nil) // a second Run waiting on the same Session
	mustAdmit(t, tb, c, "", mem, nil)   // a Run that creates its Session
	mustAdmit(t, tb, &handle{"d"}, "", mem, nil)

	if tb.ActiveRuns() != 4 {
		t.Fatalf("ActiveRuns = %d, want 4", tb.ActiveRuns())
	}
	// Two Runs on s1 count once; each Run that creates a Session counts for one.
	if tb.ActiveSessions() != 3 {
		t.Fatalf("ActiveSessions = %d, want 3", tb.ActiveSessions())
	}
	if got := tb.Of("s1"); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("Of(s1) = %v, want a and b in order", got)
	}
	if len(tb.All()) != 4 {
		t.Fatalf("All = %d handles, want 4", len(tb.All()))
	}

	tb.Move(c, "", "s2")
	if len(tb.Of("s2")) != 1 || tb.ActiveSessions() != 3 {
		t.Fatalf("after Move: Of(s2) = %v, ActiveSessions %d", tb.Of("s2"), tb.ActiveSessions())
	}
	tb.Remove(a, "s1")
	tb.Remove(b, "s1")
	if len(tb.Of("s1")) != 0 || tb.ActiveSessions() != 2 {
		t.Fatalf("after Remove: Of(s1) = %v, ActiveSessions %d", tb.Of("s1"), tb.ActiveSessions())
	}
	for range 4 {
		tb.Done()
	}
	if tb.ActiveRuns() != 0 {
		t.Fatalf("ActiveRuns = %d after Done", tb.ActiveRuns())
	}
}

func TestDecideSeesTheCountsAndRefusesBeforeRegistering(t *testing.T) {
	tb := New[*handle]()
	cache := newCache()
	cache.Put("warm", "agent", nil, warm.Plan{TTL: time.Hour})
	mem := Memory{Cache: cache}
	mustAdmit(t, tb, &handle{"a"}, "s1", mem, nil)

	var seen Counts
	released := false
	release := mustAdmit(t, tb, &handle{"b"}, "s2", mem, func(c Counts) (func(), error) {
		seen = c
		return func() { released = true }, nil
	})
	if seen != (Counts{ActiveRuns: 1, ActiveSessions: 1, WarmSessions: 1}) {
		t.Fatalf("decide saw %+v, want one Run, one Session and one warm copy: the Run itself is not counted", seen)
	}
	release()
	if !released {
		t.Fatal("the release decide returned is not the one Admit returned")
	}

	refusal := errors.New("no")
	victim, rel, err := tb.Admit(&handle{"c"}, "s3", mem, func(Counts) (func(), error) { return nil, refusal })
	if victim != nil || rel != nil || !errors.Is(err, refusal) {
		t.Fatalf("Admit with a refusing decide = victim %v, release set %v, err %v", victim, rel != nil, err)
	}
	if tb.ActiveRuns() != 2 || len(tb.Of("s3")) != 0 {
		t.Fatalf("a refused Run was registered: ActiveRuns %d", tb.ActiveRuns())
	}
}

func TestMemoryBoundEvictsOrRefusesBeforeDecide(t *testing.T) {
	tb := New[*handle]()
	cache := newCache()
	mem := Memory{MaxSessions: 2, Cache: cache}
	decided := 0
	decide := func(Counts) (func(), error) { decided++; return nil, nil }

	mustAdmit(t, tb, &handle{"a"}, "s1", mem, decide)
	cache.Put("idle", "agent", nil, warm.Plan{TTL: time.Hour})

	// At the limit (one active, one warm), a new Session first evicts the warm one.
	victim, _, err := tb.Admit(&handle{"b"}, "s2", mem, decide)
	if err != nil || victim == nil || victim.ID != "idle" {
		t.Fatalf("Admit at the limit = victim %v, err %v, want the warm copy to evict", victim, err)
	}
	if decided != 1 || tb.ActiveRuns() != 1 {
		t.Fatalf("decide ran %d times and %d Runs are admitted: nothing registers while a copy is to be evicted", decided, tb.ActiveRuns())
	}
	cache.Remove(victim)
	mustAdmit(t, tb, &handle{"b"}, "s2", mem, decide)

	// Every Session has a Run now: refused, and decide is not asked.
	if _, _, err := tb.Admit(&handle{"c"}, "s3", mem, decide); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("Admit with every Session running = %v, want ErrOverloaded", err)
	}
	if decided != 2 {
		t.Fatalf("decide ran %d times, want only for the two admitted Runs", decided)
	}
	// A Run on a Session that is active already adds nothing to the count.
	mustAdmit(t, tb, &handle{"d"}, "s1", mem, decide)
	// A warm copy of the Session itself is not evicted for its own Run.
	cache.Put("s9", "agent", nil, warm.Plan{TTL: time.Hour})
	tb2 := New[*handle]()
	mustAdmit(t, tb2, &handle{"e"}, "s9", Memory{MaxSessions: 1, Cache: cache}, nil)
}

func TestVictimOverLimitSkipsSessionsWithARun(t *testing.T) {
	tb := New[*handle]()
	cache := newCache()
	cache.Put("busy", "agent", nil, warm.Plan{TTL: time.Hour})
	cache.Put("idle", "agent", nil, warm.Plan{TTL: time.Hour})
	mustAdmit(t, tb, &handle{"a"}, "busy", Memory{Cache: cache}, nil)

	if tb.Victim(cache, 2) != nil || tb.Victim(cache, -1) != nil {
		t.Fatal("a victim within the limit, or with no limit")
	}
	if got := tb.Victim(cache, 1); got == nil || got.ID != "idle" {
		t.Fatalf("Victim = %v, want idle: the Session with a Run is never the victim", got)
	}
}

func TestDrainRefusesAndWaitsForRunsAndLockedCallers(t *testing.T) {
	tb := New[*handle]()
	mem := Memory{Cache: newCache()}
	if tb.Drained() != nil || tb.Draining() {
		t.Fatal("a table drains before Drain")
	}
	mustAdmit(t, tb, &handle{"a"}, "s1", mem, nil)
	endLocked := tb.Locked()

	drained, first := tb.Drain()
	again, second := tb.Drain()
	if !first || second || drained != again || !tb.Draining() || tb.Drained() != drained {
		t.Fatal("Drain does not start once and hand out the same channel")
	}
	if _, _, err := tb.Admit(&handle{"b"}, "s2", mem, nil); !errors.Is(err, ErrDraining) {
		t.Fatalf("Admit while draining = %v, want ErrDraining", err)
	}

	waitClosed := func() bool {
		select {
		case <-drained:
			return true
		case <-time.After(30 * time.Millisecond):
			return false
		}
	}
	if waitClosed() {
		t.Fatal("drained while a Run and a locked caller are in flight")
	}
	tb.Done()
	if waitClosed() {
		t.Fatal("drained while a caller still holds a Session lock")
	}
	endLocked()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("not drained after the last Run and caller ended")
	}
}

// Admission is atomic: a limit that decide enforces by the counts never lets
// more Runs through than it allows, however many ask at once.
func TestConcurrentAdmitHonoursALimitOfDecide(t *testing.T) {
	tb := New[*handle]()
	mem := Memory{Cache: newCache()}
	const limit, askers = 5, 50
	var admitted, refused int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range askers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := tb.Admit(&handle{}, "", mem, func(c Counts) (func(), error) {
				if c.ActiveRuns >= limit {
					return nil, errors.New("full")
				}
				return nil, nil
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				refused++
			} else {
				admitted++
			}
			_ = i
		}()
	}
	wg.Wait()
	if admitted != limit || refused != askers-limit || tb.ActiveRuns() != limit {
		t.Fatalf("admitted %d, refused %d, in flight %d, want %d, %d, %d", admitted, refused, tb.ActiveRuns(), limit, askers-limit, limit)
	}
}
