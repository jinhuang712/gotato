package service

import (
	"context"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// Defaults of a warm Session (WarmPolicy).
const (
	// defaultWarmTTL is how long an idle Session stays in memory.
	defaultWarmTTL = 60 * time.Minute
	// defaultIdleLead is how long before its cache expires, or before it
	// leaves memory, an idle Session is compacted: a compaction can take a
	// minute or more, and its Recap should read a warm cache.
	defaultIdleLead = 5 * time.Minute
	// defaultIdleMinTokens is the smallest stretch since the last Checkpoint
	// that an idle Session is compacted for: below it the model call costs
	// more than it saves.
	defaultIdleMinTokens = 20000
)

// WarmPolicy says how long a Session of an Agent stays in memory after its
// Run, and whether it is compacted while it idles. The zero value is the
// default: 60 minutes, and an idle compaction shortly before the prompt cache
// expires or the Session leaves memory.
//
// A warm Session is a cache, never a lock: it holds no Claim while it idles,
// so another Instance can take the Session over, and the copy is dropped. It
// also counts toward Admission.MaxActiveSessions and is the first thing evicted
// at the limit, longest idle first, without a compaction.
type WarmPolicy struct {
	// TTL is how long an idle Session stays in memory; the clock starts when
	// a Run ends and starts again with the next one. Zero means
	// 60 minutes. A negative TTL keeps no Session warm: each Run loads it
	// and lets it go.
	TTL time.Duration
	// NoIdleCompaction turns off the compaction of an idle Session.
	NoIdleCompaction bool
	// IdleLead is how long before the cache expires, or before the Session
	// leaves memory, the idle compaction runs. Zero means 5 minutes. It
	// is at most half the TTL.
	IdleLead time.Duration
	// IdleMinTokens is the smallest stretch, estimated in tokens, since the
	// last Checkpoint that an idle Session is compacted for. Zero means
	// 20000 tokens; a negative value compacts any stretch.
	IdleMinTokens int
}

func (p WarmPolicy) ttl() time.Duration {
	if p.TTL == 0 {
		return defaultWarmTTL
	}
	return p.TTL
}

func (p WarmPolicy) lead() time.Duration {
	if p.IdleLead <= 0 {
		return defaultIdleLead
	}
	return p.IdleLead
}

func (p WarmPolicy) minTokens() int {
	switch {
	case p.IdleMinTokens == 0:
		return defaultIdleMinTokens
	case p.IdleMinTokens < 0:
		return 0
	}
	return p.IdleMinTokens
}

// idleSchedule says when, after a Run ends, the idle compaction runs and why.
// The cache's retention counts only when it is well above the lead (at least
// twice): with a retention of 5 minutes less the lead is 0, and the Session
// would be compacted the moment its Run ends, so a user who answers a minute
// later would pay to write a new prefix instead of reading a warm one. The
// compaction then runs before the Session leaves memory. The lead is at most
// half the TTL, so a short TTL does not compact right after a Run.
func (p WarmPolicy) idleSchedule(retention time.Duration) (after time.Duration, why gotato.CompactionWhy, on bool) {
	if p.NoIdleCompaction {
		return 0, "", false
	}
	ttl := p.ttl()
	lead := min(p.lead(), ttl/2)
	after, why = ttl-lead, gotato.CompactionWhyBeforeUnload
	if retention >= 2*p.lead() && retention-lead < after {
		after, why = retention-lead, gotato.CompactionWhyBeforeCacheExpires
	}
	return after, why, true
}

// clock is the time a Runner waits by. The default is the system clock; a test
// supplies one it advances itself, so no test waits for a TTL.
type clock interface {
	Now() time.Time
	// AfterFunc calls f once, in a goroutine of its own, after d.
	AfterFunc(d time.Duration, f func()) timer
}

// timer is a pending call of clock.AfterFunc.
type timer interface {
	// Stop prevents the call when it has not happened yet.
	Stop() bool
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) AfterFunc(d time.Duration, f func()) timer { return time.AfterFunc(d, f) }

// warmEntry is the live copy of one idle Session: its Record and the Writer
// that wrote it, kept in memory after a Run. It holds no Claim.
type warmEntry struct {
	id     string
	agent  string
	spec   Agent
	writer *session.Writer
	timers []timer
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

// WarmSessions reports the idle Sessions this Instance holds in memory.
func (r *Runner) WarmSessions() int {
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	return len(r.warm)
}

// warmCountLocked tells whether id has a warm copy, and returns the copy to
// evict: the longest-idle one whose Session has no admitted Run. The caller
// holds r.mu.
func (r *Runner) warmCountLocked(id string) (warm bool, victim *warmEntry) {
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	_, warm = r.warm[id]
	for _, entry := range r.warm {
		if len(r.handles[entry.id]) > 0 {
			continue
		}
		if victim == nil || entry.since.Before(victim.since) || (entry.since.Equal(victim.since) && entry.seq < victim.seq) {
			victim = entry
		}
	}
	return warm, victim
}

// liveSessionsLocked counts the Sessions this Instance keeps: those with an
// admitted Run, and the warm ones without. The caller holds r.mu.
func (r *Runner) liveSessionsLocked() int {
	n := r.activeSessionsLocked()
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	for id := range r.warm {
		if len(r.handles[id]) == 0 {
			n++
		}
	}
	return n
}

// keepWarm keeps the Session writer wrote, released and healthy, in memory for
// the Agent's TTL, and arms its timers. A Writer that failed, a draining or
// closed Runner and a negative TTL keep nothing.
func (r *Runner) keepWarm(writer *session.Writer, spec Agent) {
	ttl := spec.Warm.ttl()
	if ttl < 0 || writer == nil || writer.Failed() != nil {
		return
	}
	entry := &warmEntry{id: writer.ID(), agent: spec.Name, spec: spec, writer: writer, since: r.clock.Now()}
	r.mu.Lock()
	draining := r.draining
	r.mu.Unlock()
	r.warmMu.Lock()
	if draining || r.warmClosed {
		r.warmMu.Unlock()
		return
	}
	r.warmSeq++
	entry.seq = r.warmSeq
	if old := r.warm[entry.id]; old != nil {
		old.stopTimers()
	}
	r.warm[entry.id] = entry
	entry.timers = append(entry.timers, r.clock.AfterFunc(ttl, func() { r.expire(entry) }))
	if after, why, on := spec.Warm.idleSchedule(modelctx.RetentionOf(spec.Model)); on && after < ttl {
		entry.timers = append(entry.timers, r.clock.AfterFunc(after, func() { r.idleCompact(entry, why) }))
	}
	r.warmMu.Unlock()
	r.trimWarm()
}

// trimWarm evicts the longest-idle warm Sessions until the Instance holds no
// more than Admission.MaxWarmSessions. A Session with an admitted Run is never
// the victim.
func (r *Runner) trimWarm() {
	limit := r.admission.MaxWarmSessions
	if limit < 0 {
		return
	}
	for {
		r.mu.Lock()
		r.warmMu.Lock()
		var victim *warmEntry
		if len(r.warm) > limit {
			for _, entry := range r.warm {
				if len(r.handles[entry.id]) > 0 {
					continue
				}
				if victim == nil || entry.since.Before(victim.since) || (entry.since.Equal(victim.since) && entry.seq < victim.seq) {
					victim = entry
				}
			}
		}
		r.warmMu.Unlock()
		r.mu.Unlock()
		if victim == nil {
			return
		}
		r.dropEntry(victim, "evicted")
	}
}

func (e *warmEntry) stopTimers() {
	for _, timer := range e.timers {
		timer.Stop()
	}
	e.timers = nil
}

// takeWarm removes the Session's warm copy from the cache, if there is one,
// and stops its timers. The caller holds the Session lock, so no idle
// compaction is running on it.
func (r *Runner) takeWarm(id string) *warmEntry {
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	entry := r.warm[id]
	if entry == nil {
		return nil
	}
	delete(r.warm, id)
	entry.stopTimers()
	entry.mu.Lock()
	entry.dead = true
	entry.mu.Unlock()
	return entry
}

// removeWarm removes entry if it is still the Session's warm copy, and says so.
func (r *Runner) removeWarm(entry *warmEntry) bool {
	r.warmMu.Lock()
	defer r.warmMu.Unlock()
	if r.warm[entry.id] != entry {
		return false
	}
	delete(r.warm, entry.id)
	entry.stopTimers()
	entry.mu.Lock()
	entry.dead = true
	entry.mu.Unlock()
	return true
}

// unloaded tells the Instance's subscribers that a Session left memory.
func (r *Runner) unloaded(id, agent, why string) {
	r.notice(gotato.SessionUnloaded{Header: gotato.NewHeader(gotato.SessionID(id), "", 0), Why: why}, agent)
}

// dropWarm lets go of a Session's warm copy, with why, when this Instance has
// one. The caller holds the Session lock.
func (r *Runner) dropWarm(id, why string) {
	if entry := r.takeWarm(id); entry != nil {
		r.unloaded(id, entry.agent, why)
	}
}

// dropAllWarm lets go of every warm copy, with why, canceling any idle
// compaction first. An idle copy holds no Claim, so nothing is released.
func (r *Runner) dropAllWarm(why string) {
	r.warmMu.Lock()
	entries := make([]*warmEntry, 0, len(r.warm))
	for _, entry := range r.warm {
		entries = append(entries, entry)
	}
	r.warmMu.Unlock()
	for _, entry := range entries {
		r.dropEntry(entry, why)
	}
}

// dropEntry cancels entry's idle compaction, waits for it to let go of the
// Writer, and drops the copy, if it is still the cached one.
func (r *Runner) dropEntry(entry *warmEntry, why string) {
	entry.cancelIdle()
	if r.removeWarm(entry) {
		r.unloaded(entry.id, entry.agent, why)
	}
}

// expire is the TTL timer: the idle Session leaves memory.
func (r *Runner) expire(entry *warmEntry) { r.dropEntry(entry, "ttl") }

// cancelIdle cancels the entry's idle compaction and waits until it has let go
// of the Writer.
func (e *warmEntry) cancelIdle() {
	e.mu.Lock()
	run := e.idle
	e.mu.Unlock()
	if run != nil {
		run.cancel()
		<-run.done
	}
}

// holdWarm is called before a caller takes the Session lock. It cancels an
// idle compaction of the Session, waits for it to finish, and keeps a new one
// from starting until the returned function is called, so a message that
// arrives while the Session compacts is served first and never finds it busy.
func (r *Runner) holdWarm(ctx context.Context, id string) (func(), error) {
	r.warmMu.Lock()
	entry := r.warm[id]
	r.warmMu.Unlock()
	if entry == nil {
		return func() {}, nil
	}
	entry.mu.Lock()
	entry.holds++
	run := entry.idle
	entry.mu.Unlock()
	unhold := func() {
		entry.mu.Lock()
		entry.holds--
		entry.mu.Unlock()
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

// idleCompact is the warm Session's own timer: it compacts the idle Session
// with the compaction a service's timer calls (Runner.Compact with
// min_tokens), shortly before its prompt cache expires or it leaves memory. A
// caller that needs the Session cancels it (holdWarm) and is served first. A
// Session whose Record another Instance wrote is dropped instead, and one whose
// stretch is below the minimum is left alone, with no write. A compaction
// that cannot be written leaves the copy ahead of the store, so the copy is
// dropped and the next Run loads the Session again.
func (r *Runner) idleCompact(entry *warmEntry, why gotato.CompactionWhy) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &idleRun{cancel: cancel, done: make(chan struct{})}
	if !entry.beginIdle(run) {
		return
	}
	defer entry.endIdle(run)
	// Never wait for the Session: whoever holds it is using it.
	if err := r.locks.acquire(ctx, entry.id, false, nil); err != nil {
		return
	}
	defer r.locks.release(entry.id)
	r.compactIdleLocked(ctx, entry, why)
}

// beginIdle makes run the entry's idle compaction, unless a caller holds the
// entry, it is dead, or another idle compaction is going. It reports whether
// run was installed.
func (e *warmEntry) beginIdle(run *idleRun) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.holds > 0 || e.dead || e.idle != nil {
		return false
	}
	e.idle = run
	return true
}

// endIdle removes run from the entry and tells whoever waits for it.
func (e *warmEntry) endIdle(run *idleRun) {
	e.mu.Lock()
	e.idle = nil
	e.mu.Unlock()
	close(run.done)
}

// compactIdleLocked is idleCompact once it holds the Session's lock: check
// that the copy is still the Session's, resume its Writer, compact, release.
func (r *Runner) compactIdleLocked(ctx context.Context, entry *warmEntry, why gotato.CompactionWhy) {
	r.warmMu.Lock()
	current := r.warm[entry.id] == entry
	r.warmMu.Unlock()
	if !current || ctx.Err() != nil {
		return
	}
	writer := entry.writer
	if stale, err := writer.Stale(ctx); err != nil || stale {
		if ctx.Err() == nil && r.removeWarm(entry) {
			r.unloaded(entry.id, entry.agent, "taken_over")
		}
		return
	}
	record := writer.Record()
	minTokens := entry.spec.Warm.minTokens()
	if minTokens > 0 && modelctx.EstimateTokens(record.Tail()) < minTokens {
		return
	}
	if err := writer.Resume(ctx); err != nil {
		if r.removeWarm(entry) {
			reason := "failed"
			if lostRace(err) {
				reason = "taken_over"
			}
			r.noteFailure(err, entry.id, entry.agent)
			r.unloaded(entry.id, entry.agent, reason)
		}
		return
	}
	opts := compactOptionsFor(entry.spec, record, modelctx.CompactConfig{MinTokens: minTokens})
	compactOpts := []modelctx.CompactOption{modelctx.WithCompactionWhy(why)}
	if opts.Recap != nil {
		compactOpts = append(compactOpts, modelctx.WithRecapContext(r.recapContext(entry.spec, record)))
	}
	// The result needs no report: the compaction's Events say what it did.
	_, _ = modelctx.Compact(ctx, record, opts, compactOpts...)
	saveCtx, cancelSave := persistContext(ctx)
	releaseErr := writer.Release(saveCtx)
	cancelSave()
	if releaseErr != nil || writer.Failed() != nil {
		r.noteFailure(releaseErr, entry.id, entry.agent)
		if r.removeWarm(entry) {
			r.unloaded(entry.id, entry.agent, "failed")
		}
	}
}
