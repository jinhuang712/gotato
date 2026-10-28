package service

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// CreateSession creates and saves a Session bound to an Agent. It fails with
// ErrSessionExists when an ID given in options is taken.
func (r *Runner) CreateSession(ctx context.Context, agent string, metadata map[string]string, options ...session.RecordOption) (*session.Record, error) {
	s, spec, err := r.newSession(agent, metadata, options...)
	if err != nil {
		return nil, err
	}
	// Nothing runs against a Session that is only created: it is released in
	// the write that creates it.
	if _, err := r.create(ctx, s, spec.Name, session.WithIdle()); err != nil {
		return nil, err
	}
	return s, nil
}

// newSession builds a Session bound to an Agent, without saving it.
func (r *Runner) newSession(agent string, metadata map[string]string, options ...session.RecordOption) (*session.Record, Agent, error) {
	spec, ok := r.Agent(agent)
	if !ok {
		return nil, Agent{}, ErrUnknownAgent
	}
	s := session.New(options...)
	for key, value := range metadata {
		s.Set(key, value)
	}
	s.Set(MetaAgent, spec.Name)
	return s, spec, nil
}

// CreateSessionExclusive creates a Session with an explicit ID and fails with
// ErrSessionExists when the ID is taken. The store's Create is the check, so
// two concurrent creates cannot both succeed, also on two Instances.
func (r *Runner) CreateSessionExclusive(ctx context.Context, agent, id string, metadata map[string]string) (*session.Record, error) {
	if strings.TrimSpace(id) == "" {
		return nil, gotato.ErrorOf(gotato.CodeInvalidArgument, "service: session ID is required")
	}
	return r.CreateSession(ctx, agent, metadata, session.WithID(id))
}

// DeleteSession removes a Session under the Session lock, so a Run in flight
// is never resurrected by a delete or vice versa.
func (r *Runner) DeleteSession(ctx context.Context, sessionID string) error {
	return r.withSessionLock(ctx, sessionID, func() error {
		r.dropWarm(sessionID, "deleted")
		if err := r.store.Delete(ctx, sessionID); err != nil {
			return err
		}
		r.notice(gotato.SessionDeleted{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0)}, "")
		r.endSessionStreams(sessionID, session.ErrNotFound)
		return nil
	})
}

// Fork creates a new Session from an existing one. An explicit newID must be
// free: a fork never overwrites an existing Session, and a taken ID is
// ErrSessionExists, as CreateSessionExclusive reports it.
func (r *Runner) Fork(ctx context.Context, sessionID, newID string) (*session.Record, error) {
	parent, err := session.View(ctx, r.store, sessionID)
	if err != nil {
		return nil, err
	}
	var options []session.RecordOption
	if newID != "" {
		options = append(options, session.WithID(newID))
	}
	child := session.Fork(parent, options...)
	// The child starts with its parent's history, so it keeps its parent's
	// cache key and its first Turn reads the parent's cached prefix.
	child.Set(MetaCacheKey, sessionCacheKey(parent))
	// The store's Create is the existence check, so two concurrent forks with
	// the same explicit newID cannot both succeed.
	childAgent, _ := child.Get(MetaAgent)
	if _, err := r.create(ctx, child, childAgent, session.WithIdle()); err != nil {
		return nil, err
	}
	return child, nil
}

// AbortSession stops the Run in flight on a Session: everything in flight is
// canceled through its context, and the Run settles as canceled with every
// Turn it committed. It is the Stop of the messages a Run takes (Send).
//
// A Run going on another live Instance is not stopped from here: the call
// returns not_writer (NotRun, Retryable) with the writer's advertised address,
// as Send does. With no Run going anywhere it returns ErrNoRun, and for a
// Session the store does not know (or refuses the caller), not_found.
func (r *Runner) AbortSession(ctx context.Context, sessionID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	handles := append([]*runHandle(nil), r.handles[sessionID]...)
	r.mu.Unlock()
	// A Run that has a live Session has read the Record; one still loading it
	// has not, and the Record may not exist yet.
	if slices.ContainsFunc(handles, (*runHandle).hasLive) {
		if err := r.reachStore(ctx, sessionID); err != nil {
			return err
		}
	}
	if r.abortSession(sessionID) {
		return nil
	}
	if err := r.checkWriter(ctx, sessionID); err != nil {
		return err
	}
	return ErrNoRun
}

// abortSession cancels the started Run for a Session, or the oldest Run still
// waiting for its lock when none has started.
func (r *Runner) abortSession(sessionID string) bool {
	r.mu.Lock()
	waiting := append([]*runHandle(nil), r.handles[sessionID]...)
	r.mu.Unlock()
	var chosen *runHandle
	for _, handle := range waiting {
		handle.mu.Lock()
		started := handle.runID != ""
		handle.mu.Unlock()
		if started {
			chosen = handle
			break
		}
	}
	if chosen == nil && len(waiting) > 0 {
		chosen = waiting[0]
	}
	if chosen == nil {
		return false
	}
	chosen.stop()
	return true
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

// retargetHandle moves an admitted Run to the Session ID it turned out to own.
// A Run registers before its Session is loaded, so a newly created Session's
// generated ID has to be attached afterwards.
func (r *Runner) retargetHandle(handle *runHandle, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if handle.sessionID == sessionID {
		return
	}
	list := r.handles[handle.sessionID]
	for i, candidate := range list {
		if candidate == handle {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.handles, handle.sessionID)
	} else {
		r.handles[handle.sessionID] = list
	}
	handle.sessionID = sessionID
	r.handles[sessionID] = append(r.handles[sessionID], handle)
}

// unregisterHandle drops an admitted Run and its RunID mapping.
func (r *Runner) unregisterHandle(handle *runHandle) {
	handle.finish()
	defer handle.overOnce.Do(func() { close(handle.over) })
	r.mu.Lock()
	list := r.handles[handle.sessionID]
	for i, candidate := range list {
		if candidate == handle {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.handles, handle.sessionID)
	} else {
		r.handles[handle.sessionID] = list
	}
	r.mu.Unlock()
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
