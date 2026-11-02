// Package heartbeat keeps one Instance's Liveness row alive, and is the guard
// every write of the Instance asks (decisions 019 and 027).
//
// A Row holds the Instance's name, the row it writes, the guard that refuses a
// write once the row cannot be shown to be alive, and the drain mark. It
// starts with the Instance's first write (Live), so an Instance that only
// reads never writes a row. It knows no Session: what the Instance does when
// the row lapses, when it takes a new name, and on each renewal is given as
// calls (Config).
package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service/internal/clock"
	"github.com/jinhuang712/gotato/store"
)

// ErrLapsed refuses a write once this Instance cannot show that its row is
// alive: another Instance may hold its Sessions by now.
var ErrLapsed = lostRow("service: this instance's liveness row may have expired")

// ErrLapsedNotRun is ErrLapsed as open and create return it: the guard refused
// the Claim, the first write, so nothing was written and nothing ran.
var ErrLapsedNotRun = func() *gotato.RuntimeError {
	e := lostRow(ErrLapsed.Message)
	e.NotRun = true
	return e
}()

// ErrClosed refuses a write once Close has removed this Instance's row. It is
// a conflict, as ErrLapsed is.
var ErrClosed = lostRow("service: the runner is closed")

// lostRow builds a refusal of a write that carries conflict and wraps
// store.ErrConflict, so the checks for a lost race match it as they match a
// store's conflict.
func lostRow(message string) *gotato.RuntimeError {
	e := gotato.ErrorOf(gotato.CodeConflict, message)
	e.Cause = store.ErrConflict
	return e
}

// Lapsed reports whether err is ErrLapsed itself. errors.Is cannot tell it
// from ErrClosed or a store's conflict, which all match store.ErrConflict.
func Lapsed(err error) bool {
	var runtimeErr *gotato.RuntimeError
	return errors.As(err, &runtimeErr) && (runtimeErr == ErrLapsed || runtimeErr == ErrLapsedNotRun)
}

// NotRunIfLapsed marks a refusal of the Claim by the guard as not run: it is
// the first write, so nothing was written. Any other error is returned as it
// is.
func NotRunIfLapsed(err error) error {
	if Lapsed(err) {
		return ErrLapsedNotRun
	}
	return err
}

// Config says what a Row writes and what it tells its Instance.
type Config struct {
	Store store.Liveness
	Clock clock.Clock
	// Label is the human-readable part of the Instance's name: the name is the
	// label and a random suffix, or a generated one when the label is empty.
	Label string
	// Address is where a caller can reach the Instance; the row carries it.
	Address string
	// TTL is how long the row stays alive after each renewal, by the store's
	// clock. The Row renews it every TTL/3.
	TTL time.Duration
	// OnLapse is called once for each name, when the guard first refuses a
	// write because the row lapsed.
	OnLapse func(instance string)
	// OnRename is called after the row expired and the Row took a new name:
	// whatever the Instance kept under the old name is of no use.
	OnRename func()
	// OnBeat is called after each periodic renewal.
	OnBeat func()
}

// Row is one Instance's Liveness row.
type Row struct {
	cfg Config

	mu       sync.Mutex
	started  bool
	draining bool
	closed   bool
	lastSent time.Time
	expired  bool
	// instance is the name this Instance writes with. It changes when the row
	// expired and the Instance registered a new one (see renew).
	instance string
	// reported says OnLapse was called for this name already.
	reported bool
	// ticker renews the row; nil until the first write.
	ticker *clock.Ticker
}

// New returns a Row with a new name. It writes nothing until Live.
func New(cfg Config) *Row {
	r := &Row{cfg: cfg}
	// The label is never the identity: the suffix makes the name unique per
	// start, so a restarted or overlapping process cannot share a row or a
	// Claim with the one it replaces.
	r.instance = r.newName()
	return r
}

// Name returns the name this Instance writes with now.
func (r *Row) Name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instance
}

// newName makes a name for an Instance: the label and a random suffix, or a
// whole generated name when there is no label.
func (r *Row) newName() string {
	if r.cfg.Label == "" {
		return record.NewID()
	}
	return r.cfg.Label + "-" + record.NewID()[:8]
}

// stale reports whether the last renewal is older than the TTL less a margin.
func (r *Row) stale() bool {
	return clock.Since(r.cfg.Clock, r.lastSent) > r.cfg.TTL-r.cfg.TTL/5
}

// Lapsed reports whether the Instance cannot show now that its row is alive:
// the last renewal is too old, or the row expired and no new one has been
// registered yet. It is false for an Instance that has not written. The Row
// recovers by itself when the store answers again (see renew); until then it
// refuses writes.
func (r *Row) Lapsed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.closed {
		return false
	}
	return r.expired || r.stale()
}

// Live makes sure the row is alive before the Instance writes, beating once
// and starting the renewals the first time. It returns ErrClosed after Close,
// and a store's error as it is.
func (r *Row) Live(ctx context.Context) error {
	r.mu.Lock()
	started, closed := r.started, r.closed
	r.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if started {
		return nil
	}
	if err := r.renew(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		// Closed while this first renewal was on its way: remove the row it
		// may have written.
		_ = r.cfg.Store.Beat(ctx, store.InstanceRow{ID: r.instance}, 0)
		return ErrClosed
	}
	if r.started {
		return nil
	}
	r.started = true
	r.ticker = clock.Every(r.cfg.Clock, r.cfg.TTL/3, r.tick)
	return nil
}

