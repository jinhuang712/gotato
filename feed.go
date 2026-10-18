package gotato

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Subscription is a queue of Events, in order: a Run's, a Session's from any
// number, or an Instance's. The Session that fills it never waits for it: a
// subscriber that reads slowly slows only itself.
type Subscription interface {
	// Next returns the next Event. It returns io.EOF once the stream has
	// ended, and ctx's error when ctx ends first.
	Next(ctx context.Context) (Event, error)
	// Close ends the subscription at once, and drops what is queued.
	Close() error
	// End ends the stream after the Events already queued for it: Next
	// returns them, and then io.EOF.
	End()
}

// EventSource reads stored Events again by number. A subscriber that falls too
// far behind is no longer buffered in memory: it catches up from its Feed's
// source, because the Record only grows. session.Writer is the source of a
// Session that has a store.
type EventSource interface {
	// EventsAfter returns the stored Events whose Number is greater than
	// after, in order. limit is a hint for how many the caller reads at once:
	// a source may return fewer, none when it has no more, or all it has.
	EventsAfter(ctx context.Context, after uint64, limit int) ([]Event, error)
}

// ErrEventsLost ends a Subscription that needs Events its source no longer
// holds.
var ErrEventsLost = errors.New("gotato: the Events this subscriber missed are no longer available")

const (
	defaultSubscriptionBuffer = 256
	catchUpBatch              = 128
	catchUpRetries            = 100
	catchUpPause              = 5 * time.Millisecond
	defaultRetainedEvents     = 4096
)

// Feed fans a stream of Events out to Subscriptions. Publish never blocks: it
// puts each Event in every subscriber's queue and returns.
//
// A Session's Feed holds that Session's Events, which have numbers. A
// subscriber that falls behind catches up by number, and only deltas are
// skipped. An Instance's Feed (NewInstanceFeed) mixes Sessions: a subscriber
// that falls behind skips Events and is told how many, with an EventsSkipped
// Event.
type Feed struct {
	mu     sync.Mutex
	subs   map[*subscription]struct{}
	source EventSource
	mixed  bool
	last   uint64
	count  uint64
	closed bool
	mirror *Feed
	label  string

	// ring holds the most recent stored Events, for a Feed without a source.
	ring    []Event
	ringCap int
	dropped bool
}

// NewFeed creates the Feed of one Session. A subscriber that falls behind
// catches up from source; with a nil source the Feed keeps the last 4096
// stored Events in memory for that.
func NewFeed(source EventSource) *Feed {
	return &Feed{subs: map[*subscription]struct{}{}, source: source, ringCap: defaultRetainedEvents}
}

// NewInstanceFeed creates the Feed of an Instance: every Session's Events and
// the Instance's own, with no single number.
func NewInstanceFeed() *Feed {
	return &Feed{subs: map[*subscription]struct{}{}, mixed: true}
}

// MirrorTo has every Event published here also published to other, as a
// Session's Feed does to its Instance's.
func (f *Feed) MirrorTo(other *Feed) {
	f.mu.Lock()
	f.mirror = other
	f.mu.Unlock()
}

// SetLabel sets the label that goes with every Event published here when it is
// mirrored to another Feed, such as the name of the Agent whose Session this
// is. A subscriber of the other Feed can filter on it (SubscribeLabelFilter).
func (f *Feed) SetLabel(label string) {
	f.mu.Lock()
	f.label = label
	f.mu.Unlock()
}

// SetSource sets where a subscriber that falls behind catches up. A Feed that
// has a source keeps no Events of its own.
func (f *Feed) SetSource(source EventSource) {
	f.mu.Lock()
	f.source = source
	f.ring, f.dropped = nil, false
	f.mu.Unlock()
}

// Published returns how many Events the Feed has accepted. A test that reads a
// Subscription compares it with how many it has read, to know that it has
// seen everything.
func (f *Feed) Published() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

// Last returns the Number of the last stored Event published.
func (f *Feed) Last() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// SubscribeOption configures Feed.Subscribe.
type SubscribeOption func(*subscribeConfig)

type subscribeConfig struct {
	after    uint64
	hasAfter bool
	buffer   int
	filter   func(Event) bool
	labeled  func(Event, string) bool
	until    func(Event) bool
}

// SubscribeAfter starts a Session's stream after the stored Event numbered n:
// first the later Events from the Record, then the live ones.
func SubscribeAfter(n uint64) SubscribeOption {
	return func(c *subscribeConfig) { c.after, c.hasAfter = n, true }
}

// SubscribeBuffer sets how many Events a subscriber's queue holds before it
// stops being buffered. The default is 256.
func SubscribeBuffer(n int) SubscribeOption {
	return func(c *subscribeConfig) { c.buffer = n }
}

// SubscribeFilter keeps only the Events for which keep returns true. It runs
// on the publisher's goroutine, so it must be fast and must not block.
func SubscribeFilter(keep func(Event) bool) SubscribeOption {
	return func(c *subscribeConfig) { c.filter = keep }
}

