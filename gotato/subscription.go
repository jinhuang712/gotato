package gotato

import (
	"context"
	"io"
	"sync"
	"time"
)

// Subscription is a queue of Events, in order: a Run's, a Session's from any
// number, or an Instance's. The Session that fills it never waits for it: a
// subscriber that reads slowly slows only itself.
type Subscription interface {
	// Next returns the next Event. It returns io.EOF once the stream has
	// ended, and ctx's error when ctx ends first. A stream that ends because
	// the context given to Subscribe ended returns that context's error (also
	// context.Canceled), never io.EOF: io.EOF is a clean end only.
	Next(ctx context.Context) (Event, error)
	// Close ends the subscription at once, and drops what is queued.
	Close() error
	// End ends the stream after the Events already queued for it: Next
	// returns them, and then io.EOF.
	End()
}

// subscription is one subscriber: a bounded queue the Feed fills, and one
// goroutine that forwards it in order.
type subscription struct {
	feed    *Feed
	max     int
	filter  func(Event) bool
	labeled func(Event, string) bool
	until   func(Event) bool
	tail    time.Duration
	verify  func(context.Context) error

	filterMu sync.Mutex // serializes calls of filter

	mu     sync.Mutex
	queue  []Event
	cursor uint64 // number of the last stored Event queued or delivered
	// cursorSet is false for a live subscriber until the first stored Event
	// reaches it, which is where its stream starts.
	cursorSet bool
	behind    bool // the queue overflowed: catch up from the source after resume
	resume    uint64
	skipped   uint64
	ending    bool
	err       error

	wake chan struct{}
	out  chan Event
	done chan struct{}
	once sync.Once
}

func (s *subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// passes runs the subscriber's filter. The publisher's goroutine (offer) and
// the subscriber's own (catch-up and tail reads) both call it, so the calls are
// serialized: the filter is never run by two goroutines at once. No lock of
// the Feed or the subscription is held. A panic is the error.
func (s *subscription) passes(event Event) (keep bool, err error) {
	if s.filter == nil {
		return true, nil
	}
	// [LIVENESS] a panic in a filter ends this one Subscription; it would end the process, or leave the Feed's lock held.
	err = callHost("SubscribeFilter", func() error {
		s.filterMu.Lock()
		defer s.filterMu.Unlock()
		keep = s.filter(event)
		return nil
	})
	return keep, extensionFailure("Subscription", err)
}

// extensionFailure returns err, a panic of host code, as a RuntimeError that
// Next returns; nil stays nil.
func extensionFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	return runtimeError(CodeExtensionFailure, stage, PublicText(err, "subscription callback failed"), err)
}

