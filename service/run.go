package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// RunRequest is one unit of work.
type RunRequest struct {
	// SessionID names the Session. Empty creates a new Session.
	SessionID string
	// Agent selects the Agent for a new Session; an existing Session
	// keeps the Agent it was created with unless Agent is set.
	Agent string
	// Prompt is the user input. Empty with Continue=true resumes the loop.
	Prompt   string
	Continue bool
	// Metadata is applied to a new Session.
	Metadata map[string]string
	// Update sets metadata on the Session before the Run, under its Claim,
	// as the Run's first write. An empty value deletes a key.
	Update map[string]string
	// SkipSave runs without persisting the Session: a new Session lives only
	// for this Run, and a changed existing Session, Update included, is left
	// as it was.
	SkipSave bool
	// Timeout bounds this Run; the Run settles as deadline_exceeded. Zero
	// keeps the Agent's RunTimeout.
	Timeout time.Duration
}

// RunResponse is the outcome of one Run plus the Session state it left.
type RunResponse struct {
	SessionID string           `json:"session_id"`
	Agent     string           `json:"agent"`
	Model     string           `json:"model,omitempty"`
	Result    gotato.RunResult `json:"result"`
	FinalText string           `json:"final_text,omitempty"`
	Compacted bool             `json:"compacted"`
	Messages  int              `json:"messages"`
	Events    int              `json:"events"`
	// RecordEpoch is the epoch of the Session's Record, for a client that
	// loses the Run's stream to resume the Session's stream with. Empty for a
	// Run that is not saved.
	RecordEpoch string `json:"record_epoch,omitempty"`
	// Reconnect is set when this instance's drain stopped the Run before it
	// completed. By the time the Run returns, its Session is saved and
	// released, and so is every other Session of the drain.
	Reconnect *Reconnect `json:"reconnect,omitempty"`
}

// Run executes one Run synchronously.
func (r *Runner) Run(ctx context.Context, request RunRequest) (RunResponse, error) {
	return r.run(ctx, request, nil)
}

// StreamRun executes one Run and delivers every Event of its stream to sink as
// it is produced, on the caller's goroutine: the Session's stored Events and
// its deltas, from the first Event of the Run to its RunEnded. The Run itself
// runs on another goroutine, and sink reads a Subscription, so a sink that is
// slow slows only itself: the Session never waits for it, and a stored Event
// the sink falls behind on is read again from the Record. A sink error is
// advisory: it stops delivery, not the Run. StreamRun returns after the Run
// settles and its last Event has been delivered.
func (r *Runner) StreamRun(ctx context.Context, request RunRequest, sink func(gotato.Event) error) (RunResponse, error) {
	return r.StreamRunAdmitted(ctx, request, nil, sink)
}

