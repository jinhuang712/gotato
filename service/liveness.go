package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Liveness sets how a Runner keeps its Liveness row, and how long it waits
// for a Session that runs on another Instance.
type Liveness struct {
	// TTL is how long the row stays alive after each renewal, by the store's
	// clock. The Runner renews it every TTL/3. Zero means 10 seconds.
	TTL time.Duration
	// Wait bounds how long a Run waits for a Session that is running on
	// another live Instance before it is refused with ErrBusy. Zero means 5
	// seconds.
	Wait time.Duration
}

const (
	defaultLivenessTTL  = 10 * time.Second
	defaultLivenessWait = 5 * time.Second
)

func (l Liveness) withDefaults() Liveness {
	if l.TTL <= 0 {
		l.TTL = defaultLivenessTTL
	}
	if l.Wait <= 0 {
		l.Wait = defaultLivenessWait
	}
	return l
}

// heartbeat keeps one Runner's Liveness row alive. It starts with the
// Runner's first write, so a Runner that only reads never writes a row.
type heartbeat struct {
	mu       sync.Mutex
	started  bool
	draining bool
	closed   bool
	lastSent time.Time
	expired  bool
	// reported says liveness_lapsed was published once already.
	reported bool
	stop     chan struct{}
	done     chan struct{}
}

// errLapsed refuses a write once this Instance cannot show that its row is
// alive: another Instance may hold its Sessions by now.
var errLapsed = lostRow("service: this instance's liveness row may have expired")

// lostRow builds a refusal of a write that carries conflict and wraps
// session.ErrConflict, so the checks for a lost race match it as they match a
// store's conflict.
func lostRow(message string) *gotato.RuntimeError {
	e := gotato.ErrorOf(gotato.CodeConflict, message)
	e.Cause = session.ErrConflict
	return e
}

// lapsed reports whether err is errLapsed itself. errors.Is cannot tell it from
// errClosed or a store's conflict, which all match session.ErrConflict.
func lapsed(err error) bool {
	var runtimeErr *gotato.RuntimeError
	return errors.As(err, &runtimeErr) && runtimeErr == errLapsed
}

// errHolderBusy reports, inside open, a Session running on another live
// Instance.
var errHolderBusy = errors.New("service: the session is running on another instance")

// errClosed refuses a write once Close has removed this Instance's row. It
// is a conflict, as errLapsed is.
var errClosed = lostRow("service: the runner is closed")

// live makes sure this Runner's row is alive before it writes, beating once
// and starting the renewals the first time.
func (r *Runner) live(ctx context.Context) error {
	r.beat.mu.Lock()
	started, closed := r.beat.started, r.beat.closed
	r.beat.mu.Unlock()
	if closed {
		return errClosed
	}
	if started {
		return nil
	}
	if err := r.renew(ctx); err != nil {
		return err
	}
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	if r.beat.closed {
		// Closed while this first renewal was on its way: remove the row it
		// may have written.
		_ = r.store.Beat(ctx, session.InstanceRow{ID: r.instance}, 0)
		return errClosed
	}
	if r.beat.started {
		return nil
	}
	r.beat.started = true
	r.beat.stop = make(chan struct{})
	r.beat.done = make(chan struct{})
	go r.renewals(r.beat.stop, r.beat.done)
	return nil
}

// renew renews the row once. The time it was sent, not answered, is what the
// guard counts from, since the store may have timed it as early as that.
func (r *Runner) renew(ctx context.Context) error {
	r.beat.mu.Lock()
	row := session.InstanceRow{ID: r.instance, Draining: r.beat.draining, Address: r.address}
	r.beat.mu.Unlock()
	sent := time.Now()
	err := r.store.Beat(ctx, row, r.liveness.TTL)
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	switch {
	case errors.Is(err, session.ErrExpired):
		r.beat.expired = true
	case err == nil && sent.After(r.beat.lastSent):
		r.beat.lastSent = sent
	}
	return err
}

func (r *Runner) renewals(stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.liveness.TTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), r.liveness.TTL/3)
			_ = r.renew(ctx)
			cancel()
		}
	}
}

// guard refuses a write once the last renewal is older than the TTL less a
// margin, or the row has expired, so a paused or cut-off Instance stops
// before the store could have let another Instance take its Sessions.
func (r *Runner) guard() error {
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	if r.beat.expired || time.Since(r.beat.lastSent) > r.liveness.TTL-r.liveness.TTL/5 {
		if !r.beat.reported && !r.beat.closed {
			r.beat.reported = true
			r.notice(gotato.LivenessLapsed{Header: gotato.NewHeader("", "", 0), Instance: r.instance}, "")
		}
		return errLapsed
	}
	return nil
}

// markDraining sets the draining mark in this Runner's Liveness row, so every
// Instance can see that it is leaving. A Runner that has not written yet has
// no row, and its renewals carry the mark once they start.
func (r *Runner) markDraining(ctx context.Context) error {
	r.beat.mu.Lock()
	r.beat.draining = true
	started := r.beat.started
	r.beat.mu.Unlock()
	if !started {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.liveness.TTL/3)
	defer cancel()
	if err := r.renew(ctx); err != nil {
		return fmt.Errorf("service: marking the liveness row draining: %w", err)
	}
	return nil
}
