package host

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

// tryAcquire takes the lock for id, or fails with ErrBusy when the Session is
// busy.
func (l *sessionLocks) tryAcquire(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, taken := l.takeLocked(id); !taken {
		return ErrBusy
	}
	return nil
}

// acquire takes the lock for id, and waits until the lock frees or ctx ends
// when the Session is busy.
func (l *sessionLocks) acquire(ctx context.Context, id string, onWait func()) error {
	l.mu.Lock()
	lock, taken := l.takeLocked(id)
	if taken {
		l.mu.Unlock()
		return nil
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
		if !l.dropWaiter(lock, ready) {
			// release already granted ownership; hand it on.
			l.release(id)
		}
		return ctx.Err()
	}
}

// takeLocked returns the lock for id, made when there is none, and takes it
// when it is free and nobody waits for it. The caller holds l.mu.
func (l *sessionLocks) takeLocked(id string) (*sessionLock, bool) {
	lock := l.locks[id]
	if lock == nil {
		lock = &sessionLock{}
		l.locks[id] = lock
	}
	if !lock.held && len(lock.waiters) == 0 {
		lock.held = true
		return lock, true
	}
	return lock, false
}

// dropWaiter takes the waiter ready off the lock's queue, and reports whether
// it was still there.
func (l *sessionLocks) dropWaiter(lock *sessionLock, ready chan struct{}) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, candidate := range lock.waiters {
		if candidate == ready {
			lock.waiters = append(lock.waiters[:i], lock.waiters[i+1:]...)
			return true
		}
	}
	return false
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
	return r.sessionLock(ctx, sessionID, r.bounds.Queue, fn)
}

// sessionLock is withSessionLock under the queue policy queue, which a caller
// sets to QueuePolicyWait to wait for a busy Session whatever the admission
// policy, as the Run that follows another does.
func (r *Runner) sessionLock(ctx context.Context, sessionID string, queue QueuePolicy, fn func() error) error {
	// A message that arrives while the Session compacts while idle cancels
	// that compaction and is served first, so it never finds the Session busy.
	defer r.runs.Locked()()
	unhold, err := r.holdWarm(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unhold()
	onWait := func() {
		r.notice(RunWaiting{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy"}, "")
	}
	if queue == QueuePolicyWait {
		err = r.locks.acquire(ctx, sessionID, onWait)
	} else {
		err = r.locks.tryAcquire(sessionID)
	}
	if err != nil {
		if errors.Is(err, ErrBusy) {
			r.notice(RunRejected{Header: gotato.NewHeader(gotato.SessionID(sessionID), "", 0), Why: "busy"}, "")
		}
		return err
	}
	defer r.locks.release(sessionID)
	return fn()
}
