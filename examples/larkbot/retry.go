package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
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

// enter returns the channel to wait on before sending (nil: go at once) and the
// function that lets the next message go on. The gate is entered only by a
// message that is retrying, or behind one that is.
func (b *bot) enter(session string, force bool) (wait <-chan struct{}, leave func(), entered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.gates[session]
	if g == nil {
		if !force {
			return nil, func() {}, false
		}
		g = &gate{}
		b.gates[session] = g
	}
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
