package service

import (
	"context"
	"errors"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

// detach ends a settled Run's live Session: the messages it left in its
// queues are taken in the same step that stops delivering to it, and a Run
// follows with them unless something ends the work they were meant for. It
// then closes the live Session and lets the Run's stream finish before the
// save.
func (st *runState) detach(runCtx context.Context, live *gotato.Session) {
	r, handle := st.r, st.handle
	if r.beforeDetach != nil {
		r.beforeDetach()
	}
	// A notice from a service the Run left waits for the next Run, even
	// when the Run was stopped, since it asks for no work: its
	// notice_queued is in the Record, and the next Run queues it again.
	// Gotato's own notices, such as time_away, mattered only at the time.
	notices := handle.detachLive(func(left []gotato.Queued) bool {
		why := st.dropReason(runCtx)
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
	// End is idempotent: here it lets the stream finish before the save, and
	// serve's deferred call covers the returns that never get this far.
	if st.stream != nil && st.stream.sub != nil {
		st.stream.sub.End()
	}
}

// dropReason says why the messages a settled Run left must not start another
// Run; empty means they may.
func (st *runState) dropReason(runCtx context.Context) string {
	switch {
	case st.writer == nil:
		return "run_ended"
	case st.writer.Failed() != nil:
		return "writer_failed"
	case st.r.Draining():
		return "draining"
	case st.handle.isStopped() || runCtx.Err() != nil:
		return "run_ended"
	}
	return ""
}

// persist saves the settled Session, even when the Run was canceled, and
// reports the save. A failed write of the Run is added to its error.
func (st *runState) persist(runCtx context.Context, spec Agent, result *gotato.RunResult) {
	r, s, writer := st.r, st.s, st.writer
	if writer == nil {
		return
	}
	// Bound the write so a stalled Store cannot pin the Run (and Drain)
	// forever.
	wroteAll := writer.Failed() == nil
	saveCtx, cancelSave := persistContext(runCtx)
	saveErr := writer.Release(saveCtx)
	cancelSave()
	r.notice(savedEvent(s.ID(), result.RunID, s.Len(), writer.Stats().Since(st.saved), saveErr), spec.Name)
	r.noteFailure(saveErr, s.ID(), spec.Name)
	r.settle(writer, saveErr)
	if saveErr == nil && wroteAll {
		// The Session stays in memory, as a cache the store can always
		// replace; the next Run checks its head.
		r.keepWarm(writer, spec)
	}
	// A failed release or step leaves the Session busy under this Instance,
	// whose Liveness row goes on renewing: settle has the heartbeat open it
	// again and release it (repairReleases). When only the release failed,
	// every fact of the Run is stored, so the Run keeps its status; the
	// warning Events above tell the operator.
	if saveErr != nil && !wroteAll {
		// An earlier write of the Run failed, and the Writer refuses
		// the release too: keep the Run error and add the persistence
		// failure so callers can observe both (errors.Is still matches
		// each).
		var saveFailure *gotato.RuntimeError
		if !errors.As(saveErr, &saveFailure) {
			saveFailure = foreignError(gotato.CodeStoreFailure, "service: the Run could not be saved", saveErr)
			saveErr = saveFailure
		}
		// completed never carries an error. The Run's own writes
		// settle it as failed, so this only guards the rule.
		if result.Status == gotato.RunStatusCompleted {
			result.Status = gotato.RunStatusFailed
			result.Error = saveFailure
		}
		persistErr := error(notPersisted{saveErr})
		if st.runErr == nil {
			st.runErr = persistErr
		} else {
			st.runErr = errors.Join(st.runErr, persistErr)
		}
	}
}

// respond builds the Run's response from its settled result.
func (st *runState) respond(spec Agent, auto *modelctx.AutoCompactor, result gotato.RunResult) {
	s, runErr := st.s, st.runErr
	st.r.noteFailure(runErr, s.ID(), spec.Name)
	out := RunResponse{
		SessionID: s.ID(),
		Agent:     spec.Name,
		Model:     spec.ModelName,
		Result:    result,
		Compacted: auto != nil && compacted(auto),
		Messages:  s.Len(),
		Events:    len(s.Events()),
	}
	if st.writer != nil {
		out.RecordEpoch = s.RecordEpoch()
	}
	if st.request.SkipSave {
		// The Run stored nothing: its Events are those of its own stream,
		// numbered from 1, so the last number counts them.
		out.Events = int(st.feed.Last())
	}
	if result.FinalMessage != nil {
		out.FinalText = gotato.TextOf(*result.FinalMessage)
	}
	if runErr != nil && out.Result.Error == nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(runErr, &runtimeErr) {
			out.Result.Error = runtimeErr
		} else {
			out.Result.Error = foreignError(gotato.CodeInternalInvariant, "service: the Run failed", runErr)
		}
		if out.Result.Status == "" {
			out.Result.Status = runStatusForError(runErr)
		}
	}
	st.out = out
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
		r.notice(gotato.SessionFenced{Header: header, Instance: r.id()}, agent)
	case gotato.IsCode(err, gotato.CodeStoreFailure):
		r.notice(gotato.StoreWriteFailed{Header: header, Code: gotato.CodeStoreFailure}, agent)
	}
}

// foreignError builds the RuntimeError of a failure whose text the service did
// not write (a Store's error, say). Message reaches HTTP clients, so it is the
// text of a gotato.PublicError that err is or wraps, else the fixed fallback;
// err itself stays on the Cause for logs.
func foreignError(code gotato.ErrorCode, fallback string, err error) *gotato.RuntimeError {
	message := fallback
	var public gotato.PublicError
	if errors.As(err, &public) {
		message = public.Public()
	}
	e := gotato.ErrorOf(code, message)
	e.Cause = err
	return e
}

// canceledRunResult reports a Run canceled before its Agent started as a
// settled canceled Run.
func canceledRunResult(sessionID, agent string, err error) RunResponse {
	code, message := gotato.CodeCanceled, "service: the Run was canceled before it started"
	if errors.Is(err, context.DeadlineExceeded) {
		code, message = gotato.CodeDeadlineExceeded, "service: the Run timed out before it started"
	}
	return RunResponse{
		SessionID: sessionID,
		Agent:     agent,
		Result:    gotato.RunResult{Status: runStatusForError(err), Error: foreignError(code, message, err)},
	}
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
