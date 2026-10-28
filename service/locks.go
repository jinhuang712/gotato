package service

import (
	"context"
	"errors"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
)

// sessionLocks serializes Runs per Session inside one process. Continuity
// lives in the Store, so this lock only protects the Session document from
// two Runs appending at once; a multi-process deployment adds a Store-level
// lease on top.
type sessionLocks struct {
	mu    sync.Mutex
	locks map[string]*sessionLock
}

type sessionLock struct {
	held bool
	// waiters is a FIFO of channels. release closes the head, transferring the
	// held lock to that waiter, so a busy Session cannot starve a queued Run.
	waiters []chan struct{}
}

func newSessionLocks() *sessionLocks { return &sessionLocks{locks: map[string]*sessionLock{}} }

// acquire takes the lock for id. When wait is false a busy Session fails
// with ErrBusy; otherwise the caller waits until the lock frees or ctx ends.
func (l *sessionLocks) acquire(ctx context.Context, id string, wait bool, onWait func()) error {
	l.mu.Lock()
	lock := l.locks[id]
	if lock == nil {
		lock = &sessionLock{}
		l.locks[id] = lock
	}
	if !lock.held && len(lock.waiters) == 0 {
		lock.held = true
		l.mu.Unlock()
		return nil
	}
	if !wait {
		l.mu.Unlock()
		return ErrBusy
	}
	ready := make(chan struct{})
	lock.waiters = append(lock.waiters, ready)
	l.mu.Unlock()
	if onWait != nil {
		onWait()
	}
	select {
	case <-ready:
		// release handed the lock to this waiter. Honour a cancellation that
		// arrived meanwhile, without leaking the lock.
		if err := ctx.Err(); err != nil {
			l.release(id)
			return err
		}
		return nil
	case <-ctx.Done():
		l.mu.Lock()
		removed := false
		for i, candidate := range lock.waiters {
			if candidate == ready {
				lock.waiters = append(lock.waiters[:i], lock.waiters[i+1:]...)
				removed = true
				break
			}
		}
		l.mu.Unlock()
		if !removed {
			// release already granted ownership; hand it on.
			l.release(id)
		}
		return ctx.Err()
	}
}

func (l *sessionLocks) release(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := l.locks[id]
	if lock == nil || !lock.held {
		return
	}
	if len(lock.waiters) > 0 {
		next := lock.waiters[0]
		lock.waiters = lock.waiters[1:]
		close(next) // ownership transfers; held stays true
		return
	}
	lock.held = false
	delete(l.locks, id)
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