// StreamRunAdmitted is StreamRun with a call, on the caller's goroutine, once
// the Run is admitted and before its first Event is delivered. A Run that is
// refused at admission (draining, at capacity, past the activation limit)
// returns its error without the call, so an endpoint can still answer the
// request with a status of its own before it starts to stream.
func (r *Runner) StreamRunAdmitted(ctx context.Context, request RunRequest, admitted func(), sink func(gotato.Event) error) (RunResponse, error) {
	if sink == nil {
		return r.run(ctx, request, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	type outcome struct {
		result RunResponse
		err    error
	}
	subs := make(chan gotato.Subscription, 1)
	admit := make(chan struct{}, 1)
	done := make(chan outcome, 1)
	go func() {
		result, err := r.run(ctx, request, &runStream{ready: func(sub gotato.Subscription) { subs <- sub }, admitted: admit})
		done <- outcome{result, err}
	}()
	var finished *outcome
	call := func() {
		if admitted != nil {
			admitted()
		}
	}
	select {
	case <-admit:
		call()
	case o := <-done:
		finished = &o
		select {
		case <-admit:
			call()
		default:
		}
	}
	if finished != nil {
		// The Run ended: it may have handed over its stream first.
		select {
		case sub := <-subs:
			deliver(ctx, sub, sink)
		default:
		}
		return finished.result, finished.err
	}
	select {
	case sub := <-subs:
		deliver(ctx, sub, sink)
	case o := <-done:
		finished = &o
		// A Run that ended may have handed over its stream first.
		select {
		case sub := <-subs:
			deliver(ctx, sub, sink)
		default:
		}
	}
	if finished == nil {
		o := <-done
		finished = &o
	}
	return finished.result, finished.err
}

func (r *Runner) run(ctx context.Context, request RunRequest, stream *runStream) (RunResponse, error) {
	return r.runWith(ctx, request, stream, false)
}

// runWith is run; wait makes the Run wait for a busy Session, as a Run that
// follows another does.
func (r *Runner) runWith(ctx context.Context, request RunRequest, stream *runStream, wait bool) (RunResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !request.Continue && strings.TrimSpace(request.Prompt) == "" {
		return RunResponse{}, notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: prompt is required"))
	}
	// The Run registers as it is admitted, in one step, so the limits count it
	// at once and Drain and AbortSession reach it even while it is still
	// loading its Session.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	handle := &runHandle{sessionID: request.SessionID, cancel: cancelRun, ready: make(chan struct{}), wait: wait, over: make(chan struct{})}
	if err := r.admit(handle); err != nil {
		why := "at_capacity"
		switch {
		case errors.Is(err, ErrDraining):
			why = "draining"
		case errors.Is(err, ErrOverloaded):
			why = "activation_limit"
		}
		r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Why: why}, request.Agent)
		return RunResponse{}, err
	}
	if stream != nil && stream.admitted != nil {
		stream.admitted <- struct{}{}
	}
	r.notice(gotato.RunRequested{Header: gotato.NewHeader(gotato.SessionID(request.SessionID), "", 0), Agent: request.Agent}, request.Agent)
	release := sync.OnceFunc(r.release)
	followed := false
	defer func() {
		if !followed {
			r.unregisterHandle(handle)
			release()
		}
	}()
	out, err := r.serve(ctx, runCtx, request, stream, handle, nil)
	if next := handle.takeNext(); len(next) > 0 {
		// Messages the Run accepted after its last point of taking them run in
		// a Run that follows it, on this instance and under the same
		// admission. This call returns with its own Run's result.
		followed = true
		r.followOn(ctx, handle, next, release)
		return out, err
	}
	if handle.stoppedByDrain() && out.Result.Status != gotato.RunStatusCompleted {
		// The drain stopped this Run, and it is saved and released. It
		// returns once every Run of the drain is, so the caller reconnects to
		// a Session that no instance is still using.
		out.Reconnect = NewReconnect("draining")
		release()
		r.mu.Lock()
		drained := r.drained
		r.mu.Unlock()
		select {
		case <-drained:
		case <-ctx.Done():
		}
	}
	return out, err
}