// SubscribeLabelFilter keeps only the Events for which keep returns true,
// given the label of the Feed the Event was mirrored from (see SetLabel). The
// label is empty for an Event published to this Feed directly.
func SubscribeLabelFilter(keep func(event Event, label string) bool) SubscribeOption {
	return func(c *subscribeConfig) { c.labeled = keep }
}

// SubscribeUntil ends the stream after the Event for which done returns true.
func SubscribeUntil(done func(Event) bool) SubscribeOption {
	return func(c *subscribeConfig) { c.until = done }
}

// NextRun returns the options of a Run's stream: the Events of the next Run
// that starts, from its RunStarted to its RunEnded. Pass them to Subscribe
// before the Run starts.
func NextRun() []SubscribeOption {
	var mu sync.Mutex
	var run RunID
	return []SubscribeOption{
		SubscribeFilter(func(e Event) bool {
			mu.Lock()
			defer mu.Unlock()
			if run == "" {
				if started, ok := e.(RunStarted); ok {
					run = started.RunID
					return true
				}
				return false
			}
			return e.Head().RunID == run
		}),
		SubscribeUntil(func(e Event) bool {
			mu.Lock()
			defer mu.Unlock()
			_, ended := e.(RunEnded)
			return ended && run != "" && e.Head().RunID == run
		}),
	}
}

// Subscribe starts a Subscription. Without SubscribeAfter it carries the
// Events published from now on.
func (f *Feed) Subscribe(ctx context.Context, options ...SubscribeOption) (Subscription, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var config subscribeConfig
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.buffer <= 0 {
		config.buffer = defaultSubscriptionBuffer
	}
	s := &subscription{
		feed:    f,
		max:     config.buffer,
		filter:  config.filter,
		labeled: config.labeled,
		until:   config.until,
		wake:    make(chan struct{}, 1),
		out:     make(chan Event),
		done:    make(chan struct{}),
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, io.EOF
	}
	if !f.mixed {
		if config.hasAfter {
			s.behind, s.cursor, s.resume, s.cursorSet = true, config.after, config.after, true
		}
	}
	f.subs[s] = struct{}{}
	f.mu.Unlock()
	go s.forward(ctx)
	return s, nil
}

// Publish puts an Event in every subscriber's queue, and returns at once. A
// stored Event is published only after its write succeeded. Writers on
// different goroutines can publish out of order, or an Event can be missing;
// each subscriber notices a number that is not the next one and reads what it
// missed from the Record, and ignores one it has already seen.
func (f *Feed) Publish(event Event) {
	f.mu.Lock()
	label := f.label
	f.mu.Unlock()
	f.PublishLabeled(event, label)
}

// PublishLabeled is Publish with the label that a subscriber's
// SubscribeLabelFilter sees.
func (f *Feed) PublishLabeled(event Event, label string) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if !f.mixed && event.Kind().Class() == ClassStored {
		if number := event.Head().Number; number > f.last {
			f.last = number
		}
		f.retain(event)
	}
	f.count++
	for s := range f.subs {
		s.offer(event, label)
	}
	mirror := f.mirror
	f.mu.Unlock()
	if mirror != nil {
		mirror.PublishLabeled(event, label)
	}
}

// retain keeps a stored Event for catch-up when the Feed has no source. The
// caller holds the lock.
func (f *Feed) retain(event Event) {
	if f.source != nil {
		return
	}
	if len(f.ring) >= f.ringCap {
		f.ring = append(f.ring[:0], f.ring[len(f.ring)-f.ringCap/2:]...)
		f.dropped = true
	}
	// Keep the ring in the order of the numbers, whatever order they came in.
	at := len(f.ring)
	for at > 0 && f.ring[at-1].Head().Number > event.Head().Number {
		at--
	}
	if at > 0 && f.ring[at-1].Head().Number == event.Head().Number {
		return
	}
	f.ring = append(f.ring, nil)
	copy(f.ring[at+1:], f.ring[at:])
	f.ring[at] = event
}

// eventsAfter reads stored Events again, from the source or from memory.
func (f *Feed) eventsAfter(ctx context.Context, after uint64, limit int) ([]Event, error) {
	f.mu.Lock()
	source := f.source
	if source == nil {
		defer f.mu.Unlock()
		if f.dropped && len(f.ring) > 0 && f.ring[0].Head().Number > after+1 {
			return nil, ErrEventsLost
		}
		var out []Event
		for _, event := range f.ring {
			if event.Head().Number > after {
				out = append(out, event)
				if len(out) == limit {
					break
				}
			}
		}
		return out, nil
	}
	f.mu.Unlock()
	return source.EventsAfter(ctx, after, limit)
}

// Close ends the Feed: each subscriber gets what is queued for it, and
// whatever it still has to catch up on, and then io.EOF.
func (f *Feed) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	for s := range f.subs {
		s.end()
	}
	f.mu.Unlock()
}

