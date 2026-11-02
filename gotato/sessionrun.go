package gotato

import (
	"context"
	"sync"
	"sync/atomic"
)

// sessionRun is the Run in flight: its ID and Turn, whether its span is open or
// it is ending, how to stop it, and how to cut the model call short for an
// Interrupt. It is the second of the Session's locks in order: events, then
// run, then queue (the queue's own lock is a leaf), and nothing is called into
// the host while mu is held.
type sessionRun struct {
	mu   sync.Mutex
	id   RunID
	turn TurnNumber
	// ending says the Run is writing its end: no Abort starts a write, and no
	// Steer, FollowUp or Interrupt is taken into a queue (see sendControl).
	ending bool
	// open says the Run's span is open: its run_started is being written or is
	// written, and its run_ended is not. Only writeEvents changes it, under
	// events.mu and mu both. abortBeforeStart records an Abort that came
	// before; the write of run_started adds the abort_requested.
	open             bool
	abortBeforeStart bool
	// keeps says the ending Run leaves its queues for the host
	// (WithQueueHandoff), so a message taken in meanwhile is kept, not dropped.
	keeps bool
	// stop cancels the Run; nil when none is in flight.
	stop context.CancelFunc
	// callCancel cancels the model call in flight, which an Interrupt triggers.
	callCancel context.CancelFunc
	// inRun says a Run is in flight, for a caller that must not take mu.
	inRun atomic.Bool
	// aborts counts the AbortRequested writes in flight, so a Run's RunEnded is
	// written after them: it is added to under mu while the Run is not ending,
	// and waited on once it is.
	aborts sync.WaitGroup
	// endingHook, for tests, runs when a Run has begun to write its end.
	endingHook func()
}
