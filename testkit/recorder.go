package testkit

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// EventRecorder keeps every Event of a Subscription in order, for a test to
// read. It reads the Subscription on a goroutine of its own, as any
// subscriber does, so the Session under test never waits for it.
type EventRecorder struct {
	mu        sync.Mutex
	changed   *sync.Cond
	events    []gotato.Event
	published func() uint64
	base      uint64
	sub       gotato.Subscription
	done      chan struct{}
}

// RecordEvents subscribes to a live Session and records every Event it
// publishes from now on, the stored Events and the deltas. Call it right after
// NewSession, before the first Prompt. Events waits until the recorder has
// read everything the Session has published, so a test can read the
// recording as soon as Prompt returns.
func RecordEvents(s *gotato.Session) *EventRecorder {
	sub, err := s.Subscribe(context.Background(), gotato.SubscribeBuffer(1<<16))
	if err != nil {
		panic("testkit: " + err.Error())
	}
	r := newRecorder(sub)
	r.published, r.base = s.Published, s.Published()
	return r
}

// RecordSubscription records every Event of sub until it ends. Events returns
// what has arrived so far; Wait waits for more.
func RecordSubscription(sub gotato.Subscription) *EventRecorder { return newRecorder(sub) }

func newRecorder(sub gotato.Subscription) *EventRecorder {
	r := &EventRecorder{sub: sub, done: make(chan struct{})}
	r.changed = sync.NewCond(&r.mu)
	go func() {
		defer close(r.done)
		defer func() {
			r.mu.Lock()
			r.changed.Broadcast()
			r.mu.Unlock()
		}()
		for {
			event, err := sub.Next(context.Background())
			if err != nil {
				return
			}
			r.mu.Lock()
			r.events = append(r.events, event)
			r.changed.Broadcast()
			r.mu.Unlock()
		}
	}()
	return r
}

// Events returns the recorded Events, in the order they were published.
func (r *EventRecorder) Events() []gotato.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.published != nil {
		deadline := time.Now().Add(5 * time.Second)
		for uint64(len(r.events)) < r.published()-r.base && time.Now().Before(deadline) {
			r.mu.Unlock()
			time.Sleep(time.Millisecond)
			r.mu.Lock()
		}
	}
	return slices.Clone(r.events)
}

// Wait blocks until the recording holds an Event for which match is true, or
// timeout passes, and reports whether it found one.
func (r *EventRecorder) Wait(timeout time.Duration, match func(gotato.Event) bool) bool {
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() {
		r.mu.Lock()
		r.changed.Broadcast()
		r.mu.Unlock()
	})
	defer timer.Stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if slices.ContainsFunc(r.events, match) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-r.done:
			return slices.ContainsFunc(r.events, match)
		default:
		}
		r.changed.Wait()
	}
}

// Close ends the Subscription and the recorder's goroutine.
func (r *EventRecorder) Close() {
	_ = r.sub.Close()
	<-r.done
}

// Kinds returns the recorded Event kinds in order.
func (r *EventRecorder) Kinds() []gotato.EventKind {
	events := r.Events()
	out := make([]gotato.EventKind, len(events))
	for i, event := range events {
		out[i] = event.Kind()
	}
	return out
}

// OfKind returns the recorded Events of one kind.
func (r *EventRecorder) OfKind(kind gotato.EventKind) []gotato.Event {
	var out []gotato.Event
	for _, event := range r.Events() {
		if event.Kind() == kind {
			out = append(out, event)
		}
	}
	return out
}

// Reset drops recorded Events.
func (r *EventRecorder) Reset() {
	r.mu.Lock()
	r.events = nil
	if r.published != nil {
		r.base = r.published()
	}
	r.mu.Unlock()
}