func (f *Feed) remove(s *subscription) {
	f.mu.Lock()
	delete(f.subs, s)
	f.mu.Unlock()
}

// subscription is one subscriber: a bounded queue the Feed fills, and one
// goroutine that forwards it in order.
type subscription struct {
	feed    *Feed
	max     int
	filter  func(Event) bool
	labeled func(Event, string) bool
	until   func(Event) bool

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

// offer puts an Event in the queue without ever blocking. The Feed's lock is
// held.
func (s *subscription) offer(event Event, label string) {
	keep := (s.filter == nil || s.filter(event)) && (s.labeled == nil || s.labeled(event, label))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ending && !s.behind {
		return
	}
	class := event.Kind().Class()
	stored := class == ClassStored && !s.feed.mixed
	if s.behind {
		// A stored Event is read again from the Record; a delta is skipped.
		return
	}
	if stored {
		number := event.Head().Number
		if s.cursorSet {
			if number <= s.cursor {
				return
			}
			if number > s.cursor+1 {
				// An Event was written that this Feed was not given: another
				// Instance wrote it, or a write whose outcome was unknown
				// landed. Read it, and what follows, from the Record.
				s.behind, s.resume = true, s.cursor
				s.signal()
				return
			}
		}
		if !keep {
			s.cursor, s.cursorSet = number, true
			return
		}
	} else if !keep {
		return
	}
	if len(s.queue) >= s.max {
		// Make room by dropping the oldest delta; only deltas may be skipped.
		dropped := false
		for i, queued := range s.queue {
			if queued.Kind().Class() == ClassDelta {
				s.queue = append(s.queue[:i], s.queue[i+1:]...)
				dropped = true
				break
			}
		}
		switch {
		case class == ClassDelta:
			if s.feed.mixed {
				s.skipped++
			}
			return
		case !dropped && s.feed.mixed:
			s.skipped++
			return
		case !dropped:
			s.behind, s.resume = true, s.cursor
			s.signal()
			return
		}
		if s.feed.mixed {
			s.skipped++
		}
	}
	s.queue = append(s.queue, event)
	if stored {
		s.cursor, s.cursorSet = event.Head().Number, true
	}
	s.signal()
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
		if err != nil && !errors.Is(err, context.Canceled) {
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
	var pending []Event
	for {
		event, ok, err := s.take(ctx, &pending)
		if err != nil {
			s.finish(err)
			return
		}
		if !ok {
			s.finish(nil)
			return
		}
		select {
		case s.out <- event:
		case <-s.done:
			return
		case <-ctx.Done():
			s.finish(ctx.Err())
			return
		}
		if s.until != nil && s.until(event) {
			s.finish(nil)
			return
		}
	}
}

// take returns the next Event: from the pending catch-up batch, the queue, or
// the source. ok is false when the stream has ended.
func (s *subscription) take(ctx context.Context, pending *[]Event) (Event, bool, error) {
	empty := 0
	for {
		if len(*pending) > 0 {
			event := (*pending)[0]
			*pending = (*pending)[1:]
			return event, true, nil
		}
		s.mu.Lock()
		if s.skipped > 0 {
			skipped := s.skipped
			s.skipped = 0
			s.mu.Unlock()
			return EventsSkipped{Header: NewHeader("", "", 0), Skipped: skipped}, true, nil
		}
		if len(s.queue) > 0 {
			event := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return event, true, nil
		}
		behind, resume, ending := s.behind, s.resume, s.ending
		s.mu.Unlock()
		if behind {
			batch, err := s.feed.eventsAfter(ctx, resume, catchUpBatch)
			if err != nil {
				return nil, false, err
			}
			if len(batch) > 0 {
				s.mu.Lock()
				s.resume = batch[len(batch)-1].Head().Number
				s.cursor = max(s.cursor, s.resume)
				s.mu.Unlock()
				for _, event := range batch {
					if s.filter == nil || s.filter(event) {
						*pending = append(*pending, event)
					}
				}
				continue
			}
			// Nothing more to read: rejoin the live Events once the Feed
			// has published nothing newer than what was read.
			s.feed.mu.Lock()
			s.mu.Lock()
			if s.resume >= s.feed.last {
				s.behind, s.cursorSet = false, true
				empty = 0
			}
			caughtUp := !s.behind
			s.mu.Unlock()
			s.feed.mu.Unlock()
			if caughtUp {
				continue
			}
			// The source holds nothing the Feed has published since: give
			// a store that is slow to show its last write a moment, and then
			// give up, because the Events are not coming.
			if empty++; empty > catchUpRetries {
				return nil, false, ErrEventsLost
			}
			select {
			case <-time.After(catchUpPause):
			case <-s.done:
				return nil, false, nil
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
			continue
		}
		if ending {
			return nil, false, nil
		}
		select {
		case <-s.wake:
		case <-s.done:
			return nil, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
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