// tick is one beat of the renewals: the row, then whatever the Instance does
// each beat.
func (r *Row) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.TTL/3)
	_ = r.renew(ctx)
	cancel()
	if r.cfg.OnBeat != nil {
		r.cfg.OnBeat()
	}
}

// renew renews the row once. The time it was sent, not answered, is what the
// guard counts from, since the store may have timed it as early as that.
//
// A row that expired never comes back (store.ErrExpired), and the Sessions
// it held may belong to other Instances by now. A store that was out of reach
// longer than the TTL therefore ends this name, and renew registers a new one
// instead of leaving the Instance without writes for good: the Runs and warm
// copies of the old name are refused or dropped, and the Claims it left are
// dead, so any Instance may take those Sessions over. Until the new row is
// written, the Row is Lapsed.
func (r *Row) renew(ctx context.Context) error {
	r.mu.Lock()
	row := store.InstanceRow{ID: r.instance, Draining: r.draining, Address: r.cfg.Address}
	r.mu.Unlock()
	sent := r.cfg.Clock.Now()
	err := r.cfg.Store.Beat(ctx, row, r.cfg.TTL)
	r.mu.Lock()
	switch {
	case errors.Is(err, store.ErrExpired):
		r.expired = true
	case err == nil && sent.After(r.lastSent):
		r.lastSent = sent
	}
	closed := r.closed
	r.mu.Unlock()
	// [OWNERSHIP] an expired Liveness row is written again, or this Instance's Claims would be taken over.
	if errors.Is(err, store.ErrExpired) && !closed {
		return r.reregister(ctx, row)
	}
	return err
}

// reregister gives the Instance a new name after its row expired, and writes
// that name's row. The old name's expired row is removed, best effort.
func (r *Row) reregister(ctx context.Context, old store.InstanceRow) error {
	row := old
	row.ID = r.newName()
	sent := r.cfg.Clock.Now()
	if err := r.cfg.Store.Beat(ctx, row, r.cfg.TTL); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed || r.instance != old.ID {
		// Closed meanwhile, or another renewal registered a name first: the
		// row just written belongs to no one.
		r.mu.Unlock()
		_ = r.cfg.Store.Beat(ctx, store.InstanceRow{ID: row.ID}, 0)
		return nil
	}
	r.instance = row.ID
	r.expired = false
	r.reported = false
	r.lastSent = sent
	r.mu.Unlock()
	_ = r.cfg.Store.Beat(ctx, store.InstanceRow{ID: old.ID}, 0)
	// A warm copy holds no Claim, but resumes with the old name.
	if r.cfg.OnRename != nil {
		r.cfg.OnRename()
	}
	return nil
}

// Guard refuses a write once the last renewal is older than the TTL less a
// margin, or the row has expired, so a paused or cut-off Instance stops before
// the store could have let another Instance take its Sessions. It is Guard for
// the Writer of one name: once the Row has a new name, the Writers of the old
// one are refused for good, as their Claims are dead. An empty name is the
// Row's own.
func (r *Row) Guard(name string) error {
	r.mu.Lock()
	if name != "" && name != r.instance {
		r.mu.Unlock()
		return ErrLapsed
	}
	if !r.expired && !r.stale() {
		r.mu.Unlock()
		return nil
	}
	report := !r.reported && !r.closed
	if report {
		r.reported = true
	}
	instance := r.instance
	r.mu.Unlock()
	if report && r.cfg.OnLapse != nil {
		r.cfg.OnLapse(instance)
	}
	return ErrLapsed
}

// MarkDraining sets the draining mark in the Instance's row, so every Instance
// can see that it is leaving. An Instance that has not written yet has no row,
// and its renewals carry the mark once they start.
func (r *Row) MarkDraining(ctx context.Context) error {
	r.mu.Lock()
	r.draining = true
	started := r.started
	r.mu.Unlock()
	if !started {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.TTL/3)
	defer cancel()
	if err := r.renew(ctx); err != nil {
		return fmt.Errorf("service: marking the liveness row draining: %w", err)
	}
	return nil
}

// Close removes the Instance's row, so other Instances take its Sessions over
// at once instead of waiting for the row to expire. A closed Row writes
// nothing more, so its row never comes back. Closing twice is a no-op.
func (r *Row) Close(ctx context.Context) error {
	r.mu.Lock()
	started := r.started
	ticker := r.ticker
	r.started = false
	r.closed = true
	r.expired = true
	name := r.instance
	r.mu.Unlock()
	if !started {
		return nil
	}
	ticker.Stop()
	err := r.cfg.Store.Beat(ctx, store.InstanceRow{ID: name}, 0)
	if errors.Is(err, store.ErrExpired) {
		// A store that keeps an ended row refuses an expired one.
		return nil
	}
	return err
}
