package gotato

import (
	"context"
	"time"
)

// forwarder is the state of a subscription's forward goroutine: the catch-up
// batch it hands out first, and the count of catch-up reads in a row that
// found nothing the Feed had published.
type forwarder struct {
	*subscription
	pending []Event
	empty   int
}

// take returns the next Event: from the pending catch-up batch, the queue, or
// the source. ok is false when the stream has ended.
func (w *forwarder) take(ctx context.Context) (Event, bool, error) {
	for {
		if len(w.pending) > 0 {
			event := w.pending[0]
			w.pending = w.pending[1:]
			return event, true, nil
		}
		event, ok, state := w.popQueued()
		if ok {
			return event, true, nil
		}
		if ended, err := w.advance(ctx, state); ended || err != nil {
			return nil, false, err
		}
	}
}

// advance does what state says when nothing is queued: it catches up from
// the source, ends the stream, or waits. ended is true when the stream ended
// without an error.
func (w *forwarder) advance(ctx context.Context, state queueState) (ended bool, err error) {
	switch {
	case state.behind:
		return w.catchUp(ctx, state.resume)
	case state.ending:
		return true, nil
	default:
		return w.await(ctx)
	}
}

// catchUp reads the next batch of stored Events after resume from the source
// into pending, or rejoins the live Events once it has read them all. ended
// is true when the stream ended without an error.
func (w *forwarder) catchUp(ctx context.Context, resume uint64) (ended bool, err error) {
	batch, kept, err := w.read(ctx, resume)
	if err != nil {
		return false, err
	}
	if len(batch) > 0 {
		w.mu.Lock()
		w.resume = batch[len(batch)-1].Head().Number
		w.cursor = max(w.cursor, w.resume)
		w.mu.Unlock()
		for i, event := range batch {
			if kept[i] {
				w.pending = append(w.pending, event)
			}
		}
		return false, nil
	}
	// Nothing more to read: rejoin the live Events once the Feed
	// has published nothing newer than what was read.
	w.feed.mu.Lock()
	w.mu.Lock()
	if w.resume >= w.feed.last {
		w.behind, w.cursorSet = false, true
		w.empty = 0
	}
	caughtUp := !w.behind
	w.mu.Unlock()
	w.feed.mu.Unlock()
	if caughtUp {
		return false, nil
	}
	// The source holds nothing the Feed has published since: give
	// a store that is slow to show its last write a moment, and then
	// give up, because the Events are not coming.
	if w.empty++; w.empty > catchUpRetries {
		return false, ErrEventsLost
	}
	select {
	case <-time.After(catchUpPause):
		return false, nil
	case <-w.done:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// await blocks until the subscriber has something to look at: a wake-up from
// the Feed, or the tail interval, after which it reads the source. ended is
// true when the stream ended without an error.
func (w *forwarder) await(ctx context.Context) (ended bool, err error) {
	var tick <-chan time.Time
	if w.tail > 0 && !w.feed.mixed && w.feed.hasSource() {
		timer := time.NewTimer(w.tail)
		defer timer.Stop()
		tick = timer.C
	}
	select {
	case <-w.wake:
		return false, nil
	case <-tick:
		// Nothing came for a while: look in the source, which holds what
		// another Instance wrote. The stream stays live while it reads, so
		// a delta published meanwhile is not lost.
		return false, w.look(ctx)
	case <-w.done:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// look reads the stored Events after the last one this subscriber has from the
// Feed's source, and queues those that are still the next ones once the read
// is done: a live Event that arrived meanwhile has moved the cursor, and what
// it covers is not queued twice.
func (w *forwarder) look(ctx context.Context) error {
	w.mu.Lock()
	if !w.cursorSet || w.behind {
		w.mu.Unlock()
		return nil
	}
	after := w.cursor
	w.mu.Unlock()
	batch, kept, err := w.read(ctx, after)
	if err != nil {
		return err
	}
	// An Event that a live publish queued during the read is skipped below by
	// its number.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.behind {
		return nil
	}
	for i, event := range batch {
		number := event.Head().Number
		if number <= w.cursor {
			continue
		}
		if number != w.cursor+1 {
			// A gap: the catch-up reads from the cursor on.
			w.behind, w.resume = true, w.cursor
			return nil
		}
		w.cursor = number
		if kept[i] {
			w.queue = append(w.queue, event)
		}
	}
	return nil
}

// read is the one read of the Feed's source, which a catch-up and the tail
// share: the next batch of stored Events after number after, checked with
// SubscribeVerify when it holds any, and kept[i] says batch[i] passes the
// filter.
// [CONCURRENCY] no lock of the subscription is held, so the filter and the check may call End or Close.
func (w *forwarder) read(ctx context.Context, after uint64) (batch []Event, kept []bool, err error) {
	batch, err = w.feed.eventsAfter(ctx, after, catchUpBatch)
	if err != nil || len(batch) == 0 {
		return nil, nil, err
	}
	if err := w.check(ctx); err != nil {
		return nil, nil, err
	}
	kept = make([]bool, len(batch))
	for i, event := range batch {
		if kept[i], err = w.passes(event); err != nil {
			return nil, nil, err
		}
	}
	return batch, kept, nil
}

// check runs the SubscribeVerify function, if any, after a read that returned
// Events. A panic in it ends this Subscription.
// [LIVENESS] a panic in the host's verify function ends this one Subscription, not the process.
func (w *forwarder) check(ctx context.Context) error {
	if w.verify == nil {
		return nil
	}
	var failure error
	if err := callHost("SubscribeVerify", func() error { failure = w.verify(ctx); return nil }); err != nil {
		return extensionFailure("Subscription", err)
	}
	return failure
}
