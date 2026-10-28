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
	// [OWNERSHIP] a Record that another Instance holds or that is gone is not ours to give back. Test: TestAReleaseThatLostTheRecordIsNotMarkedForRepair
	if lostRace(releaseErr) {
		return
	}
	r.markUnreleased(writer.ID())
}

// releaseWriter gives back the Claim of writer and settles the result. Every
// exit that held a Claim ends here, so none can skip the settle.
func (r *Runner) releaseWriter(ctx context.Context, writer *session.Writer) error {
	err := writer.Release(ctx)
	r.settle(writer, err)
	return err
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
	// [LIVENESS] a slow store cannot hold back the next Liveness renewal.
	ctx, cancel := context.WithTimeout(context.Background(), r.liveness.TTL/3)
	defer cancel()
	for _, id := range ids {
		// Clear the mark before the attempt: a mark set meanwhile is a new
		// failure and stays. An attempt that is not done marks it again.
		r.releaseMu.Lock()
		delete(r.unreleased, id)
		r.releaseMu.Unlock()
		if !r.repairRelease(ctx, id) {
			r.markUnreleased(id)
		}
	}
}

// repairRelease releases one Session, under the Session lock when it is free;
// a Session with a Run on it is not waited for and stays marked. It reports
// whether the Session needs no more attempts.
func (r *Runner) repairRelease(ctx context.Context, sessionID string) bool {
	record, err := session.Open(ctx, r.store, sessionID)
	if err != nil {
		// A Session that is gone has nothing to release.
		return errors.Is(err, session.ErrNotFound)
	}
	// [OWNERSHIP] only a Claim held by this Instance is released here.
	if claim, busy := record.Holder(); !busy || claim.Instance != r.id() {
		return true
	}
	// [OWNERSHIP] a Run holding the Session lock owns its release; the mark stays for the next tick. Test: TestRepairKeepsTheMarkOfASessionWhoseRunStillHoldsTheLock
	if err := r.locks.acquire(ctx, sessionID, false, nil); err != nil {
		// A Session with a Run on it may have been marked by that Run, which
		// settles inside the lock: keep the mark and try again next tick.
		return false
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
