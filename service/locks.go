package service

import (
	"context"
	"sync"
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
	held    bool
	waiters int
	ch      chan struct{} // closed on release
}

func newSessionLocks() *sessionLocks { return &sessionLocks{locks: map[string]*sessionLock{}} }

// acquire takes the lock for id. When wait is false a busy Session fails
// with ErrBusy; otherwise the caller waits until the lock frees or ctx ends.
func (l *sessionLocks) acquire(ctx context.Context, id string, wait bool) error {
	for {
		l.mu.Lock()
		lock := l.locks[id]
		if lock == nil {
			lock = &sessionLock{}
			l.locks[id] = lock
		}
		if !lock.held {
			lock.held = true
			lock.ch = make(chan struct{})
			l.mu.Unlock()
			return nil
		}
		if !wait {
			l.mu.Unlock()
			return ErrBusy
		}
		lock.waiters++
		released := lock.ch
		l.mu.Unlock()
		select {
		case <-released:
		case <-ctx.Done():
			l.mu.Lock()
			lock.waiters--
			l.mu.Unlock()
			return ctx.Err()
		}
		l.mu.Lock()
		lock.waiters--
		l.mu.Unlock()
	}
}

func (l *sessionLocks) release(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := l.locks[id]
	if lock == nil || !lock.held {
		return
	}
	lock.held = false
	close(lock.ch)
	if lock.waiters == 0 {
		delete(l.locks, id)
	}
}
