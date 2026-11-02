package service

import (
	"context"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/service/internal/clock"
	"github.com/jinhuang712/gotato/store"
)

// createSession creates and claims the Record of a new, saved Run. The Run's
// stream starts before the Session is created, so its first Event is the
// session_created that the Run caused.
func (st *runState) createSession(ctx, runCtx context.Context) error {
	r := st.r
	fresh, spec, err := r.newSession(ctx, st.request.Agent, st.request.task, st.request.Metadata)
	if err != nil {
		return err
	}
	st.agent = &spec
	st.acquired = feedKey{fresh.ID(), fresh.RecordEpoch()}
	st.feed = r.acquireFeed(st.acquired, spec.Name)
	if err := r.streamTo(ctx, st.feed, st.stream, st.track); err != nil {
		return err
	}
	st.writer, err = r.create(runCtx, fresh, spec.Name)
	stats := store.WriteStats{}
	if st.writer != nil {
		stats = st.writer.Stats()
		st.saved = stats
	}
	r.notice(savedEvent(fresh.ID(), "", fresh.Len(), stats, err), spec.Name)
	if err != nil {
		return err
	}
	st.s = fresh
	return nil
}

// openedSession is what opening an existing Session produced, for the Events
// to publish once the Run knows its Record and its Agent.
type openedSession struct {
	loadTime time.Duration
	// early are the Events the open wrote, a claim among them. They go to
	// that Record's Feed and carry the Agent's name.
	early []gotato.Event
	// reused says a warm copy served the Run: the Session was not loaded.
	reused bool
}

// openSession loads an existing Session once, under the lock: another Run may
// have saved it meanwhile, and a Session deleted in between must not be
// resurrected from a stale copy. A Run writes as this Instance: opening the
// Session claims it when another Instance held it, and a stale writer is
// fenced. A Run that creates its Session has nothing to open.
func (st *runState) openSession(ctx, runCtx context.Context) (openedSession, error) {
	var opened openedSession
	r, request := st.r, st.request
	if request.SessionID == "" {
		return opened, nil
	}
	began := r.clock.Now()
	var err error
	if request.SkipSave {
		st.feed = newFeed(nil)
		if err := r.streamTo(ctx, st.feed, st.stream, st.track); err != nil {
			return opened, err
		}
		st.s, err = store.Open(runCtx, r.store.Records, st.sessionID)
		err = storeFailureOf(runCtx, err, true)
		opened.loadTime = clock.Since(r.clock, began)
	} else {
		opened.loadTime, opened.early, opened.reused, err = st.openWriter(runCtx, began)
	}
	if err != nil {
		r.notice(loadedEvent(st.sessionID, 0, opened.loadTime, err), request.Agent)
		r.noteFailure(err, st.sessionID, request.Agent)
		return opened, err
	}
	if st.writer != nil {
		if err := st.joinFeed(ctx, runCtx); err != nil {
			// The Session is claimed and no Run follows: give it back.
			releaseCtx, cancelRelease := persistContext(runCtx)
			_ = r.releaseWriter(releaseCtx, st.writer)
			cancelRelease()
			return opened, err
		}
	}
	return opened, nil
}

// resolveAgent finds the AgentProfile of the Run: the one the Run built its new
// Session from, else the request's, else the Session's (as its TaskSpec narrows
// it). It is resolved before the Update is applied, so a Run that is refused for
// its profile writes nothing.
func (st *runState) resolveAgent(ctx context.Context) (profile.AgentProfile, error) {
	r, request := st.r, st.request
	switch {
	case st.agent != nil:
		return *st.agent, nil
	case request.task != nil:
		return r.resolveNarrowed(ctx, request.task)
	case request.Agent != "":
		if _, task := st.s.Get(metaTask); task {
			// [CONTRACT] a Session opened for a TaskSpec keeps its AgentProfile: the snapshot narrows that one alone.
			return profile.AgentProfile{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: a Session opened for a TaskSpec keeps its AgentProfile"))
		}
		return r.agentNamed(ctx, request.Agent)
	}
	return r.agentOf(ctx, st.s)
}

// applyUpdate writes the request's Update and the AgentProfile's name into the
// Session's metadata.
func (st *runState) applyUpdate(spec profile.AgentProfile) {
	for key, value := range st.request.Update {
		st.s.Set(key, value)
	}
	st.s.Set(metaAgent, spec.Name)
	if spec.ModelName != "" {
		st.s.Set(metaModel, spec.ModelName)
	}
}

// openWriter opens the Writer of an existing, saved Session: a warm copy of it
// when the head still matches, else a load from the Store. It reports the time
// the open took and whether a warm copy served.
func (st *runState) openWriter(runCtx context.Context, began time.Time) (loadTime time.Duration, early []gotato.Event, reused bool, err error) {
	var before *store.WriteStats
	st.writer, early, before, err = st.r.acquire(runCtx, st.sessionID)
	if before != nil {
		// A warm copy has written for earlier Runs: this Run's
		// writes count from here, its claim included.
		reused, st.saved = true, *before
	}
	if st.writer != nil {
		loadTime = st.writer.Stats().Load
		st.s = st.writer.Record()
	} else {
		loadTime = clock.Since(st.r.clock, began)
	}
	return loadTime, early, reused, err
}

// joinFeed starts the Run's stream on the Feed of the Record it opened. The
// Record's epoch is known once it is loaded: the Run uses that Record's Feed,
// and its stream starts there, with the claim the open wrote.
func (st *runState) joinFeed(ctx, runCtx context.Context) error {
	st.acquired = feedKey{st.sessionID, st.s.RecordEpoch()}
	st.feed = st.r.acquireFeed(st.acquired, st.request.Agent)
	if err := st.r.streamTo(ctx, st.feed, st.stream, st.track); err != nil {
		return st.abandon(runCtx, err)
	}
	return nil
}

// announceOpen labels the Feed with the Agent's name and publishes the Events
// the open wrote and the session_loaded Event.
func (st *runState) announceOpen(spec profile.AgentProfile, known bool, early []gotato.Event, reused bool, loadTime time.Duration) {
	label := spec.Name
	if !known {
		label = ""
	}
	setLabel(st.feed, label)
	if st.writer != nil {
		st.writer.SetPublisher(st.r.publisher(st.acquired, label))
	}
	for _, event := range early {
		publish(st.feed, event)
	}
	if !reused {
		st.r.notice(loadedEvent(st.sessionID, st.s.Len(), loadTime, nil), label)
	}
}
