package service

import (
	"context"
	"errors"
	"time"

	"github.com/jinhuang712/gotato/service/internal/clock"
	"github.com/jinhuang712/gotato/store"
)

// drainStopWait is how long Drain waits, after its ctx ended and it stopped the
// Runs, for them to settle: a Run that ignores its ctx returns from Prompt after
// 5 seconds, and its end is written within store.WriteTimeout.
const drainStopWait = 5*time.Second + store.WriteTimeout

// stopWait returns drainStopWait, or the value a test set.
func (r *Runner) stopWait() time.Duration {
	if r.hooks.drainStop > 0 {
		return r.hooks.drainStop
	}
	return drainStopWait
}

// stopAll stops every admitted Run for the drain: started Runs through their
// live Session, Runs still waiting for a Session lock through their context.
func (r *Runner) stopAll() {
	for _, handle := range r.runs.All() {
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
	// Draining comes first: the write of the row can take as long as the
	// store does, and a Run admitted meanwhile would start on an Instance
	// that other Instances already see leaving.
	drained, first := r.runs.Drain()
	var markErr error
	if first {
		markErr = r.markDraining(ctx)
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
		stopped, cancelStop := clock.After(r.clock, r.stopWait())
		defer cancelStop()
		select {
		case <-drained:
		// [LIVENESS] a Run that ignores its stop would hold Drain for good.
		case <-stopped:
		}
		r.dropAllWarm("drain")
		r.endStreams()
		return errors.Join(markErr, waitCtx.Err())
	}
}