// serve runs one admitted Run.
func (r *Runner) serve(ctx, runCtx context.Context, request RunRequest, stream *runStream, handle *runHandle, follow *followOn) (RunResponse, error) {
	// Create the Session, or name the one to load. runCtx carries the
	// cancellation that Stop/Drain apply, so a stalled Store call does not
	// outlive them.
	var s *session.Record
	var writer *session.Writer
	var saved session.WriteStats
	var feed *gotato.Feed
	track := &tracker{}
	sessionID := request.SessionID
	// The Run's stream ends with the Run: what is queued is still delivered.
	defer func() {
		if stream != nil && stream.sub != nil {
			stream.sub.End()
		}
	}()
	// A Record's Feed is shared and counted; a Run that is not saved has a
	// Feed of its own, since its Events are in no Record.
	var acquired feedKey
	defer func() {
		if acquired != (feedKey{}) {
			r.releaseFeed(acquired)
		}
	}()
	if sessionID == "" {
		if request.SkipSave {
			// No persistence: build an in-memory Session for this Run only.
			s = session.New()
			for key, value := range request.Metadata {
				s.Set(key, value)
			}
			feed = gotato.NewFeed(nil)
			defer feed.Close()
			if err := r.streamTo(ctx, feed, stream, track); err != nil {
				return RunResponse{}, err
			}
		} else {
			fresh, spec, err := r.newSession(request.Agent, request.Metadata)
			if err != nil {
				return RunResponse{}, err
			}
			// The Run's stream starts before the Session is created, so its
			// first Event is the session_created that the Run caused.
			acquired = feedKey{fresh.ID(), fresh.RecordEpoch()}
			feed = r.acquireFeed(acquired, spec.Name)
			if err := r.streamTo(ctx, feed, stream, track); err != nil {
				return RunResponse{}, err
			}
			writer, err = r.create(runCtx, fresh, spec.Name)
			stats := session.WriteStats{}
			if writer != nil {
				stats = writer.Stats()
				saved = stats
			}
			r.notice(savedEvent(fresh.ID(), "", fresh.Len(), stats, err), spec.Name)
			if err != nil {
				return RunResponse{}, err
			}
			s = fresh
		}
		// A newly created Session has a generated ID; retarget the handle so
		// AbortSession can find it for the rest of the Run.
		sessionID = s.ID()
		r.retargetHandle(handle, sessionID)
	}

	// One Run per Session at a time. Every Session mutation goes through
	// withSessionLock; the admission policy decides the busy behavior.
	var out RunResponse
	var runErr error
	started := false
	created := request.SessionID == "" && !request.SkipSave
	// A Run that follows another waits for the Session whatever the busy policy.
	lockErr := r.sessionLock(runCtx, sessionID, follow != nil || handle.wait, func() error {
		// An existing Session is loaded once, under the lock: another Run
		// may have saved it meanwhile, and a Session deleted in between must
		// not be resurrected from a stale copy.
		var loadTime time.Duration
		// The Events the open writes, a claim among them, are published once
		// the Run knows its Record and its Agent, so they go to that Record's
		// Feed and carry the Agent's name.
		var early []gotato.Event
		// reused says a warm copy served the Run: the Session was not loaded.
		reused := false
		if request.SessionID != "" {
			// A Run writes as this Instance: opening the Session claims it
			// when another Instance held it, and a stale writer is fenced.
			began := time.Now()
			var err error
			if request.SkipSave {
				feed = gotato.NewFeed(nil)
				defer feed.Close()
				if err := r.streamTo(ctx, feed, stream, track); err != nil {
					return err
				}
				s, err = session.Open(runCtx, r.store, sessionID)
				loadTime = time.Since(began)
			} else {
				var before *session.WriteStats
				writer, early, before, err = r.acquire(runCtx, sessionID)
				if before != nil {
					// A warm copy has written for earlier Runs: this Run's
					// writes count from here, its claim included.
					reused, saved = true, *before
				}
				if writer != nil {
					loadTime = writer.Stats().Load
					s = writer.Record()
				} else {
					loadTime = time.Since(began)
				}
			}
			if err != nil {
				r.notice(loadedEvent(sessionID, 0, loadTime, err), request.Agent)
				r.noteFailure(err, sessionID, request.Agent)
				return err
			}
			if writer != nil {
				// The Record's epoch is known once it is loaded: the Run uses
				// that Record's Feed, and its stream starts there, with the
				// claim the open wrote.
				acquired = feedKey{sessionID, s.RecordEpoch()}
				feed = r.acquireFeed(acquired, request.Agent)
				if err := r.streamTo(ctx, feed, stream, track); err != nil {
					releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
					_ = writer.Release(releaseCtx)
					cancelRelease()
					return err
				}
			}
		}
		for key, value := range request.Update {
			s.Set(key, value)
		}

		agentName := request.Agent
		if agentName == "" {
			agentName, _ = s.Get(MetaAgent)
		}
		spec, ok := r.Agent(agentName)
		if request.SessionID != "" {
			label := spec.Name
			if !ok {
				label = ""
			}
			feed.SetLabel(label)
			if writer != nil {
				writer.SetPublisher(r.publisher(acquired, label))
			}
			for _, event := range early {
				feed.Publish(event)
			}
			if !reused {
				r.notice(loadedEvent(sessionID, s.Len(), loadTime, nil), label)
			}
		}
		// A Run that cannot start still releases the Session it opened.
		abandon := func(err error) error {
			if writer != nil {
				releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
				_ = writer.Release(releaseCtx)
				cancelRelease()
			}
			return err
		}
		if !ok {
			return abandon(ErrUnknownAgent)
		}
		s.Set(MetaAgent, spec.Name)
		if spec.ModelName != "" {
			s.Set(MetaModel, spec.ModelName)
		}

		onRunStart := gotato.WithRunStart(func(runID gotato.RunID) {
			track.set(runID)
			handle.attachRunID(runID)
			handle.abortIfStopped()
		})
		live, auto, err := r.buildSession(ctx, spec, s, writer, InspectConfig{}, onRunStart, gotato.WithFeed(feed), gotato.WithQueueHandoff())
		if err != nil {
			return abandon(err)
		}
		// The notices that wait in the Record are read before this Run queues
		// any of its own.
		kept := s.PendingNotices()
		if follow == nil && !request.Continue {
			// A message that starts a Run after a long absence is followed by
			// a notice with the time.
			r.queueTimeAway(ctx, spec, s, live)
		}
		if len(kept) > 0 {
			// Notices that were queued and not committed, by an earlier Run or
			// by a call between Runs, on this instance or another, go in after
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
		started = true
		// From here a stop aborts the Run inside the live Session, so Prompt
		// still returns the settled (canceled) result. Prompt waits on the
		// caller's context, not runCtx: a stop that canceled runCtx while the
		// Session was loading reaches the Run through abortIfStopped instead.
		handle.attachAbort(live.Abort)
		handle.attachLive(live)

		// The Run's deadline: the request's Timeout, else the Agent's
		// RunTimeout. The caller's own deadline on ctx overrides it when
		// it is shorter, as any child context's does.
		runDeadline := request.Timeout
		if runDeadline == 0 {
			runDeadline = spec.RunTimeout
		}
		if runDeadline == 0 {
			runDeadline = DefaultRunTimeout
		}
		var result gotato.RunResult
		func() {
			runCtx := ctx
			if runDeadline > 0 {
				var cancelDeadline context.CancelFunc
				runCtx, cancelDeadline = context.WithTimeout(ctx, runDeadline)
				defer cancelDeadline()
			}
			switch {
			case request.Continue:
				result, runErr = live.Continue(runCtx)
			case follow != nil:
				result, runErr = live.Prompt(runCtx, follow.prompt)
			default:
				result, runErr = live.Prompt(runCtx, gotato.UserMessage(request.Prompt))
			}
		}()
		// The messages the Run left in its queues are taken in the same step
		// that stops delivering to it, and a Run follows with them unless
		// something ends the work they were meant for.
		if r.beforeDetach != nil {
			r.beforeDetach()
		}
		// A notice from a service the Run left waits for the next Run, even
		// when the Run was stopped, since it asks for no work: its
		// notice_queued is in the Record, and the next Run queues it again.
		// Gotato's own notices, such as time_away, mattered only at the time.
		notices := handle.detachLive(func(left []gotato.Queued) bool {
			why := ""
			switch {
			case writer == nil:
				why = "run_ended"
			case writer.Failed() != nil:
				why = "writer_failed"
			case r.Draining():
				why = "draining"
			case handle.isStopped() || runCtx.Err() != nil:
				why = "run_ended"
			}
			if why != "" {
				live.DropQueued(left, why)
				return false
			}
			return true
		})
		for _, notice := range notices {
			if notice.Message.Notice != nil && notice.Message.Notice.Source == gotato.NoticeSourceGotato {
				live.DropQueued([]gotato.Queued{notice}, "run_ended")
			}
		}
		_ = live.Close(context.Background())
		if stream != nil && stream.sub != nil {
			stream.sub.End()
		}
		if r.beforeRelease != nil {
			r.beforeRelease()
		}

		if writer != nil {
			// Persist the settled Session even when the Run was canceled, but
			// bound the write so a stalled Store cannot pin the Run (and Drain)
			// forever.
			wroteAll := writer.Failed() == nil
			saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(runCtx), persistTimeout)
			saveErr := writer.Release(saveCtx)
			cancelSave()
			r.notice(savedEvent(s.ID(), result.RunID, s.Len(), writer.Stats().Since(saved), saveErr), spec.Name)
			r.noteFailure(saveErr, s.ID(), spec.Name)
			if saveErr == nil && wroteAll {
				// The Session stays in memory, as a cache the store can always
				// replace; the next Run checks its head.
				r.keepWarm(writer, spec)
			}
			// Only the release failed: every fact of the Run is stored, so the
			// Run keeps its status. The warning Events above tell the operator,
			// and the Claim lapses with the Liveness row.
			if saveErr != nil && !wroteAll {
				// An earlier write of the Run failed, and the Writer refuses
				// the release too: keep the Run error and add the persistence
				// failure so callers can observe both (errors.Is still matches
				// each).
				var saveFailure *gotato.RuntimeError
				if !errors.As(saveErr, &saveFailure) {
					saveFailure = gotato.ErrorOf(gotato.CodeStoreFailure, saveErr.Error())
					saveFailure.Cause = saveErr
					saveErr = saveFailure
				}
				// completed never carries an error. The Run's own writes
				// settle it as failed, so this only guards the rule.
				if result.Status == gotato.RunStatusCompleted {
					result.Status = gotato.RunStatusFailed
					result.Error = saveFailure
				}
				persistErr := error(notPersisted{saveErr})
				if runErr == nil {
					runErr = persistErr
				} else {
					runErr = errors.Join(runErr, persistErr)
				}
			}
		}
		r.noteFailure(runErr, s.ID(), spec.Name)
		out = RunResponse{
			SessionID: s.ID(),
			Agent:     spec.Name,
			Model:     spec.ModelName,
			Result:    result,
			Compacted: auto != nil && compacted(auto),
			Messages:  s.Len(),
			Events:    len(s.Events()),
		}
		if writer != nil {
			out.RecordEpoch = s.RecordEpoch()
		}
		if request.SkipSave {
			// The Run stored nothing: its Events are those of its own stream,
			// numbered from 1, so the last number counts them.
			out.Events = int(feed.Last())
		}
		if result.FinalMessage != nil {
			out.FinalText = gotato.TextOf(*result.FinalMessage)
		}
		if runErr != nil && out.Result.Error == nil {
			var runtimeErr *gotato.RuntimeError
			if errors.As(runErr, &runtimeErr) {
				out.Result.Error = runtimeErr
			} else {
				out.Result.Error = gotato.ErrorOf(gotato.CodeInternalInvariant, runErr.Error())
			}
			if out.Result.Status == "" {
				out.Result.Status = runStatusForError(runErr)
			}
		}
		return nil
	})
	if lockErr != nil {
		if created && !started {
			// A one-shot Run that never started must not leave an empty
			// Session behind. A failed cleanup is reported, not discarded.
			deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
			deleteErr := r.store.Delete(deleteCtx, sessionID)
			cancelDelete()
			if deleteErr != nil {
				lockErr = errors.Join(lockErr, fmt.Errorf("service: cleanup of session %s failed: %w", sessionID, deleteErr))
			}
		}
		if errors.Is(lockErr, context.Canceled) || errors.Is(lockErr, context.DeadlineExceeded) {
			// Canceled while waiting for the Session lock: report a settled
			// canceled Run rather than a bare error.
			return canceledRunResult(sessionID, request.Agent, lockErr), lockErr
		}
		return RunResponse{}, lockErr
	}
	return out, runErr
}

