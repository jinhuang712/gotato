package gotato

import (
	"context"
	"sync"
	"sync/atomic"
)

// Abort cancels the current Run, and writes an AbortRequested Event before the
// Run's RunEnded. It does not close the Session, and it is a no-op when no Run
// is active.
func (a *Session) Abort() { a.abort() }

// abort cancels the Run in flight, if any, and reports whether there was one.
func (a *Session) abort() bool {
	a.run.mu.Lock()
	stop := a.run.stop
	if stop == nil || a.run.ending {
		// No Run, or one that is writing its end: an abort_requested after
		// run_ended would say the Abort came after the Run.
		a.run.mu.Unlock()
		return false
	}
	if !a.run.open {
		// The Run's run_started is not written yet, and an abort_requested
		// before it would name a Run that has not begun: the Run only stops,
		// and the write of run_started adds the Event (see openSpan).
		a.run.abortBeforeStart = true
		a.run.mu.Unlock()
		stop()
		return true
	}
	// The Run stops at once; the Event is written beside it, and the Run's
	// last write waits for it. It is counted while the Run cannot yet be
	// ending.
	a.run.aborts.Add(1)
	a.run.mu.Unlock()
	go func() {
		defer a.run.aborts.Done()
		ctx, cancel := finalContext(context.Background())
		defer cancel()
		_ = a.writeNow(ctx, AbortRequested{Header: a.nowHeader()})
	}()
	stop()
	return true
}

// admissionError is the error for an operation on a Session that is closing
// or closed, and nil otherwise.
func (a *Session) admissionError(operation string) error {
	switch a.Status() {
	case SessionStatusClosing:
		return runtimeError(CodeSessionClosing, operation, "Session is closing", nil)
	case SessionStatusClosed:
		return runtimeError(CodeSessionClosed, operation, "Session is closed", nil)
	default:
		return nil
	}
}

// Close stops the Session: it takes no new Run, lets the one in flight finish
// (Abort ends it sooner), and drops the messages still queued. It returns when
// the Session's goroutine is done, or ctx.Err() if ctx ends first, and it is
// safe to call more than once.
func (a *Session) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.life.closeOnce.Do(func() {
		a.life.closeRequested.Store(true)
		a.life.setStatus(SessionStatusClosing)
		close(a.life.closeSignal)
	})
	select {
	case <-a.life.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForIdle blocks until the Session is idle. It returns ctx.Err() if ctx
// ends first, and an error when the Session is closing or closed.
func (a *Session) WaitForIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		// Capture the change signal before reading the Status, so a status
		// transition that lands in between closes the captured channel and
		// wakes this loop instead of being missed.
		changed := a.life.changeSignal()
		switch a.Status() {
		case SessionStatusIdle:
			return nil
		case SessionStatusCreated, SessionStatusBusy:
			select {
			case <-changed:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return a.admissionError("WaitForIdle")
		}
	}
}

// loop is the Session goroutine: it takes one command at a time, and runs it
// to its end, until Close.
func (a *Session) loop() {
	a.life.setStatus(SessionStatusIdle)
	close(a.life.ready)
	defer a.finishClose()

	for {
		select {
		case <-a.life.closeSignal:
			return
		case cmd := <-a.life.commands:
			if closing := a.serve(cmd); closing {
				return
			}
		}
	}
}

// serve runs one Prompt or Continue command and delivers its result. It
// reports whether the Session is closing, so its goroutine ends.
func (a *Session) serve(cmd sessionCommand) (closing bool) {
	operation := "Prompt"
	if cmd.kind == commandContinue {
		operation = "Continue"
	}
	err := a.admissionError(operation)
	if err == nil && cmd.kind == commandContinue {
		if messages, failure := a.history.messages(); failure != nil {
			err = failure
		} else {
			err = validateContinuable(messages)
		}
	}
	if err != nil {
		a.reply(cmd, promptResponse{err: err}, false)
		return false
	}
	a.life.setStatus(SessionStatusBusy)
	var prompt *Message
	if cmd.kind == commandPrompt {
		prompt = &cmd.message
	}
	result, err := a.executeRun(cmd.ctx, prompt)
	closing = a.life.closeRequested.Load()
	a.reply(cmd, promptResponse{result: result, err: err}, !closing)
	return closing
}

