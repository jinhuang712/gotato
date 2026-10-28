package service

import (
	"context"
	"errors"
	"time"

	"github.com/jinhuang712/gotato/session"
)

// drainStopWait is how long Drain waits, after its ctx ended and it stopped the
// Runs, for them to settle: a Run that ignores its ctx returns from Prompt after
// 5 seconds, and its end is written within session.WriteTimeout.
const drainStopWait = 5*time.Second + session.WriteTimeout

// stopWait returns drainStopWait, or the value a test set.
func (r *Runner) stopWait() time.Duration {
	if r.drainStop > 0 {
		return r.drainStop
	}
	return drainStopWait
}

// stopAll stops every admitted Run for the drain: started Runs through their
// live Session, Runs still waiting for a Session lock through their context.
func (r *Runner) stopAll() {
	r.mu.Lock()
	var all []*runHandle
	for _, list := range r.handles {
		all = append(all, list...)
	}
	r.mu.Unlock()
	for _, handle := range all {
		handle.stopForDrain()
	}
}

// Drain stops this Instance in order, so a deploy loses no work:
//
//  1. its Liveness row says draining, so every Instance can see it;
//  2. Draining reports true, so /readyz answers 503, and the Runner admits
//     no new Run and takes no Session over from another Instance
//     (ErrDraining); creating or forking a Session activates nothing, so it
//     still works;
//  3. every Run in flight finishes, is saved, and releases its Session; so
//     does every Compact, Notice, SetToolActive and DeleteSession in flight.
//
// Draining reports true from the first moment of Drain, before the write of
// the row, so that no Run is admitted while a slow store takes it.
//
// Drain waits for the Runs at most Config.DrainTimeout (DefaultDrainTimeout,
// ten seconds, when it is zero), or until ctx ends, whichever comes first.
//
// When that time ends first, the Runs still going are stopped, saved and released,
// and each returns with a Reconnect once every Run of the drain has settled;
// Drain then returns that context's error (context.DeadlineExceeded when the
// timeout ended it). It waits for them at most drainStopWait after
// ctx ended (the time a Run needs to settle and write its end), so that a tool
// that ignores its context cannot hold Drain for good; the Run goes on
// settling in the background, and its Session is fenced by the next Claim if it
// is not saved. A Run that cannot be saved in time is fenced by the next Claim
// anyway. The Runner keeps renewing its row while it drains;
// call Close afterwards to remove it. A signal handler or an endpoint of your
// own calls Drain.
func (r *Runner) Drain(ctx context.Context) error {
	r.mu.Lock()
	first := r.drained == nil
	if first {
		r.drained = make(chan struct{})
	}
	drained := r.drained
	r.mu.Unlock()
	var markErr error
	if first {
		// Draining comes first: the write of the row can take as long as the
		// store does, and a Run admitted meanwhile would start on an Instance
		// that other Instances already see leaving.
		r.mu.Lock()
		r.draining = true
		r.mu.Unlock()
		markErr = r.markDraining(ctx)
		go func() {
			r.mu.Lock()
			for r.inflight > 0 || r.locked > 0 {
				r.idle.Wait()
			}
			r.mu.Unlock()
			close(drained)
		}()
	}
	waitCtx := ctx
	if r.drainTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, r.drainTimeout)
		defer cancel()
	}
	select {
	case <-drained:
		r.dropAllWarm("drain")
		r.endStreams()
		return markErr
	case <-waitCtx.Done():
		r.stopAll()
		select {
		case <-drained:
		// [LIVENESS] Drain waits for stopped Runs only so long.
		case <-time.After(r.stopWait()):
		}
		r.dropAllWarm("drain")
		r.endStreams()
		return errors.Join(markErr, waitCtx.Err())
	}
}
