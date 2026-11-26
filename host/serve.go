package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
)

// runState is what one serve call shares between its phases: the Session it
// works on, the Writer and Feed it holds, and the outcome it builds.
type runState struct {
	r       *Runner
	request RunRequest
	stream  *runStream
	handle  *runHandle
	follow  *followOn

	s         *record.Record
	writer    *store.Writer
	saved     store.WriteStats
	feed      *gotato.Feed
	track     *tracker
	sessionID string
	// acquired is the shared Feed the Run holds, released when serve returns.
	acquired feedKey
	// agent is the AgentProfile the Run built its new Session from, so the
	// Run looks it up once.
	agent *profile.AgentProfile

	// started says the Run's live Session was built, so the Run cannot be
	// undone by deleting the Session it created.
	started bool
	// followDropped says the messages a following Run was to take were
	// reported dropped, since the Run failed before its live Session took them.
	followDropped bool
	out           RunResponse
	runErr        error
}

// newRun is the state of one admitted Run of request, before it is served.
func (r *Runner) newRun(request RunRequest, stream *runStream, handle *runHandle, follow *followOn) *runState {
	return &runState{r: r, request: request, stream: stream, handle: handle, follow: follow, track: &tracker{}, sessionID: request.SessionID}
}

// serve runs one admitted Run.
func (r *Runner) serve(ctx, runCtx context.Context, st *runState) (RunResponse, error) {
	// Create the Session, or name the one to load. runCtx carries the
	// cancellation that Stop/Drain apply, so a stalled Store call does not
	// outlive them.
	request, stream, handle := st.request, st.stream, st.handle
	// The Run's stream ends with the Run: what is queued is still delivered.
	defer st.endStream()
	// A Record's Feed is shared and counted; a Run that is not saved has a
	// Feed of its own, since its Events are in no Record.
	defer st.releaseAcquired()
	if st.sessionID == "" {
		if request.SkipSave {
			st.newMemorySession()
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
	lockErr := r.sessionLock(runCtx, st.sessionID, st.queue(), func() error {
		return st.serveLocked(ctx, runCtx)
	})
	if lockErr != nil {
		return st.settleLockErr(ctx, runCtx, lockErr)
	}
	return st.out, st.runErr
}

// queue is the queue policy the Run takes the Session lock under.
func (st *runState) queue() QueuePolicy {
	// A Run that follows another waits for the Session whatever the busy policy.
	if st.follow != nil || st.handle.wait {
		return QueuePolicyWait
	}
	return st.r.bounds.Queue
}

// endStream ends the Run's stream, when it has one. What is queued is still
// delivered.
func (st *runState) endStream() {
	if st.stream != nil && st.stream.sub != nil {
		st.stream.sub.End()
	}
}

// releaseAcquired releases the shared Feed the Run holds, when it holds one.
func (st *runState) releaseAcquired() {
	if st.acquired != (feedKey{}) {
		st.r.releaseFeed(st.acquired)
	}
}

// newMemorySession builds the in-memory Session of a Run that saves nothing,
// with a Feed of its own. The caller closes the Feed.
func (st *runState) newMemorySession() {
	// No persistence: build an in-memory Session for this Run only.
	st.s = record.New()
	for key, value := range st.request.Metadata {
		st.s.Set(key, value)
	}
	if st.request.task != nil {
		st.s.Set(metaTask, st.request.task.snapshot)
	}
	st.feed = newFeed(nil)
}

// settleLockErr settles a Run that failed under, or while it waited for, the
// Session lock: it drops what a following Run was to take, removes the Session
// of a one-shot Run that never started, and reports a canceled wait as a
// settled canceled Run.
func (st *runState) settleLockErr(ctx, runCtx context.Context, lockErr error) (RunResponse, error) {
	st.dropFollow(runCtx)
	created := st.request.SessionID == "" && !st.request.SkipSave
	if created && !st.started {
		lockErr = st.cleanupUnstarted(ctx, lockErr)
	}
	// [CONTRACT] a Run canceled while it waits for the Session lock settles as a canceled Run.
	if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
		// Canceled while waiting for the Session lock: report a settled
		// canceled Run rather than a bare error.
		return canceledRunResult(st.sessionID, st.request.Agent, lockErr), lockErr
	}
	return RunResponse{}, lockErr
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
	spec, err := st.resolveAgent(runCtx)
	if request.SessionID != "" {
		st.announceOpen(spec, err, opened)
	}
	if err != nil {
		return st.abandon(runCtx, err)
	}
	st.applyUpdate(spec)

	live, auto, err := st.startRun(ctx, runCtx, spec)
	if err != nil {
		return st.abandon(runCtx, err)
	}
	var result gotato.RunResult
	result, st.runErr = st.promptWithDeadline(ctx, spec, live)
	st.dropUntakenFollow(live, result)
	st.detach(runCtx, live)
	if r.hooks.beforeRelease != nil {
		r.hooks.beforeRelease()
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
func (st *runState) startRun(ctx, runCtx context.Context, spec profile.AgentProfile) (*gotato.Session, *record.AutoCompactor, error) {
	r, s, handle := st.r, st.s, st.handle
	onRunStart := withRunStart(func(runID gotato.RunID) {
		st.track.set(runID)
		handle.attachRunID(runID)
		handle.abortIfStopped()
	})
	live, auto, err := r.buildSession(runCtx, spec, s, buildConfig{writer: st.writer}, onRunStart, withFeed(st.feed), withQueueHandoff(), withRequestID(st.request.RequestID))
	if err != nil {
		return nil, nil, err
	}
	st.queueWaiting(ctx, spec, live)
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

// queueWaiting queues in the live Session what waits for it: the notice of
// a long absence, the notices that wait in the Record and the messages that
// followed the first one.
func (st *runState) queueWaiting(ctx context.Context, spec profile.AgentProfile, live *gotato.Session) {
	r, s, follow := st.r, st.s, st.follow
	// The notices that wait in the Record are read before this Run queues
	// any of its own.
	kept := s.PendingNotices()
	if follow == nil && !st.request.Continue {
		// A message that starts a Run after a long absence is followed by
		// a notice with the time.
		r.queueTimeAway(ctx, spec, s, live)
	}
	if len(kept) > 0 {
		requeueNotices(live, kept)
	}
	if follow != nil {
		// The messages that waited behind the first one wait in this Run's
		// queues, in order, before anything new can reach them.
		if rest := requeue(live, follow.rest); len(rest) > 0 {
			dropQueued(live, rest, "queue_full")
		}
	}
}

// requeueNotices queues the notices kept in the Record in the live Session,
// and drops those its queue has no room for.
func requeueNotices(live *gotato.Session, kept []gotato.PendingNotice) {
	// Notices that were queued and not committed, by an earlier Run or
	// by a call between Runs, on this Instance or another, go in after
	// this Run's prompt. The Record is where they wait.
	queued := make([]gotato.Queued, len(kept))
	for i, notice := range kept {
		queued[i] = notice.Queued()
	}
	if rest := requeue(live, queued); len(rest) > 0 {
		dropQueued(live, rest, "queue_full")
	}
}

// promptWithDeadline runs the Run's prompt, or its continuation, on the live
// Session under the Run's deadline: the request's Timeout, else the Agent's
// RunTimeout. The caller's own deadline on ctx overrides it when it is
// shorter, as any child context's does.
func (st *runState) promptWithDeadline(ctx context.Context, spec profile.AgentProfile, live *gotato.Session) (gotato.RunResult, error) {
	runDeadline := st.request.Timeout
	if runDeadline == 0 {
		runDeadline = spec.RunTimeout
	}
	if runDeadline == 0 {
		runDeadline = profile.DefaultRunTimeout
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
	deleteErr := st.r.store.Records.Delete(deleteCtx, st.sessionID)
	cancelDelete()
	if deleteErr != nil {
		lockErr = errors.Join(lockErr, fmt.Errorf("host: cleanup of session %s failed: %w", st.sessionID, deleteErr))
	}
	return lockErr
}

// dropUntakenFollow reports the first message of a following Run as dropped
// when the Run ended without committing it as its prompt (it failed before
// that, as a Session at its message limit does): its message_queued is in the
// Record, and no one else would close it.
// [DATA-LOSS] a message reported queued must end committed or dropped, and the Run that never committed it does not drop it.
func (st *runState) dropUntakenFollow(live *gotato.Session, result gotato.RunResult) {
	if st.follow == nil || (st.runErr == nil && result.Status == gotato.RunStatusCompleted) {
		return
	}
	first := st.follow.first
	for _, message := range live.Transcript().Messages() {
		if message.ID == first.Message.ID {
			return
		}
	}
	dropQueued(live, []gotato.Queued{first}, "run_failed")
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
