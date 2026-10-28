package service

import (
	"context"
	"errors"

	"github.com/jinhuang712/gotato/session"
)

// markUnreleased remembers a Session whose release write failed.
func (r *Runner) markUnreleased(sessionID string) {
	r.releaseMu.Lock()
	defer r.releaseMu.Unlock()
	if r.unreleased == nil {
		r.unreleased = map[string]struct{}{}
	}
	r.unreleased[sessionID] = struct{}{}
}

// settle is what each exit that held the Claim of a Session does with the
// result of its release: a Writer that failed, or a release that failed, may
// leave the Session busy in the store under this Instance, whose Liveness row
// goes on renewing, so no other Instance could take it. The Session is
// remembered, and the heartbeat opens it again from the store and releases it
// (repairReleases). Every exit that claims a Session ends with it.
func (r *Runner) settle(writer *session.Writer, releaseErr error) {
	if releaseErr == nil && writer.Failed() == nil {
		return
	}
	if lostRace(releaseErr) && !lapsed(releaseErr) {
		// Another Instance holds the Record, or it is gone: nothing is ours to give back.
		return
	}
	r.markUnreleased(writer.ID())
}

// repairReleases writes again the release of each Session that this Instance
// failed to release. It runs on every heartbeat tick, so a Session is busy
// under an idle holder for a third of the Liveness TTL after the store
// returns, not for good. A Session that is no longer this Instance's, or no
// longer busy, or gone, is forgotten.
func (r *Runner) repairReleases() {
	r.releaseMu.Lock()
	ids := make([]string, 0, len(r.unreleased))
	for id := range r.unreleased {
		ids = append(ids, id)
	}
	r.releaseMu.Unlock()
	// One bound for the whole pass, so that a slow store cannot hold back the
	// next renewal.
	ctx, cancel := context.WithTimeout(context.Background(), r.liveness.TTL/3)
	defer cancel()
	for _, id := range ids {
		if r.repairRelease(ctx, id) {
			r.releaseMu.Lock()
			delete(r.unreleased, id)
			r.releaseMu.Unlock()
		}
	}
}

// repairRelease releases one Session, under the Session lock when it is free;
// a Session with a Run on it is not waited for. It reports whether the Session
// needs no more attempts.
func (r *Runner) repairRelease(ctx context.Context, sessionID string) bool {
	record, err := session.Open(ctx, r.store, sessionID)
	if err != nil {
		// A Session that is gone has nothing to release.
		return errors.Is(err, session.ErrNotFound)
	}
	if claim, busy := record.Holder(); !busy || claim.Instance != r.id() {
		return true
	}
	if err := r.locks.acquire(ctx, sessionID, false, nil); err != nil {
		// A Session with a Run on it again is released by that Run.
		return errors.Is(err, ErrBusy)
	}
	defer r.locks.release(sessionID)
	writer, early, _, err := r.acquire(ctx, sessionID)
	if err != nil {
		return errors.Is(err, session.ErrNotFound)
	}
	agent, _ := writer.Record().Get(MetaAgent)
	r.publishOpened(writer, early, agent)
	if err := writer.Release(ctx); err != nil {
		return false
	}
	if spec, ok := r.Agent(agent); ok {
		r.keepWarm(writer, spec)
	}
	return true
}