// reply gives the admission token back, records Idle when idle is set, and
// only then delivers resp to the caller.
// [CONCURRENCY] the caller that receives resp must be able to start its next operation at once, not find this command's token held and be told Busy.
func (a *Session) reply(cmd sessionCommand, resp promptResponse, idle bool) {
	<-a.life.admission
	if idle {
		a.life.setStatus(SessionStatusIdle)
	}
	cmd.result <- resp
}

func (a *Session) finishClose() {
	a.dropLeftoverControl()
	a.life.setStatus(SessionStatusClosed)
	if a.events.ownFeed {
		// A Session that owns its Feed says it left, and ends its streams.
		// A host's Feed outlives the Session, and the host closes it.
		a.events.feed.Publish(SessionUnloaded{Header: NewHeader(a.id, "", 0), Why: "close"})
		a.events.feed.Close()
	}
	a.life.doneOnce.Do(func() { close(a.life.done) })
}

// dropLeftoverControl reports a message that waited in a queue when the Session
// closed, which no Run will take: one that arrived after its Run had written
// its last boundary. Each is a MessageDropped, so a caller that was told it
// was queued also learns that it was not taken.
func (a *Session) dropLeftoverControl() {
	for _, queued := range a.queue.seal() {
		_ = a.writeNow(context.Background(), MessageDropped{Header: a.nowHeader(), MessageID: queued.message.ID, Source: queued.source, Why: "session_closed"})
	}
}

// sessionLife is the Session's life: the commands its goroutine takes, the
// token that admits one Run at a time, its status and its close. It holds no
// lock: its state is channels and atomics, so a caller never waits on it.
type sessionLife struct {
	commands  chan sessionCommand
	admission chan struct{}
	// status is the encoded SessionStatus; changed is the channel closed, and
	// replaced, at every change of it, for WaitForIdle.
	status  atomic.Uint32
	changed atomic.Pointer[chan struct{}]
	// closeRequested says Close was called; closeSignal is closed by the first.
	closeRequested atomic.Bool
	closeSignal    chan struct{}
	closeOnce      sync.Once
	// ready is closed when the goroutine is serving, done when it has ended.
	ready    chan struct{}
	done     chan struct{}
	doneOnce sync.Once
}

// init makes the channels; it is called once, before the goroutine starts.
func (l *sessionLife) init() {
	l.commands = make(chan sessionCommand)
	l.admission = make(chan struct{}, 1)
	l.closeSignal = make(chan struct{})
	l.ready = make(chan struct{})
	l.done = make(chan struct{})
	changed := make(chan struct{})
	l.changed.Store(&changed)
}

// changeSignal returns the channel the next change of status closes.
func (l *sessionLife) changeSignal() <-chan struct{} { return *l.changed.Load() }

// setStatus stores a status and wakes the goroutines waiting in WaitForIdle.
func (l *sessionLife) setStatus(status SessionStatus) {
	l.status.Store(encodeSessionStatus(status))
	next := make(chan struct{})
	close(*l.changed.Swap(&next))
}

// encodeSessionStatus maps a status to the number stored atomically.
func encodeSessionStatus(status SessionStatus) uint32 {
	switch status {
	case SessionStatusCreated:
		return 1
	case SessionStatusIdle:
		return 2
	case SessionStatusBusy:
		return 3
	case SessionStatusClosing:
		return 4
	case SessionStatusClosed:
		return 5
	default:
		return 0
	}
}

// decodeSessionStatus is the inverse of encodeSessionStatus.
func decodeSessionStatus(status uint32) SessionStatus {
	switch status {
	case 1:
		return SessionStatusCreated
	case 2:
		return SessionStatusIdle
	case 3:
		return SessionStatusBusy
	case 4:
		return SessionStatusClosing
	case 5:
		return SessionStatusClosed
	default:
		return SessionStatusCreated
	}
}
