package gotato

import (
	"context"
	"sync"
	"sync/atomic"
)

// sessionEvents is the Session's Events: where they are kept, in what order
// they are sent, and how far they have been numbered. mu is the first of the
// Session's locks in order: events, then run (the span of a Run is opened and
// closed under both), then queue.
type sessionEvents struct {
	// journal keeps the Session's stored Events: the Transcript's Journal when
	// it declares one, and a numbering of its own otherwise.
	journal Journal
	// feed sends them, and the deltas. ownFeed says the Session made it and so
	// closes it.
	feed    *Feed
	ownFeed bool
	// staged holds the stored Events of the step in progress, on the Session
	// goroutine. They are written, and then sent, by flush.
	staged []Event
	// mu makes a write and its sending one step, so Events reach subscribers in
	// the order of their numbers, whichever goroutine writes them.
	mu sync.Mutex
	// last is the number of the last stored Event the Session wrote.
	last atomic.Uint64
}

// nowHeader returns the Header of an Event that another goroutine writes
// (a queued message, an Abort, a drop). It names no Run: writeEvents stamps the
// Run and the Turn when it numbers the Event, so an Event of a Run is always
// between that Run's run_started and run_ended.
func (a *Session) nowHeader() Header {
	return NewHeader(a.def.id, "", 0)
}

// runHeader returns the Header of an Event that the Session goroutine stages
// in the Run in flight.
func (a *Session) runHeader() Header {
	a.run.mu.Lock()
	defer a.run.mu.Unlock()
	return NewHeader(a.def.id, a.run.id, a.run.turn)
}

// stage queues stored Events of the step in progress. They ride the write of
// the step, in flush, and reach subscribers only after it. Only the Session
// goroutine stages.
func (a *Session) stage(events ...Event) {
	a.events.staged = append(a.events.staged, events...)
}

// flush writes the staged Events, and then sends them. A failed write fails
// the Run: nothing that was not stored is sent.
func (a *Session) flush(ctx context.Context) *RuntimeError {
	if len(a.events.staged) == 0 {
		return nil
	}
	events := a.events.staged
	a.events.staged = nil
	return a.write(ctx, events)
}

// flushNumbered is flush that also returns the number of the last staged Event
// of the given kind, 0 when it wrote none.
func (a *Session) flushNumbered(ctx context.Context, kind EventKind) (uint64, *RuntimeError) {
	if len(a.events.staged) == 0 {
		return 0, nil
	}
	events := a.events.staged
	a.events.staged = nil
	written, err := a.writeEvents(ctx, events)
	if err != nil {
		return 0, err
	}
	var number uint64
	for _, event := range written {
		if event.Kind() == kind {
			number = event.Head().Number
		}
	}
	return number, nil
}

// writeNow writes Events of their own, between steps: a queued message, an
// Abort. Their Header names no Run (see nowHeader). It is safe to call from any goroutine.
func (a *Session) writeNow(ctx context.Context, events ...Event) *RuntimeError {
	return a.write(ctx, events)
}

// write keeps events in the Journal and sends every Event it wrote, under one
// lock, so that Events are sent in the order of their numbers.
func (a *Session) write(ctx context.Context, events []Event) *RuntimeError {
	_, err := a.writeEvents(ctx, events)
	return err
}

// writeEvents is write that also returns the Events it wrote, numbered. What
// it sends is without the body of a spilled result.
//
// Under events.mu it keeps the span of the Run: it opens when run_started is
// written and closes when run_ended is. An Event with no RunID is stamped with
// the open Run and its Turn as it is numbered, or left without a Run when none
// is open, so that no Event of a Run falls outside its run_started and
// run_ended, whichever goroutine wrote it.
func (a *Session) writeEvents(ctx context.Context, events []Event) ([]Event, *RuntimeError) {
	a.events.mu.Lock()
	defer a.events.mu.Unlock()
	events = a.openSpan(events)
	written, err := a.writeJournal(ctx, events)
	if err != nil {
		return nil, journalError(err)
	}
	a.closeSpan(events)
	for _, event := range written {
		if number := event.Head().Number; number > a.events.last.Load() {
			a.events.last.Store(number)
		}
		a.events.feed.publish(WithoutBody(event))
	}
	return written, nil
}

// openSpan stamps the Events that name no Run, opens the Run's span when
// events hold its run_started, and returns the Events to write. An Abort that
// came before the span opened is written right after run_started, in the same
// write.
// [CONCURRENCY] caller holds events.mu; the span opens and abortBeforeStart is read in one run.mu step, so an Abort either sees the span open or is written here.
func (a *Session) openSpan(events []Event) []Event {
	a.run.mu.Lock()
	defer a.run.mu.Unlock()
	out := make([]Event, 0, len(events)+1)
	for _, event := range events {
		if event.Head().RunID == "" && a.run.open {
			run, turn := a.run.id, a.run.turn
			event = withHeader(event, func(h *Header) { h.RunID, h.Turn = run, turn })
		}
		out = append(out, event)
		if event.Kind() == EventKindRunStarted {
			a.run.open = true
			if a.run.abortBeforeStart {
				a.run.abortBeforeStart = false
				out = append(out, AbortRequested{Header: NewHeader(a.def.id, a.run.id, 0)})
			}
		}
	}
	return out
}

// closeSpan closes the Run's span once events, which were written, hold its
// run_ended.
// [CONCURRENCY] caller holds events.mu: nothing is numbered inside the Run after its run_ended.
func (a *Session) closeSpan(events []Event) {
	for _, event := range events {
		if event.Kind() == EventKindRunEnded {
			a.run.mu.Lock()
			a.run.open = false
			a.run.mu.Unlock()
			return
		}
	}
}

// send sends a delta at once. It is marked with the number of the stored Event
// it follows, and never stored.
func (a *Session) send(event Event) {
	a.events.feed.publish(event)
}

// deltaHeader is the Header of a delta: the Number is that of the last stored
// Event, which the delta follows.
func (a *Session) deltaHeader(runID RunID, turn TurnNumber) Header {
	header := NewHeader(a.def.id, runID, turn)
	header.Number = a.events.last.Load()
	return header
}

// reportExtension stores an ExtensionFailed Event for an advisory extension
// that failed during a Run, on the Session goroutine.
func (a *Session) reportExtension(stage string, failure *RuntimeError) {
	if !a.run.inRun.Load() {
		return
	}
	a.stage(ExtensionFailed{Header: a.runHeader(), Stage: stage, Code: failure.Code})
}

// writeJournal writes events to the Journal; a panic in it is an error.
func (a *Session) writeJournal(ctx context.Context, events []Event) (written []Event, err error) {
	defer recoverPanic("Journal", &err)
	return a.events.journal.Write(ctx, events)
}
