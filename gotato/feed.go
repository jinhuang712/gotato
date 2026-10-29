package gotato

import (
	"context"
	"io"
	"maps"
	"slices"
	"sync"
	"time"
)

// EventSource reads stored Events again by number. A subscriber that falls too
// far behind is no longer buffered in memory: it catches up from its Feed's
// source, because the Record only grows. session.Writer is the source of a
// Session that has a store.
// [SEAM] a Feed catches a slow subscriber up from this source; session.Writer is the one implementation in the repository.
type EventSource interface {
	// EventsAfter returns the stored Events whose Number is greater than
	// after, in order. limit is a hint for how many the caller reads at once:
	// a source may return fewer, none when it has no more, or all it has.
	EventsAfter(ctx context.Context, after uint64, limit int) ([]Event, error)
}

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

// setSource sets where a subscriber that falls behind catches up. A Feed that
// has a source keeps no Events of its own.
func (f *Feed) setSource(source EventSource) {
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

// Subscribe starts a Subscription. Without SubscribeAfter it carries the
// Events published from now on. SubscribeAfter and SubscribeTail apply to a
// Session's Feed only; an Instance's Feed ignores them.
//
// A Subscription runs one goroutine. It stops when the caller calls Close,
// when the Feed closes, when SubscribeUntil fires, when the stream ends with
// an error, or when ctx ends. Close it, or pass a ctx that ends: a Subscription
// on context.Background() that is never closed lives until its Feed closes.
//
// Subscribe on a closed Feed returns a nil Subscription and io.EOF.
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
		tail:    config.tail,
		verify:  config.verify,
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
	if !f.mixed && event.Kind().Class() == EventClassStored {
		if number := event.Head().Number; number > f.last {
			f.last = number
		}
		f.retain(event)
	}
	f.count++
	subs := slices.Collect(maps.Keys(f.subs))
	mirror := f.mirror
	f.mu.Unlock()
	// [CONCURRENCY] a subscriber's filter is host code: it runs here, outside f.mu, so it may call Close or End and never holds up another publisher.
	for _, s := range subs {
		s.offer(event, label)
	}
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
		kept := copy(f.ring, f.ring[len(f.ring)-f.ringCap/2:])
		clear(f.ring[kept:]) // let the trimmed Events be collected
		f.ring = f.ring[:kept]
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

// hasSource reports whether a subscriber that falls behind can read again.
func (f *Feed) hasSource() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.source != nil
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
	var batch []Event
	var readErr error
	// [LIVENESS] a panic in the host's source ends the one Subscription that reads it, not the process.
	if failure := callHost("EventsAfter", func() error {
		batch, readErr = source.EventsAfter(ctx, after, limit)
		return nil
	}); failure != nil {
		return nil, extensionFailure("Subscription", failure)
	}
	return batch, readErr
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