// offer puts an Event in the queue without ever blocking. No lock of the Feed
// is held: the filter runs first, with no lock at all. A filter that panics
// ends this subscription.
func (s *subscription) offer(event Event, label string) {
	select {
	case <-s.done:
		return
	default:
	}
	keep, err := s.keeps(event, label)
	if err != nil {
		s.finish(err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ending && !s.behind {
		return
	}
	class := event.Class()
	stored := s.numbered(class)
	if s.behind {
		// A stored Event is read again from the Record; a delta is skipped.
		return
	}
	if stored && !s.inSequence(event.Head().Number) {
		return
	}
	if !keep {
		if stored {
			s.cursor, s.cursorSet = event.Head().Number, true
		}
		return
	}
	s.push(event, class)
}

// numbered reports whether an Event of class moves the cursor: a stored Event
// of a Feed that is not mixed.
func (s *subscription) numbered(class EventClass) bool {
	return class == EventClassStored && !s.feed.mixed
}

// push queues an Event of class, making room when the queue is full. The
// caller holds s.mu.
func (s *subscription) push(event Event, class EventClass) {
	if len(s.queue) >= s.max && !s.makeRoom(class) {
		return
	}
	s.queue = append(s.queue, event)
	if s.numbered(class) {
		s.cursor, s.cursorSet = event.Head().Number, true
	}
	s.signal()
}

// keeps runs the subscriber's filter and then its label filter. A panic in
// either is the error.
func (s *subscription) keeps(event Event, label string) (bool, error) {
	keep, err := s.passes(event)
	if err == nil && keep && s.labeled != nil {
		err = callHost("SubscribeLabelFilter", func() error { keep = s.labeled(event, label); return nil })
		err = extensionFailure("Subscription", err)
	}
	return keep, err
}

// inSequence reports whether the stored Event numbered number comes right
// after the cursor, or starts the stream. A gap sets the subscription behind.
// The caller holds s.mu.
func (s *subscription) inSequence(number uint64) bool {
	if !s.cursorSet {
		return true
	}
	if number <= s.cursor {
		return false
	}
	if number > s.cursor+1 {
		// An Event was written that this Feed was not given: another
		// Instance wrote it. Read it, and what follows, from the
		// Record.
		s.behind, s.resume = true, s.cursor
		s.signal()
		return false
	}
	return true
}

// makeRoom frees a place in a full queue for an Event of class, and reports
// whether it did. The caller holds s.mu.
func (s *subscription) makeRoom(class EventClass) bool {
	// Make room by dropping the oldest delta; only deltas may be skipped.
	dropped := s.dropOldestDelta()
	switch {
	case !dropped && class == EventClassDelta:
		if s.feed.mixed {
			s.skipped++
		}
		return false
	case !dropped && s.feed.mixed:
		s.skipped++
		return false
	case !dropped:
		s.behind, s.resume = true, s.cursor
		s.signal()
		return false
	}
	if s.feed.mixed {
		s.skipped++
	}
	return true
}

// dropOldestDelta removes the oldest delta from the queue, and reports
// whether there was one. The caller holds s.mu.
func (s *subscription) dropOldestDelta() bool {
	for i, queued := range s.queue {
		if queued.Class() == EventClassDelta {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return true
		}
	}
	return false
}

// end marks the subscription as ending: it delivers what is queued, catches
// up if it must, and then returns io.EOF.
func (s *subscription) end() {
	s.mu.Lock()
	s.ending = true
	s.mu.Unlock()
	s.signal()
}

func (s *subscription) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		if err != nil {
			s.err = err
		}
		s.mu.Unlock()
		close(s.done)
		s.feed.remove(s)
	})
}

// forward is the subscriber's goroutine: it sends Events to Next in order.
func (s *subscription) forward(ctx context.Context) {
	defer close(s.out)
	w := forwarder{subscription: s}
	for {
		event, ok, err := w.take(ctx)
		if err != nil {
			s.finish(err)
			return
		}
		if !ok {
			s.finish(nil)
			return
		}
		if !w.send(ctx, event) || w.stopAt(event) {
			return
		}
	}
}

// queueState is what take learns, under the lock, when nothing is queued.
type queueState struct {
	behind bool   // the stream must be read again from the source
	resume uint64 // the number to read after when it is behind
	ending bool   // the stream ends once the queue is empty
}

// popQueued returns the next Event the queue holds: a count of skipped Events
// first, then the oldest queued one. When it holds none, ok is false and state
// says what the subscriber does next.
func (s *subscription) popQueued() (event Event, ok bool, state queueState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skipped > 0 {
		skipped := s.skipped
		s.skipped = 0
		return EventsSkipped{Header: NewHeader("", "", 0), Skipped: skipped}, true, queueState{}
	}
	if len(s.queue) > 0 {
		event := s.queue[0]
		s.queue = s.queue[1:]
		return event, true, queueState{}
	}
	return nil, false, queueState{behind: s.behind, resume: s.resume, ending: s.ending}
}

// Next implements Subscription.
func (s *subscription) Next(ctx context.Context) (Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case event, ok := <-s.out:
		if ok {
			return event, nil
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// End implements Subscription.
func (s *subscription) End() { s.end() }

// Close implements Subscription.
func (s *subscription) Close() error {
	s.finish(nil)
	return nil
}
