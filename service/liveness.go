package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
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
	closed   bool
	lastSent time.Time
	expired  bool
	stop     chan struct{}
	done     chan struct{}
}

// errLapsed refuses a write once this Instance cannot show that its row is
// alive: another Instance may hold its Sessions by now.
var errLapsed = gotato.ErrorOf(gotato.ErrConflict, "service: this instance's liveness row may have expired")

// errHolderBusy reports, inside open, a Session running on another live
// Instance.
var errHolderBusy = errors.New("service: the session is running on another instance")

// errClosed refuses a write once Close has removed this Instance's row.
var errClosed = gotato.ErrorOf(gotato.ErrInvalidState, "service: the runner is closed")

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
	sent := time.Now()
	err := r.store.Beat(ctx, session.InstanceRow{ID: r.instance}, r.liveness.TTL)
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
		return errLapsed
	}
	return nil
}

// holderCheck lets this Runner take a Session over from its holder unless the
// holder is alive, not draining, and using it.
func (r *Runner) holderCheck(ctx context.Context, holder session.Claim, busy bool) error {
	if !busy || holder.Instance == "" {
		return nil
	}
	state, err := r.store.Instance(ctx, holder.Instance)
	if err != nil {
		return err
	}
	if state.Alive && !state.Draining {
		return errHolderBusy
	}
	return nil
}

// create writes a new Session's Record as this Runner's Instance.
func (r *Runner) create(ctx context.Context, s *session.Record) (*session.Writer, error) {
	if err := r.live(ctx); err != nil {
		return nil, err
	}
	writer, err := session.Create(ctx, r.store, s, r.instance, session.WithGuard(r.guard))
	if errors.Is(err, session.ErrExists) {
		return nil, ErrSessionExists
	}
	return writer, err
}

// open opens a stored Session for writing as this Runner's Instance. When the
// Session runs on another live Instance, it waits for that Run to end, up to
// Liveness.Wait, and then refuses with a busy error that says when to retry.
func (r *Runner) open(ctx context.Context, sessionID string) (*session.Writer, error) {
	if err := r.live(ctx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(r.liveness.Wait)
	pause := 20 * time.Millisecond
	for {
		writer, err := session.OpenWriter(ctx, r.store, sessionID, r.instance, session.WithGuard(r.guard), session.WithHolderCheck(r.holderCheck))
		// A conflict here means another Instance claimed or wrote first:
		// look again, as for a busy holder.
		if err == nil || (!errors.Is(err, errHolderBusy) && !errors.Is(err, session.ErrConflict)) || errors.Is(err, errLapsed) {
			return writer, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, busyElsewhere(r.liveness.Wait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(pause, remaining)):
		}
		pause = min(pause*2, 500*time.Millisecond)
	}
}

// busyElsewhere refuses a Run on a Session that is running on another
// Instance. It matches ErrBusy, and says when to retry.
func busyElsewhere(retryAfter time.Duration) error {
	return &gotato.RuntimeError{
		Code:    gotato.ErrBusy,
		Message: "service: the session is running on another instance",
		Details: map[string]string{"retry_after_ms": strconv.FormatInt(retryAfter.Milliseconds(), 10)},
	}
}

// Close removes this Runner's Liveness row, so other Instances take its
// Sessions over at once instead of waiting for the row to expire, and the
// store keeps nothing of this Instance. Call it after Drain. A closed Runner
// writes nothing more, so its row never comes back. Closing twice is a no-op.
func (r *Runner) Close(ctx context.Context) error {
	r.beat.mu.Lock()
	started := r.beat.started
	stop, done := r.beat.stop, r.beat.done
	r.beat.started = false
	r.beat.closed = true
	r.beat.expired = true
	r.beat.mu.Unlock()
	if !started {
		return nil
	}
	close(stop)
	<-done
	err := r.store.Beat(ctx, session.InstanceRow{ID: r.instance}, 0)
	if errors.Is(err, session.ErrExpired) {
		// A store that keeps an ended row refuses an expired one.
		return nil
	}
	return err
}