// noteFailure publishes the Instance Event for a write that failed: a
// session_fenced when another Instance holds the Claim, and a
// store_write_failed for any other store failure. A write refused because this
// Instance's Liveness row lapsed is reported by liveness_lapsed instead.
func (r *Runner) noteFailure(err error, sessionID, agent string) {
	if err == nil || lapsed(err) {
		return
	}
	header := gotato.NewHeader(gotato.SessionID(sessionID), "", 0)
	switch {
	case errors.Is(err, session.ErrConflict):
		r.notice(gotato.SessionFenced{Header: header, Instance: r.instance}, agent)
	case gotato.IsCode(err, gotato.CodeStoreFailure):
		r.notice(gotato.StoreWriteFailed{Header: header, Code: gotato.CodeStoreFailure}, agent)
	}
}

// canceledRunResult reports a Run canceled before its Agent started as a
// settled canceled Run.
func canceledRunResult(sessionID, agent string, err error) RunResponse {
	code := gotato.CodeCanceled
	if errors.Is(err, context.DeadlineExceeded) {
		code = gotato.CodeDeadlineExceeded
	}
	return RunResponse{
		SessionID: sessionID,
		Agent:     agent,
		Result:    gotato.RunResult{Status: runStatusForError(err), Error: gotato.ErrorOf(code, err.Error())},
	}
}

