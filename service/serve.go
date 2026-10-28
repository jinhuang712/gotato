package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// runState is what one serve call shares between its phases: the Session it
// works on, the Writer and Feed it holds, and the outcome it builds.
type runState struct {
	r       *Runner
	request RunRequest
	stream  *runStream
	handle  *runHandle
	follow  *followOn

	s         *session.Record
	writer    *session.Writer
	saved     session.WriteStats
	feed      *gotato.Feed
	track     *tracker
	sessionID string
	// acquired is the shared Feed the Run holds, released when serve returns.
	acquired feedKey

	// started says the Run's live Session was built, so the Run cannot be
	// undone by deleting the Session it created.
	started bool
	// followDropped says the messages a following Run was to take were
	// reported dropped, since the Run failed before its live Session took them.
	followDropped bool
	out           RunResponse
	runErr        error
}

// serve runs one admitted Run.
func (r *Runner) serve(ctx, runCtx context.Context, request RunRequest, stream *runStream, handle *runHandle, follow *followOn) (RunResponse, error) {
	// Create the Session, or name the one to load. runCtx carries the
	// cancellation that Stop/Drain apply, so a stalled Store call does not
	// outlive them.
	st := &runState{r: r, request: request, stream: stream, handle: handle, follow: follow, track: &tracker{}, sessionID: request.SessionID}
	// The Run's stream ends with the Run: what is queued is still delivered.
	defer func() {
		if stream != nil && stream.sub != nil {
			stream.sub.End()
		}
	}()
	// A Record's Feed is shared and counted; a Run that is not saved has a
	// Feed of its own, since its Events are in no Record.
	defer func() {
		if st.acquired != (feedKey{}) {
			r.releaseFeed(st.acquired)
		}
	}()
	if st.sessionID == "" {
		if request.SkipSave {
			// No persistence: build an in-memory Session for this Run only.
			st.s = session.New()
			for key, value := range request.Metadata {
				st.s.Set(key, value)
			}
			st.feed = gotato.NewFeed(nil)
			defer st.feed.Close()
			if err := r.streamTo(ctx, st.feed, stream, st.track); err != nil {
				return RunResponse{}, err
			}
		} else if err := st.createSession(ctx, runCtx); err != nil {
			return RunResponse{}, err
		}
		// A newly created Session has a generated ID; retarget the handle so
		// AbortSession can find it for the rest of the Run.
		st.sessionID = st.s.ID()
		r.retargetHandle(handle, st.sessionID)
	}

	// One Run per Session at a time. Every Session mutation goes through
	// withSessionLock; the admission policy decides the busy behavior.
	created := request.SessionID == "" && !request.SkipSave
	// A Run that follows another waits for the Session whatever the busy policy.
	lockErr := r.sessionLock(runCtx, st.sessionID, follow != nil || handle.wait, func() error {
		return st.serveLocked(ctx, runCtx)
	})
	if lockErr != nil {
		st.dropFollow(runCtx)
		if created && !st.started {
			lockErr = st.cleanupUnstarted(ctx, lockErr)
		}
		if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
			// Canceled while waiting for the Session lock: report a settled
			// canceled Run rather than a bare error.
			return canceledRunResult(st.sessionID, request.Agent, lockErr), lockErr
		}
		return RunResponse{}, lockErr
	}
	return st.out, st.runErr
}

// serveLocked is the part of a Run that holds the Session lock: open the
// Session, build the live Session, prompt it, then save and report.
func (st *runState) serveLocked(ctx, runCtx context.Context) error {
	r, request := st.r, st.request
	if request.SkipSave && request.SessionID != "" {
		// A Run that saves nothing has a Feed of its own, which ends with it.
		defer func() {
			if st.feed != nil {
				st.feed.Close()
			}
		}()
	}
	opened, err := st.openSession(ctx, runCtx)
	if err != nil {
		return err
	}
	spec, ok := st.resolveAgent()
	if request.SessionID != "" {
		st.announceOpen(spec, ok, opened.early, opened.reused, opened.loadTime)
	}
	if !ok {
		return st.abandon(runCtx, ErrUnknownAgent)
	}
	st.applyUpdate(spec)

	live, auto, err := st.startRun(ctx, runCtx, spec)
	if err != nil {
		return st.abandon(runCtx, err)
	}
	var result gotato.RunResult
	result, st.runErr = st.promptWithDeadline(ctx, spec, live)
	st.detach(runCtx, live)
	if r.beforeRelease != nil {
		r.beforeRelease()
	}
	st.persist(runCtx, spec, &result)
	st.respond(spec, auto, result)
	return nil
}

// abandon returns err after a Run that cannot start has released the Session
// it opened.
func (st *runState) abandon(runCtx context.Context, err error) error {
	st.dropFollow(runCtx)
	if st.writer != nil {
		releaseCtx, cancelRelease := persistContext(runCtx)
		_ = st.r.releaseWriter(releaseCtx, st.writer)
		cancelRelease()
	}
	return err
}

