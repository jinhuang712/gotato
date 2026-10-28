package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// holderCheck runs before this Runner claims a Session another Instance
// holds. A draining Runner takes no Session over. Otherwise it takes the
// Session over unless the holder is alive and using it; a holder that is
// draining is waited for too, since its Run is about to be saved and released.
func (r *Runner) holderCheck(ctx context.Context, holder session.Claim, busy bool) error {
	if r.Draining() {
		return ErrDraining
	}
	if !busy || holder.Instance == "" {
		return nil
	}
	state, err := r.store.Instance(ctx, holder.Instance)
	if err != nil {
		return err
	}
	if state.Alive {
		return errHolderBusy
	}
	return nil
}

// create writes a new Session's Record as this Runner's Instance. A draining
// Runner creates one too: creating writes the store and activates nothing. The
// Events it writes go to the new Record's Feed, if this Instance has one, and
// to the Instance's otherwise, with agent's name.
func (r *Runner) create(ctx context.Context, s *session.Record, agent string, options ...session.WriterOption) (*session.Writer, error) {
	if err := r.live(ctx); err != nil {
		return nil, err
	}
	name := r.id()
	mark := r.streamMark()
	publish := r.publisher(feedKey{s.ID(), s.RecordEpoch()}, agent)
	options = append([]session.WriterOption{session.WithGuard(func() error { return r.guardFor(name) }), session.WithPublisher(publish)}, options...)
	writer, err := session.CreateWriter(ctx, r.store, s, name, options...)
	if errors.Is(err, session.ErrExists) {
		return nil, ErrSessionExists
	}
	if err == nil {
		r.cutStreams(ctx, s.ID(), s.RecordEpoch(), s.Head(), mark)
	}
	return writer, storeFailureOf(ctx, notRunIfLapsed(err), true)
}

// open opens a stored Session for writing as this Runner's Instance. When the
// Session runs on another live Instance, it waits for that Run to end, up to
// Liveness.Wait, and then refuses with a busy error that says when to retry.
//
// The Events the open writes, a claim among them, are not published: open
// returns them, for the caller to publish to the Feed of the Record it opened,
// whose epoch only the load tells (publishOpened). The caller sets the
// Writer's publisher before it writes again.
func (r *Runner) open(ctx context.Context, sessionID string) (*session.Writer, []gotato.Event, error) {
	if err := r.live(ctx); err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(r.liveness.Wait)
	pause := 20 * time.Millisecond
	waited := false
	for {
		name := r.id()
		var early []gotato.Event
		keep := func(event gotato.Event) { early = append(early, event) }
		options := []session.WriterOption{session.WithGuard(func() error { return r.guardFor(name) }), session.WithHolderCheck(r.holderCheck), session.WithPublisher(keep)}
		if waited {
			options = append(options, session.WithWaited())
		}
		mark := r.streamMark()
		writer, err := session.OpenWriter(ctx, r.store, sessionID, name, options...)
		// A conflict here means another Instance claimed or wrote first:
		// look again, as for a busy holder.
		if err == nil {
			r.cutStreams(ctx, sessionID, writer.Record().RecordEpoch(), writer.Record().Head(), mark)
			return writer, early, nil
		}
		if (!errors.Is(err, errHolderBusy) && !errors.Is(err, session.ErrConflict)) || lapsed(err) {
			return nil, nil, storeFailureOf(ctx, notRunIfLapsed(err), true)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy_elsewhere"}, "")
			return nil, nil, busyElsewhere(r.liveness.Wait)
		}
		if !waited {
			r.notice(gotato.RunWaiting{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy_elsewhere"}, "")
		}
		waited = true
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(min(pause, remaining)):
		}
		pause = min(pause*2, 500*time.Millisecond)
	}
}

// busyElsewhere refuses a Run on a Session that is running on another
// Instance. It wraps ErrBusy, so errors.Is matches it, and says when to retry.
func busyElsewhere(retryAfter time.Duration) error {
	busy := gotato.ErrorOf(gotato.CodeBusy, "service: the session is running on another instance")
	busy.Cause = ErrBusy
	busy.Details = map[string]string{"retry_after_ms": strconv.FormatInt(retryAfter.Milliseconds(), 10)}
	return busy
}

// acquire gives the caller the Writer of a Session it is about to use, under
// the Session lock. A warm copy is used when the store's head is still the
// copy's, claiming the Session again with a write; when another Instance
// wrote the Record, the copy is dropped and the Session is loaded from the
// store, as for a Session that was never warm. before is set only when the
// copy was used, so the Session was not loaded: it holds the Writer's counts of
// its work with the store before the claim, which a copy that has written for
// earlier Runs needs to count this Run's writes alone. The Events the claim
// wrote come back for the caller to publish, as open returns them.
func (r *Runner) acquire(ctx context.Context, sessionID string) (writer *session.Writer, early []gotato.Event, before *session.WriteStats, err error) {
	if entry := r.takeWarm(sessionID); entry != nil {
		w := entry.writer
		stale, staleErr := w.Stale(ctx)
		switch {
		case ctx.Err() != nil:
			// The caller gave up before the copy was judged: the copy is
			// untouched and still good, so it stays warm for the next Run.
			r.keepWarm(w, entry.spec)
			return nil, nil, nil, ctx.Err()
		case staleErr == nil && !stale:
			if err := r.live(ctx); err != nil {
				r.unloaded(sessionID, entry.agent, "liveness_lapsed")
				return nil, nil, nil, err
			}
			base := w.Stats()
			var claimed []gotato.Event
			w.SetPublisher(func(event gotato.Event) { claimed = append(claimed, event) })
			err := w.Resume(ctx)
			if err == nil {
				return w, claimed, &base, nil
			}
			// Only a write that lost the race to another Instance falls back
			// to a load. Any other failure is the Run's, as it is for a Session
			// that was never warm.
			if !lostRace(err) {
				r.noteFailure(err, sessionID, entry.agent)
				r.unloaded(sessionID, entry.agent, "failed")
				return nil, nil, nil, err
			}
		}
		// Another Instance wrote the Record, or deleted it, or the copy
		// could not claim: the copy is of no use.
		r.unloaded(sessionID, entry.agent, "taken_over")
	}
	writer, early, err = r.open(ctx, sessionID)
	return writer, early, nil, err
}

// lostRace reports a write refused because another Instance wrote the Record,
// or deleted it, first. A write this Instance's own Liveness row refused is
// not one.
func lostRace(err error) bool {
	return !lapsed(err) && (errors.Is(err, session.ErrConflict) || errors.Is(err, session.ErrNotFound))
}