// withSessionLock runs fn while holding the Session's single-flight lock. The
// admission queue policy decides whether a busy Session fails (ErrBusy) or the
// caller waits. Every Session mutation goes through it: Runs, compaction,
// tool activation, and deletion.
func (r *Runner) withSessionLock(ctx context.Context, sessionID string, fn func() error) error {
	return r.sessionLock(ctx, sessionID, false, fn)
}

// sessionLock is withSessionLock; forceWait makes the caller wait for a busy
// Session whatever the admission policy, as the Run that follows another does.
func (r *Runner) sessionLock(ctx context.Context, sessionID string, forceWait bool, fn func() error) error {
	// A message that arrives while the Session compacts while idle cancels
	// that compaction and is served first, so it never finds the Session busy.
	unhold, err := r.holdWarm(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unhold()
	onWait := func() {
		r.notice(gotato.RunWaiting{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy"}, "")
	}
	if err := r.locks.acquire(ctx, sessionID, forceWait || r.admission.Queue == QueuePolicyWait, onWait); err != nil {
		if errors.Is(err, ErrBusy) {
			r.notice(gotato.RunRejected{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy"}, "")
		}
		return err
	}
	defer r.locks.release(sessionID)
	return fn()
}

// runStatusForError classifies a terminal error when the Session itself did not
// report a status: a caller-canceled context is a canceled Run, not a
// failure, and the Session's Run record says the same.
func runStatusForError(err error) gotato.RunStatus {
	switch {
	case errors.Is(err, context.Canceled), gotato.IsCode(err, gotato.CodeCanceled):
		return gotato.RunStatusCanceled
	case errors.Is(err, context.DeadlineExceeded), gotato.IsCode(err, gotato.CodeDeadlineExceeded):
		return gotato.RunStatusDeadlineExceeded
	default:
		return gotato.RunStatusFailed
	}
}

func compacted(auto *modelctx.AutoCompactor) bool {
	_, runs := auto.Last()
	return runs > 0
}

// admit decides whether a Run may start, and registers its handle when it
// may. A draining Instance refuses first; then a Run that would activate a
// Session past MaxActiveSessions is refused, because another Instance can
// still take it, unless a warm Session can be evicted to make room; then
// MaxActiveRuns applies to every Run.
func (r *Runner) admit(handle *runHandle) error {
	for {
		victim, err := r.tryAdmit(handle)
		if victim == nil {
			return err
		}
		// Evicting waits for an idle compaction to unwind, so it happens
		// without the Runner's lock; the count is taken again after it.
		r.dropEntry(victim, "evicted")
	}
}

// tryAdmit is one pass of admit under the Runner's lock. When the Run needs
// room and a warm Session can give it, it returns that Session to evict and
// registers nothing.
func (r *Runner) tryAdmit(handle *runHandle) (victim *warmEntry, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining {
		return nil, ErrDraining
	}
	if limit := r.admission.MaxActiveSessions; limit > 0 {
		warm, evictable := r.warmCountLocked(handle.sessionID)
		if (handle.sessionID == "" || (len(r.handles[handle.sessionID]) == 0 && !warm)) && r.liveSessionsLocked() >= limit {
			if evictable == nil {
				return nil, ErrOverloaded
			}
			return evictable, nil
		}
	}
	if r.admission.MaxActiveRuns > 0 && r.inflight >= r.admission.MaxActiveRuns {
		return nil, ErrCapacity
	}
	r.inflight++
	r.handles[handle.sessionID] = append(r.handles[handle.sessionID], handle)
	return nil, nil
}

func (r *Runner) release() {
	r.mu.Lock()
	r.inflight--
	if r.inflight == 0 {
		r.idle.Broadcast()
	}
	r.mu.Unlock()
}