// startRun builds the live Session, queues the notices and follow-on messages
// that wait for it, and makes the handle reach it. The build, with the
// resolution of the Agent's ToolSources and ToolSets, runs under runCtx, so a
// Stop or Drain that arrives before the handle reaches the live Session
// cancels it.
func (st *runState) startRun(ctx, runCtx context.Context, spec Agent) (*gotato.Session, *modelctx.AutoCompactor, error) {
	r, s, handle, follow := st.r, st.s, st.handle, st.follow
	onRunStart := gotato.WithRunStart(func(runID gotato.RunID) {
		st.track.set(runID)
		handle.attachRunID(runID)
		handle.abortIfStopped()
	})
	live, auto, err := r.buildSession(runCtx, spec, s, st.writer, InspectConfig{}, onRunStart, gotato.WithFeed(st.feed), gotato.WithQueueHandoff(), gotato.WithRequestID(st.request.RequestID))
	if err != nil {
		return nil, nil, err
	}
	// The notices that wait in the Record are read before this Run queues
	// any of its own.
	kept := s.PendingNotices()
	if follow == nil && !st.request.Continue {
		// A message that starts a Run after a long absence is followed by
		// a notice with the time.
		r.queueTimeAway(ctx, spec, s, live)
	}
	if len(kept) > 0 {
		// Notices that were queued and not committed, by an earlier Run or
		// by a call between Runs, on this Instance or another, go in after
		// this Run's prompt. The Record is where they wait.
		queued := make([]gotato.Queued, len(kept))
		for i, notice := range kept {
			queued[i] = notice.Queued()
		}
		if rest := live.Requeue(queued); len(rest) > 0 {
			live.DropQueued(rest, "queue_full")
		}
	}
	if follow != nil {
		// The messages that waited behind the first one wait in this Run's
		// queues, in order, before anything new can reach them.
		if rest := live.Requeue(follow.rest); len(rest) > 0 {
			live.DropQueued(rest, "queue_full")
		}
	}
	st.started = true
	// The Run is really starting: a stream may begin.
	if st.stream != nil && st.stream.admitted != nil {
		st.stream.admitted <- struct{}{}
	}
	// From here a stop aborts the Run inside the live Session, so Prompt
	// still returns the settled (canceled) result. Prompt waits on the
	// caller's context, not runCtx: a stop that canceled runCtx while the
	// Session was loading reaches the Run through abortIfStopped instead.
	handle.attachAbort(live.Abort)
	handle.attachLive(live)
	return live, auto, nil
}

// promptWithDeadline runs the Run's prompt, or its continuation, on the live
// Session under the Run's deadline: the request's Timeout, else the Agent's
// RunTimeout. The caller's own deadline on ctx overrides it when it is
// shorter, as any child context's does.
func (st *runState) promptWithDeadline(ctx context.Context, spec Agent, live *gotato.Session) (gotato.RunResult, error) {
	runDeadline := st.request.Timeout
	if runDeadline == 0 {
		runDeadline = spec.RunTimeout
	}
	if runDeadline == 0 {
		runDeadline = defaultRunTimeout
	}
	promptCtx := ctx
	if runDeadline > 0 {
		var cancelDeadline context.CancelFunc
		promptCtx, cancelDeadline = context.WithTimeout(ctx, runDeadline)
		defer cancelDeadline()
	}
	switch {
	case st.request.Continue:
		return live.Continue(promptCtx)
	case st.follow != nil:
		return live.Prompt(promptCtx, st.follow.first.Message)
	default:
		return live.Prompt(promptCtx, gotato.UserMessage(st.request.Prompt))
	}
}

// cleanupUnstarted removes the Session of a one-shot Run that never started,
// so it does not leave an empty Session behind. A failed cleanup is reported,
// not discarded.
func (st *runState) cleanupUnstarted(ctx context.Context, lockErr error) error {
	deleteCtx, cancelDelete := persistContext(ctx)
	deleteErr := st.r.store.Delete(deleteCtx, st.sessionID)
	cancelDelete()
	if deleteErr != nil {
		lockErr = errors.Join(lockErr, fmt.Errorf("service: cleanup of session %s failed: %w", st.sessionID, deleteErr))
	}
	return lockErr
}

// dropFollow reports the messages a following Run was to take as dropped, with
// why run_failed, when the Run failed before its live Session took them; from
// then on the Session owns them and its own drops apply. The Events go in the
// Record when the Run opened it and can still write, and then reach its
// subscribers. When nothing can be written, such as a store that fails to open
// the Session or a Session deleted meanwhile, they go to the Instance's stream
// alone: the drop is not stored.
func (st *runState) dropFollow(runCtx context.Context) {
	if st.follow == nil || st.started || st.followDropped {
		return
	}
	st.followDropped = true
	r := st.r
	events := st.follow.dropped(gotato.SessionID(st.sessionID), "run_failed")
	if st.writer != nil {
		writeCtx, cancel := persistContext(runCtx)
		defer cancel()
		if _, err := st.writer.Record().Write(writeCtx, events); err == nil && st.writer.Flush(writeCtx) == nil {
			return
		}
	}
	for _, event := range events {
		r.notice(event, st.request.Agent)
	}
}
