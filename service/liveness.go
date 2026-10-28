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
	// instance is the name this Runner writes with. It changes when the row
	// expired and the Runner registered a new one (see renew).
	instance string
	// reported says liveness_lapsed was published once already.
	reported bool
	stop     chan struct{}
	done     chan struct{}
}

// errLapsed refuses a write once this Instance cannot show that its row is
// alive: another Instance may hold its Sessions by now.
var errLapsed = lostRow("service: this instance's liveness row may have expired")

// errLapsedNotRun is errLapsed as open and create return it: the guard refused
// the Claim, the first write, so nothing was written and nothing ran.
var errLapsedNotRun = func() *gotato.RuntimeError {
	e := lostRow(errLapsed.Message)
	e.NotRun = true
	return e
}()

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
	return errors.As(err, &runtimeErr) && (runtimeErr == errLapsed || runtimeErr == errLapsedNotRun)
}

// notRunIfLapsed marks a refusal of the Claim by the guard as not run: it is the
// first write, so nothing was written. Any other error is returned as it is.
func notRunIfLapsed(err error) error {
	if lapsed(err) {
		return errLapsedNotRun
	}
	return err
}

// id returns the name this Runner writes with now.
func (r *Runner) id() string {
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	return r.beat.instance
}

// newName makes a name for an Instance: the label and a random suffix, or a
// whole generated name when there is no label.
func (r *Runner) newName() string {
	if r.label == "" {
		return session.NewID()
	}
	return r.label + "-" + session.NewID()[:8]
}

// Lapsed reports whether this Runner cannot show now that its Liveness row is
// alive: the last renewal is too old, or the row expired and no new one has
// been registered yet. It is false for a Runner that has not written. A Runner
// recovers by itself when the store answers again (see renew); until then it
// refuses writes, so a readiness probe should report it as not ready.
func (r *Runner) Lapsed() bool {
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	if !r.beat.started || r.beat.closed {
		return false
	}
	return r.beat.expired || time.Since(r.beat.lastSent) > r.liveness.TTL-r.liveness.TTL/5
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
		return storeFailureOf(ctx, err, true)
	}
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	if r.beat.closed {
		// Closed while this first renewal was on its way: remove the row it
		// may have written.
		_ = r.store.Beat(ctx, session.InstanceRow{ID: r.beat.instance}, 0)
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
//
// A row that expired never comes back (session.ErrExpired), and the Sessions
// it held may belong to other Instances by now. A store that was out of reach
// longer than the TTL therefore ends this name, and renew registers a new one
// instead of leaving the Runner without writes for good: the Runs and warm
// copies of the old name are refused or dropped, and the Claims it left are
// dead, so any Instance may take those Sessions over. Until the new row is
// written, the Runner is Lapsed.
func (r *Runner) renew(ctx context.Context) error {
	r.beat.mu.Lock()
	row := session.InstanceRow{ID: r.beat.instance, Draining: r.beat.draining, Address: r.address}
	r.beat.mu.Unlock()
	sent := time.Now()
	err := r.store.Beat(ctx, row, r.liveness.TTL)
	r.beat.mu.Lock()
	switch {
	case errors.Is(err, session.ErrExpired):
		r.beat.expired = true
	case err == nil && sent.After(r.beat.lastSent):
		r.beat.lastSent = sent
	}
	closed := r.beat.closed
	r.beat.mu.Unlock()
	// [OWNERSHIP] an expired Liveness row is written again, or this Instance's Claims would be taken over.
	if errors.Is(err, session.ErrExpired) && !closed {
		return r.reregister(ctx, row)
	}
	return err
}

// reregister gives the Runner a new name after its row expired, and writes
// that name's row. The old name's expired row is removed, best effort.
func (r *Runner) reregister(ctx context.Context, old session.InstanceRow) error {
	row := old
	row.ID = r.newName()
	sent := time.Now()
	if err := r.store.Beat(ctx, row, r.liveness.TTL); err != nil {
		return err
	}
	r.beat.mu.Lock()
	if r.beat.closed || r.beat.instance != old.ID {
		// Closed meanwhile, or another renewal registered a name first: the
		// row just written belongs to no one.
		r.beat.mu.Unlock()
		_ = r.store.Beat(ctx, session.InstanceRow{ID: row.ID}, 0)
		return nil
	}
	r.beat.instance = row.ID
	r.beat.expired = false
	r.beat.reported = false
	r.beat.lastSent = sent
	r.beat.mu.Unlock()
	_ = r.store.Beat(ctx, session.InstanceRow{ID: old.ID}, 0)
	// A warm copy holds no Claim, but resumes with the old name.
	r.dropAllWarm("liveness_lapsed")
	return nil
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
			r.repairReleases()
		}
	}
}

// guard refuses a write once the last renewal is older than the TTL less a
// margin, or the row has expired, so a paused or cut-off Instance stops
// before the store could have let another Instance take its Sessions.
// guardFor is guard for the Writer of one name: once the Runner has a new name,
// the Writers of the old one are refused for good, as their Claims are dead.
func (r *Runner) guardFor(name string) error {
	r.beat.mu.Lock()
	defer r.beat.mu.Unlock()
	if name != "" && name != r.beat.instance {
		return errLapsed
	}
	if r.beat.expired || time.Since(r.beat.lastSent) > r.liveness.TTL-r.liveness.TTL/5 {
		if !r.beat.reported && !r.beat.closed {
			r.beat.reported = true
			r.notice(gotato.LivenessLapsed{Header: gotato.NewHeader("", "", 0), Instance: r.beat.instance}, "")
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

// Close removes this Runner's Liveness row, so other Instances take its
// Sessions over at once instead of waiting for the row to expire, and the
// store keeps nothing of this Instance. Call it after Drain. A closed Runner
// writes nothing more, so its row never comes back. Closing twice is a no-op.
func (r *Runner) Close(ctx context.Context) error {
	// The Instance's stream ends: its subscribers read what is queued, and
	// then io.EOF.
	defer r.instanceFeed.Close()
	// Warm Sessions hold no Claim, so letting them go writes nothing.
	r.warmMu.Lock()
	r.warmClosed = true
	r.warmMu.Unlock()
	r.dropAllWarm("closed")
	r.beat.mu.Lock()
	started := r.beat.started
	stop, done := r.beat.stop, r.beat.done
	r.beat.started = false
	r.beat.closed = true
	r.beat.expired = true
	name := r.beat.instance
	r.beat.mu.Unlock()
	if !started {
		return nil
	}
	close(stop)
	<-done
	err := r.store.Beat(ctx, session.InstanceRow{ID: name}, 0)
	if errors.Is(err, session.ErrExpired) {
		// A store that keeps an ended row refuses an expired one.
		return nil
	}
	return err
}
