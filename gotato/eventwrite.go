package gotato

import (
	"context"
)

// nowHeader returns the Header of an Event that another goroutine writes
// (a queued message, an Abort, a drop). It names no Run: writeEvents stamps the
// Run and the Turn when it numbers the Event, so an Event of a Run is always
// between that Run's run_started and run_ended.
func (a *Session) nowHeader() Header {
	return NewHeader(a.id, "", 0)
}

// runHeader returns the Header of an Event that the Session goroutine stages
// in the Run in flight.
func (a *Session) runHeader() Header {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	return NewHeader(a.id, a.currentRunID, a.currentTurn)
}

// stage queues stored Events of the step in progress. They ride the write of
// the step, in flush, and reach subscribers only after it. Only the Session
// goroutine stages.
func (a *Session) stage(events ...Event) {
	a.staged = append(a.staged, events...)
}

// flush writes the staged Events, and then sends them. A failed write fails
// the Run: nothing that was not stored is sent.
func (a *Session) flush(ctx context.Context) *RuntimeError {
	if len(a.staged) == 0 {
		return nil
	}
	events := a.staged
	a.staged = nil
	return a.write(ctx, events)
}

// flushNumbered is flush that also returns the number of the last staged Event
// of the given kind, 0 when it wrote none.
func (a *Session) flushNumbered(ctx context.Context, kind EventKind) (uint64, *RuntimeError) {
	if len(a.staged) == 0 {
		return 0, nil
	}
	events := a.staged
	a.staged = nil
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
// Under emitMu it keeps the span of the Run: it opens when run_started is
// written and closes when run_ended is. An Event with no RunID is stamped with
// the open Run and its Turn as it is numbered, or left without a Run when none
// is open, so that no Event of a Run falls outside its run_started and
// run_ended, whichever goroutine wrote it.
func (a *Session) writeEvents(ctx context.Context, events []Event) ([]Event, *RuntimeError) {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	events = a.openSpan(events)
	written, err := a.writeJournal(ctx, events)
	if err != nil {
		return nil, journalError(err)
	}
	a.closeSpan(events)
	for _, event := range written {
		if number := event.Head().Number; number > a.lastNumber.Load() {
			a.lastNumber.Store(number)
		}
		a.feed.Publish(WithoutBody(event))
	}
	return written, nil
}

// openSpan stamps the Events that name no Run, opens the Run's span when
// events hold its run_started, and returns the Events to write. An Abort that
// came before the span opened is written right after run_started, in the same
// write.
// [CONCURRENCY] caller holds emitMu; the span opens and abortBeforeStart is read in one runMu step, so an Abort either sees the span open or is written here.
func (a *Session) openSpan(events []Event) []Event {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	out := make([]Event, 0, len(events)+1)
	for _, event := range events {
		if event.Head().RunID == "" && a.runOpen {
			run, turn := a.currentRunID, a.currentTurn
			event = withHeader(event, func(h *Header) { h.RunID, h.Turn = run, turn })
		}
		out = append(out, event)
		if event.Kind() == EventKindRunStarted {
			a.runOpen = true
			if a.abortBeforeStart {
				a.abortBeforeStart = false
				out = append(out, AbortRequested{Header: NewHeader(a.id, a.currentRunID, 0)})
			}
		}
	}
	return out
}

// closeSpan closes the Run's span once events, which were written, hold its
// run_ended.
// [CONCURRENCY] caller holds emitMu: nothing is numbered inside the Run after its run_ended.
func (a *Session) closeSpan(events []Event) {
	for _, event := range events {
		if event.Kind() == EventKindRunEnded {
			a.runMu.Lock()
			a.runOpen = false
			a.runMu.Unlock()
			return
		}
	}
}

// send sends a delta at once. It is marked with the number of the stored Event
// it follows, and never stored.
func (a *Session) send(event Event) {
	a.feed.Publish(event)
}

// deltaHeader is the Header of a delta: the Number is that of the last stored
// Event, which the delta follows.
func (a *Session) deltaHeader(runID RunID, turn TurnNumber) Header {
	header := NewHeader(a.id, runID, turn)
	header.Number = a.lastNumber.Load()
	return header
}

// reportExtension stores an ExtensionFailed Event for an advisory extension
// that failed during a Run, on the Session goroutine.
func (a *Session) reportExtension(stage string, failure *RuntimeError) {
	if !a.inRun.Load() {
		return
	}
	a.stage(ExtensionFailed{Header: a.runHeader(), Stage: stage, Code: failure.Code})
}

// writeJournal writes events to the Journal; a panic in it is an error.
func (a *Session) writeJournal(ctx context.Context, events []Event) (written []Event, err error) {
	defer recoverPanic("Journal", &err)
	return a.journal.Write(ctx, events)
}
