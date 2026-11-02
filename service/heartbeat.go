package service

import (
	"context"
	"errors"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service/internal/heartbeat"
)

// Heartbeat sets how a Runner keeps its Liveness row (session.Liveness), and
// how long a Run waits for a Session that runs on another Instance.
type Heartbeat struct {
	// TTL is how long the row stays alive after each renewal, by the store's
	// clock. The Runner renews it every TTL/3. Zero means 10 seconds.
	TTL time.Duration
	// Wait bounds how long a Run waits for a Session that is running on
	// another live Instance before it is refused with ErrBusy. Zero means 5
	// seconds.
	Wait time.Duration
}

const (
	defaultHeartbeatTTL  = 10 * time.Second
	defaultHeartbeatWait = 5 * time.Second
)

func (h Heartbeat) withDefaults() Heartbeat {
	if h.TTL <= 0 {
		h.TTL = defaultHeartbeatTTL
	}
	if h.Wait <= 0 {
		h.Wait = defaultHeartbeatWait
	}
	return h
}

// errHolderBusy reports, inside open, a Session running on another live
// Instance.
var errHolderBusy = errors.New("service: the session is running on another instance")

// id returns the name this Runner writes with now.
func (r *Runner) id() string { return r.instance.Name() }

// Lapsed reports whether this Runner cannot show now that its Liveness row is
// alive: the last renewal is too old, or the row expired and no new one has
// been registered yet. It is false for a Runner that has not written. A Runner
// recovers by itself when the store answers again; until then it refuses
// writes, so a readiness probe should report it as not ready.
func (r *Runner) Lapsed() bool { return r.instance.Lapsed() }

// live makes sure this Runner's row is alive before it writes, beating once
// and starting the renewals the first time.
func (r *Runner) live(ctx context.Context) error {
	return storeFailureOf(ctx, r.instance.Live(ctx), true)
}

// guardFor is the guard of the Writer of one name: it refuses a write once the
// row cannot be shown to be alive, or the Runner has a new name.
func (r *Runner) guardFor(name string) error { return r.instance.Guard(name) }

// markDraining sets the draining mark in this Runner's Liveness row, so every
// Instance can see that it is leaving.
func (r *Runner) markDraining(ctx context.Context) error { return r.instance.MarkDraining(ctx) }

// lapsed reports whether err is the refusal of the Instance's own guard.
func lapsed(err error) bool { return heartbeat.Lapsed(err) }

// notRunIfLapsed marks a refusal of the Claim by the guard as not run: it is the
// first write, so nothing was written. Any other error is returned as it is.
func notRunIfLapsed(err error) error { return heartbeat.NotRunIfLapsed(err) }

// Close removes this Runner's Liveness row, so other Instances take its
// Sessions over at once instead of waiting for the row to expire, and the
// store keeps nothing of this Instance. Call it after Drain. A closed Runner
// writes nothing more, so its row never comes back. Closing twice is a no-op.
func (r *Runner) Close(ctx context.Context) error {
	// The Instance's stream ends: its subscribers read what is queued, and
	// then io.EOF.
	defer r.instanceFeed.Close()
	// Warm Sessions hold no Claim, so letting them go writes nothing.
	r.warm.Close()
	r.dropAllWarm("closed")
	return r.instance.Close(ctx)
}

// lapseNotice tells the Instance's subscribers the row lapsed.
func (r *Runner) lapseNotice(instance string) {
	r.notice(gotato.LivenessLapsed{Header: gotato.NewHeader("", "", 0), Instance: instance}, "")
}
