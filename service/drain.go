package service

import (
	"context"
	"errors"
)

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
//  3. every Run in flight finishes, is saved, and releases its Session.
//
// When ctx ends first, the Runs still going are stopped, saved and released,
// and each returns with a Reconnect once every Run of the drain has settled;
// Drain then returns ctx.Err(). A Run that cannot be saved in time is fenced
// by the next Claim anyway. The Runner keeps renewing its row while it drains;
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
		markErr = r.markDraining(ctx)
		r.mu.Lock()
		r.draining = true
		r.mu.Unlock()
		go func() {
			r.mu.Lock()
			for r.inflight > 0 {
				r.idle.Wait()
			}
			r.mu.Unlock()
			close(drained)
		}()
	}
	select {
	case <-drained:
		r.dropAllWarm("drain")
		r.endStreams()
		return markErr
	case <-ctx.Done():
		r.stopAll()
		<-drained
		r.dropAllWarm("drain")
		r.endStreams()
		return errors.Join(markErr, ctx.Err())
	}
}
