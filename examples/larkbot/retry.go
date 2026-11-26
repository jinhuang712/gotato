package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The retry rule: a message is sent again only when it is known not to have
// run. Gotato marks those answers NotRun (not_writer, busy, draining, capacity),
// and a forward that never connected delivered nothing. Anything else, such as a
// connection that was made and then lost, may have been accepted: it is not retried.
const (
	defaultRetryBudget = 20 * time.Second
	retryFirstWait     = 250 * time.Millisecond
	retryMaxWait       = 2 * time.Second
	// acceptGrace is how long an attempt may be in flight before the next message of
	// the Session may go on: a refusal comes back at once, so a Send that is still
	// going has been accepted (it may be a whole Run).
	acceptGrace = 500 * time.Millisecond
)

// retryPolicy is the backoff of one message.
type retryPolicy struct {
	budget, first, max time.Duration // budget 0 turns retrying off
}

func defaultRetry(budget time.Duration) retryPolicy {
	return retryPolicy{budget: budget, first: retryFirstWait, max: retryMaxWait}
}

// deliveryError is a failed attempt: notRun says nothing was delivered.
type deliveryError struct {
	err    error
	notRun bool
}

func (e *deliveryError) Error() string { return e.err.Error() }
func (e *deliveryError) Unwrap() error { return e.err }

// notRun reports whether err is a definite "not run" failure.
func notRun(err error) bool {
	if de, ok := errors.AsType[*deliveryError](err); ok {
		return de.notRun
	}
	return gotato.NotRun(err)
}

// notConnected reports whether err is a dial failure: nothing reached the peer.
func notConnected(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// gate keeps the messages of one Session in arrival order while one of them retries.
type gate struct {
	tail <-chan struct{} // closed when the newest waiting message may go on
	n    int
}

// join returns the channel to wait on before sending (nil: go at once) and the
// function that lets the next message go on. It enters the gate only when the
// Session has one: a message behind one that retries.
func (b *bot) join(session string) (wait <-chan struct{}, leave func(), entered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.gates[session]
	if g == nil {
		return nil, func() {}, false
	}
	return b.enter(g, session)
}

// open is join for a message that is retrying: it makes the gate when the
// Session has none.
func (b *bot) open(session string) (wait <-chan struct{}, leave func(), entered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.gates[session]
	if g == nil {
		g = &gate{}
		b.gates[session] = g
	}
	return b.enter(g, session)
}

// enter puts a message at the tail of the gate. The caller holds b.mu.
func (b *bot) enter(g *gate, session string) (wait <-chan struct{}, leave func(), entered bool) {
	done := make(chan struct{})
	wait, g.tail = g.tail, done
	g.n++
	var once sync.Once
	return wait, func() {
		once.Do(func() {
			close(done)
			b.mu.Lock()
			defer b.mu.Unlock()
			if g.n--; g.n == 0 {
				delete(b.gates, session)
			}
		})
	}, true
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// delivery is the state of one deliver call across its attempts.
type delivery struct {
	b                           *bot
	chatID, id, messageID, text string
	start                       time.Time
	backoff                     time.Duration
	leave                       func() // lets the next message of the Session go on
	gated                       bool   // this message is in the Session's gate
}

// elapsed is the time since deliver began, to the millisecond.
func (d *delivery) elapsed() time.Duration {
	return time.Since(d.start).Round(time.Millisecond)
}

// again makes one attempt and reports whether deliver should go on with the
// next one.
func (d *delivery) again(ctx context.Context, attempt int) bool {
	err := d.attempt(ctx)
	switch {
	case err == nil:
		d.logDelivered(attempt)
		return false
	case !notRun(err):
		d.logFailed(ctx, attempt, err)
		return false
	case d.b.retry.budget <= 0 || time.Since(d.start)+d.backoff > d.b.retry.budget || ctx.Err() != nil:
		d.b.log.Warn("delivery failed, not run", "chat", d.chatID, "instance", d.b.label, "session", d.id, "message_id", d.messageID,
			"attempts", attempt, "elapsed", d.elapsed(), "err", err)
		d.b.notify(ctx, d.id, d.chatID, d.messageID, busyNotice)
		return false
	}
	return d.pause(ctx, attempt, err)
}

// logDelivered logs a message that was accepted after more than one attempt.
func (d *delivery) logDelivered(attempt int) {
	if attempt > 1 {
		d.b.log.Info("delivered after retry", "session", d.id, "message_id", d.messageID, "attempts", attempt,
			"elapsed", d.elapsed(), "instance", d.b.label)
	}
}

// logFailed logs an attempt that may have run, and tells the user when it was a forward.
func (d *delivery) logFailed(ctx context.Context, attempt int, err error) {
	if _, ok := errors.AsType[*deliveryError](err); ok {
		d.b.log.Warn("delivery unknown", "chat", d.chatID, "instance", d.b.label, "session", d.id, "message_id", d.messageID, "attempts", attempt, "err", err)
		d.b.notify(ctx, d.id, d.chatID, d.messageID, busyNotice)
	} else {
		d.b.log.Error("send", "session", d.id, "message_id", d.messageID, "err", err)
	}
}

// pause waits out the backoff before the next attempt, and reports false when
// ctx ended first.
func (d *delivery) pause(ctx context.Context, attempt int, err error) bool {
	if !d.gated {
		// From here on, a later message of this Session waits for this one.
		var w <-chan struct{}
		w, d.leave, d.gated = d.b.open(d.id)
		if !d.b.await(ctx, w) {
			return false
		}
	}
	d.b.log.Info("not run, will retry", "instance", d.b.label, "session", d.id, "message_id", d.messageID, "attempt", attempt, "in", d.backoff, "err", err)
	if !sleep(ctx, d.backoff) {
		return false
	}
	if d.backoff *= 2; d.backoff > d.b.retry.max {
		d.backoff = d.b.retry.max
	}
	return true
}
