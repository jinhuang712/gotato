package gotato

import (
	"context"
)

// nowHeader returns the Header of an Event that happens now, in the Run in
// flight if there is one. It is safe to call from any goroutine.
func (a *Session) nowHeader() Header {
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
// Abort. It is safe to call from any goroutine.
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
func (a *Session) writeEvents(ctx context.Context, events []Event) ([]Event, *RuntimeError) {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()
	written, err := a.writeJournal(ctx, events)
	if err != nil {
		return nil, journalError(err)
	}
	for _, event := range written {
		if number := event.Head().Number; number > a.lastNumber.Load() {
			a.lastNumber.Store(number)
		}
		a.feed.Publish(WithoutBody(event))
	}
	return written, nil
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
	a.stage(ExtensionFailed{Header: a.nowHeader(), Stage: stage, Code: failure.Code})
}

// writeJournal writes events to the Journal; a panic in it is an error.
func (a *Session) writeJournal(ctx context.Context, events []Event) (written []Event, err error) {
	defer recoverPanic("Journal", &err)
	return a.journal.Write(ctx, events)
}
