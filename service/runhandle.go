package service

import (
	"context"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
)

// runHandle is one admitted Run. It exists from admission until the Run
// settles, so a Run still waiting on the Session lock is visible to
// AbortSession and to Drain instead of being an untracked inflight slot.
type runHandle struct {
	sessionID string

	mu      sync.Mutex
	runID   gotato.RunID
	abort   func()
	cancel  context.CancelFunc
	stopped bool
	// wait makes the Run wait for a busy Session whatever the admission
	// policy: a message that waited for the Run before it to end.
	wait bool
	// over is closed when the Run is unregistered, after it has released its
	// Session.
	over     chan struct{}
	overOnce sync.Once
	// drained says the drain stopped this Run at its deadline.
	drained bool
	// liveMu guards live, the Run's live Session while it runs, where a
	// message for the Run goes (see messages.go). ready is closed once there
	// is one, or the Run is over.
	liveMu sync.Mutex
	live   *gotato.Session
	// liveChanged is closed, and replaced, whenever live is attached or
	// withdrawn or the Run finishes (guarded by liveMu).
	liveChanged chan struct{}
	ready       chan struct{}
	readyClosed bool
	// next holds the messages a settled Run left in its queues, for the Run that
	// follows it (guarded by liveMu).
	next []gotato.Queued
}

// attachAbort installs the started-Run cancellation path. Until it is
// installed, cancel stops the pre-start wait instead.
func (h *runHandle) attachAbort(abort func()) {
	h.mu.Lock()
	h.abort = abort
	h.mu.Unlock()
}

// abortIfStopped aborts a Run that was stopped before its live Session had a
// Run to abort: Session.Abort is a no-op until the Run starts. The run-start
// hook that startRun installs calls it.
func (h *runHandle) abortIfStopped() {
	h.mu.Lock()
	stopped, abort := h.stopped, h.abort
	h.mu.Unlock()
	if stopped && abort != nil {
		abort()
	}
}

// attachRunID records the RunID the Session assigned. The first one wins.
func (h *runHandle) attachRunID(runID gotato.RunID) {
	h.mu.Lock()
	if h.runID == "" {
		h.runID = runID
	}
	h.mu.Unlock()
}

// stop aborts a started Run, or the pre-start wait when none started yet.
//
// Once the live Session is built, the Run is stopped only through Abort, never
// by canceling the context Prompt waits on: Prompt then returns the settled
// canceled result, with its RunID, usage and metrics, instead of giving up on
// it. A stop that lands before the Session has a Run to abort is kept, and
// abortIfStopped applies it at run_start. Before the live Session exists,
// canceling the context is what reaches a Run that is waiting for the
// Session lock or loading the Session.
func (h *runHandle) stop() {
	h.mu.Lock()
	h.stopped = true
	abort := h.abort
	cancel := h.cancel
	h.mu.Unlock()
	if abort != nil {
		abort()
		return
	}
	if cancel != nil {
		cancel()
	}
}

// stopForDrain stops the Run because the drain reached its deadline.
func (h *runHandle) stopForDrain() {
	h.mu.Lock()
	h.drained = true
	h.mu.Unlock()
	h.stop()
}

// stoppedByDrain reports whether the drain stopped this Run.
func (h *runHandle) stoppedByDrain() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.drained
}

// attachLive makes the Run's live Session reachable for messages.
func (h *runHandle) attachLive(live *gotato.Session) {
	h.liveMu.Lock()
	h.live = live
	h.closeReadyLocked()
	h.signalLiveLocked()
	h.liveMu.Unlock()
}

// signalLiveLocked wakes whoever waits for a change of the live Session.
func (h *runHandle) signalLiveLocked() {
	if h.liveChanged != nil {
		close(h.liveChanged)
		h.liveChanged = nil
	}
}

// awaitLiveChange blocks while the Run has a live Session that refused a
// message, until it is withdrawn or replaced, the Run is over, or ctx ends. It
// returns at once when there is none.
func (h *runHandle) awaitLiveChange(ctx context.Context) error {
	h.liveMu.Lock()
	if h.live == nil {
		h.liveMu.Unlock()
		return nil
	}
	if h.liveChanged == nil {
		h.liveChanged = make(chan struct{})
	}
	changed := h.liveChanged
	h.liveMu.Unlock()
	select {
	case <-changed:
		return nil
	case <-h.over:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *runHandle) closeReadyLocked() {
	if !h.readyClosed {
		h.readyClosed = true
		close(h.ready)
	}
}

// readyChan is closed once the Run has a live Session, or is over.
func (h *runHandle) readyChan() <-chan struct{} {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	return h.ready
}

// detachLive withdraws the live Session once its Run has settled, and takes
// what its queues still hold, in one step under the lock a message is
// delivered under: a message is either in the Run's queue here, or it finds no
// live Session and starts a Run of its own. When the Run is to be followed by
// another (follow), the handle waits for that Run's live Session, so a
// message that arrives meanwhile queues behind the leftovers instead of
// jumping ahead of them.
//
// The notices the Run left are returned and not followed: a notice asks for
// no work, so it waits for the next Run instead of starting one.
func (h *runHandle) detachLive(follow func([]gotato.Queued) bool) (notices []gotato.Queued) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	live := h.live
	h.live = nil
	h.signalLiveLocked()
	if live == nil {
		return nil
	}
	notices, left := splitNotices(live.TakeQueued())
	if len(left) > 0 && follow(left) {
		h.next = left
		h.ready, h.readyClosed = make(chan struct{}), false
	}
	return notices
}

// takeNext returns the leftovers the settled Run handed over, once.
func (h *runHandle) takeNext() []gotato.Queued {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	next := h.next
	h.next = nil
	return next
}

// finish says the Run is over, so a message that waited for its live Session
// stops waiting.
func (h *runHandle) finish() {
	h.liveMu.Lock()
	h.closeReadyLocked()
	h.signalLiveLocked()
	h.liveMu.Unlock()
}

// hasLive reports whether the Run has a live Session now.
func (h *runHandle) hasLive() bool {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	return h.live != nil
}

// send puts the message in the live Session's queue, or fails with
// invalid_state when the Run has no live Session any more.
func (h *runHandle) send(kind MessageKind, message gotato.Message) error {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	live := h.live
	if live == nil {
		return gotato.ErrorOf(gotato.CodeInvalidState, "service: the run is over")
	}
	switch kind {
	case MessageKindFollowUp:
		return live.FollowUp(message)
	case MessageKindInterrupt:
		return live.Interrupt(message)
	default:
		return live.Steer(message)
	}
}

// isStopped reports whether a Stop reached the Run.
func (h *runHandle) isStopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}
