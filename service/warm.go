package service

import (
	"context"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service/internal/warm"
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
// also counts toward Bounds.MaxActiveSessions and is the first thing evicted
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

// warmCopy is what a warm entry keeps: the Writer that wrote the Session, and
// the Agent it ran with. It holds no Claim.
type warmCopy struct {
	writer *session.Writer
	spec   Agent
}

// copyOf returns the copy a warm entry keeps.
func copyOf(entry *warm.Entry) *warmCopy { return entry.Value.(*warmCopy) }

// WarmSessions reports the idle Sessions this Instance holds in memory.
func (r *Runner) WarmSessions() int { return r.warm.Len() }

// keepWarm keeps the Session writer wrote, released and healthy, in memory for
// the Agent's TTL, and arms its timers. A Writer that failed, a draining or
// closed Runner and a negative TTL keep nothing.
func (r *Runner) keepWarm(writer *session.Writer, spec Agent) {
	ttl := spec.Warm.ttl()
	// [DATA-LOSS] a failed Writer's copy may differ from the store and must not serve the next Run.
	if ttl < 0 || writer == nil || writer.Failed() != nil {
		return
	}
	if r.Draining() {
		return
	}
	plan := warm.Plan{TTL: ttl, OnExpire: r.expire}
	if after, why, on := spec.Warm.idleSchedule(spec.Model.Config().Facts.CacheRetention); on {
		plan.IdleAfter, plan.OnIdle = after, func(entry *warm.Entry) { r.idleCompact(entry, why) }
	}
	if r.warm.Put(writer.ID(), spec.Name, &warmCopy{writer: writer, spec: spec}, plan) == nil {
		return
	}
	r.trimWarm()
}

// trimWarm evicts the longest-idle warm Sessions until the Instance holds no
// more than Bounds.MaxWarmSessions. A Session with an admitted Run is never
// the victim.
func (r *Runner) trimWarm() {
	for {
		victim := r.runs.Victim(r.warm, r.bounds.MaxWarmSessions)
		if victim == nil {
			return
		}
		r.dropEntry(victim, "evicted")
	}
}

// unloaded tells the Instance's subscribers that a Session left memory.
func (r *Runner) unloaded(id, agent, why string) {
	r.notice(gotato.SessionUnloaded{Header: gotato.NewHeader(gotato.SessionID(id), "", 0), Why: why}, agent)
}

// dropAllWarm lets go of every warm copy, with why, canceling any idle
// compaction first. An idle copy holds no Claim, so nothing is released.
func (r *Runner) dropAllWarm(why string) {
	for _, entry := range r.warm.Entries() {
		r.dropEntry(entry, why)
	}
}

// dropEntry cancels entry's idle compaction, waits for it to let go of the
// Writer, and drops the copy, if it is still the cached one.
func (r *Runner) dropEntry(entry *warm.Entry, why string) {
	entry.CancelIdle()
	if r.warm.Remove(entry) {
		r.unloaded(entry.ID, entry.Agent, why)
	}
}

// expire is the TTL timer: the idle Session leaves memory.
func (r *Runner) expire(entry *warm.Entry) { r.dropEntry(entry, "ttl") }

// holdWarm is called before a caller takes the Session lock. It cancels an
// idle compaction of the Session, waits for it to finish, and keeps a new one
// from starting until the returned function is called, so a message that
// arrives while the Session compacts is served first and never finds it busy.
func (r *Runner) holdWarm(ctx context.Context, id string) (func(), error) {
	entry := r.warm.Get(id)
	if entry == nil {
		return func() {}, nil
	}
	// The store answers first: a caller it refuses must not cancel the idle
	// compaction of a Session that is not its own.
	if err := r.reachStore(ctx, id); err != nil {
		return nil, err
	}
	return entry.Hold(ctx)
}

// idleCompact is the warm Session's own timer: it compacts the idle Session
// with the compaction a service's timer calls (Runner.Compact with
// min_tokens), shortly before its prompt cache expires or it leaves memory. A
// caller that needs the Session cancels it (holdWarm) and is served first. A
// Session whose Record another Instance wrote is dropped instead, and one whose
// stretch is below the minimum is left alone, with no write. A compaction
// that cannot be written leaves the copy ahead of the store, so the copy is
// dropped and the next Run loads the Session again.
func (r *Runner) idleCompact(entry *warm.Entry, why gotato.CompactionWhy) {
	ctx, end, ok := entry.BeginIdle()
	if !ok {
		return
	}
	defer end()
	// Never wait for the Session: whoever holds it is using it.
	if err := r.locks.acquire(ctx, entry.ID, false, nil); err != nil {
		return
	}
	defer r.locks.release(entry.ID)
	r.compactIdleLocked(ctx, entry, why)
}

// compactIdleLocked is idleCompact once it holds the Session's lock: check
// that the copy is still the Session's, resume its Writer, compact, release.
func (r *Runner) compactIdleLocked(ctx context.Context, entry *warm.Entry, why gotato.CompactionWhy) {
	if r.warm.Get(entry.ID) != entry || ctx.Err() != nil {
		return
	}
	kept := copyOf(entry)
	writer := kept.writer
	if stale, err := writer.Stale(ctx); err != nil || stale {
		if ctx.Err() == nil && r.warm.Remove(entry) {
			r.unloaded(entry.ID, entry.Agent, "taken_over")
		}
		return
	}
	record := writer.Record()
	minTokens := kept.spec.Warm.minTokens()
	if minTokens > 0 && modelctx.EstimateTokens(record.Tail()) < minTokens {
		return
	}
	if err := writer.Resume(ctx); err != nil {
		if !lostRace(err) {
			r.settle(writer, err)
		}
		if r.warm.Remove(entry) {
			reason := "failed"
			if lostRace(err) {
				reason = "taken_over"
			}
			r.noteFailure(err, entry.ID, entry.Agent)
			r.unloaded(entry.ID, entry.Agent, reason)
		}
		return
	}
	compactOpts := []modelctx.CompactOption{modelctx.WithCompactionWhy(why), modelctx.WithRecapContext(r.recapContext(kept.spec, record))}
	// The result needs no report: the compaction's Events say what it did.
	_, _ = modelctx.Compact(ctx, record, compactorFor(kept.spec), modelctx.CompactConfig{MinTokens: minTokens}, compactOpts...)
	saveCtx, cancelSave := persistContext(ctx)
	releaseErr := r.releaseWriter(saveCtx, writer)
	cancelSave()
	if releaseErr != nil || writer.Failed() != nil {
		r.noteFailure(releaseErr, entry.ID, entry.Agent)
		if r.warm.Remove(entry) {
			r.unloaded(entry.ID, entry.Agent, "failed")
		}
	}
}
