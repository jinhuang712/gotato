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
// source, because the Record only grows. store.Writer is the source of a
// Session that has a store.
// [SEAM] a Feed catches a slow subscriber up from this source; store.Writer is the one implementation in the repository.
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

// Feed fans a stream of Events out to Subscriptions. Publishing never blocks:
// it puts each Event in every subscriber's queue and returns. A Session makes
// its own Feed, and a Runner makes one for each Record and one for the
// Instance; a library user only subscribes to one (Subscribe) and closes what
// it subscribed to.
//
// A Session's Feed holds that Session's Events, which have numbers. A
// subscriber that falls behind catches up by number, and only deltas are
// skipped. An Instance's Feed (newInstanceFeed) mixes Sessions: a subscriber
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

// newFeed creates the Feed of one Session. A subscriber that falls behind
// catches up from source; with a nil source the Feed keeps the last 4096
// stored Events in memory for that.
func newFeed(source EventSource) *Feed {
	return &Feed{subs: map[*subscription]struct{}{}, source: source, ringCap: defaultRetainedEvents}
}

// newInstanceFeed creates the Feed of an Instance: every Session's Events and
// the Instance's own, with no single number.
func newInstanceFeed() *Feed {
	return &Feed{subs: map[*subscription]struct{}{}, mixed: true}
}

// mirrorTo has every Event published here also published to other, as a
// Session's Feed does to its Instance's.
func (f *Feed) mirrorTo(other *Feed) {
	f.mu.Lock()
	f.mirror = other
	f.mu.Unlock()
}

// setLabel sets the label that goes with every Event published here when it is
// mirrored to another Feed, such as the name of the Agent whose Session this
// is. A subscriber of the other Feed can filter on it (SubscribeLabelFilter).
func (f *Feed) setLabel(label string) {
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
func (f *Feed) published() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

// Last returns the Number of the last stored Event published.
func (f *Feed) lastNumber() uint64 {
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
// stored Event is published only after its write succeeded, by the one sender
// a Session's Feed has, which holds the lock that numbers them: so they come
// in the order of their numbers. An Event can still be missing, when another
// Instance wrote it; each subscriber notices a number that is not the next one
// and reads what it missed from the Record, and ignores one it has already
// read from there.
func (f *Feed) publish(event Event) {
	f.mu.Lock()
	label := f.label
	f.mu.Unlock()
	f.publishLabeled(event, label)
}

// publishLabeled is Publish with the label that a subscriber's
// SubscribeLabelFilter sees.
func (f *Feed) publishLabeled(event Event, label string) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if !f.mixed && event.Class() == EventClassStored {
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
		mirror.publishLabeled(event, label)
	}
}

// retain keeps a stored Event for catch-up when the Feed has no source. The
// caller holds the lock. A Feed without a source is a Session's own, whose one
// sender publishes in the order of the numbers, so the ring stays in order.
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
	f.ring = append(f.ring, event)
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

// SubscribeOption configures Feed.Subscribe.
type SubscribeOption func(*subscribeConfig)

type subscribeConfig struct {
	after    uint64
	hasAfter bool
	buffer   int
	filter   func(Event) bool
	labeled  func(Event, string) bool
	until    func(Event) bool
	tail     time.Duration
	verify   func(context.Context) error
}

// SubscribeAfter starts a Session's stream after the stored Event numbered n:
// first the later Events from the Record, then the live ones.
func SubscribeAfter(n uint64) SubscribeOption {
	return func(c *subscribeConfig) { c.after, c.hasAfter = n, true }
}

// SubscribeTail has a Session's stream that is waiting for its next Event look
// in the Feed's source every interval, and deliver the stored Events it finds
// there. A Feed is given only the Events that its own Instance writes. A
// stream that follows a Session whose writer is another Instance needs the
// tail to see what that Instance writes; it costs one read of the source per
// interval while the stream is idle, and a SubscribeVerify check only after a
// read that returned Events. It has no effect on a Feed without a source, or
// on an Instance's stream.
func SubscribeTail(interval time.Duration) SubscribeOption {
	return func(c *subscribeConfig) { c.tail = interval }
}

// SubscribeVerify has a Session's stream run check after every read from the
// source that returned Events, and before it queues any of them. When check
// fails, the stream ends with its error and none of those Events is delivered:
// a Session's stream uses it to end when the Record it reads is not the one it
// follows. A read that returned nothing costs no check.
func SubscribeVerify(check func(context.Context) error) SubscribeOption {
	return func(c *subscribeConfig) { c.verify = check }
}

// SubscribeBuffer sets how many Events a subscriber's queue holds before it
// stops being buffered. The default is 256.
func SubscribeBuffer(n int) SubscribeOption {
	return func(c *subscribeConfig) { c.buffer = n }
}

// SubscribeFilter keeps only the Events for which keep returns true. It runs
// on the publisher's goroutine, and on the subscriber's when it reads Events
// again from the source; the calls never overlap, so keep needs no lock of its
// own. It must be fast and must not block. It may call the Subscription's Close
// or End. A panic in it ends this Subscription: Next returns a *RuntimeError
// with CodeExtensionFailure.
func SubscribeFilter(keep func(Event) bool) SubscribeOption {
	return func(c *subscribeConfig) { c.filter = keep }
}

// SubscribeLabelFilter keeps only the Events for which keep returns true,
// given the label of the Feed the Event was mirrored from (see setLabel). The
// label is empty for an Event published to this Feed directly. Like
// SubscribeFilter's keep, it may call Close or End, and a panic in it ends this
// Subscription.
func SubscribeLabelFilter(keep func(event Event, label string) bool) SubscribeOption {
	return func(c *subscribeConfig) { c.labeled = keep }
}

// SubscribeUntil ends the stream after the Event for which done returns true. A
// panic in done ends the stream with a *RuntimeError of CodeExtensionFailure.
func SubscribeUntil(done func(Event) bool) SubscribeOption {
	return func(c *subscribeConfig) { c.until = done }
}

// SubscribeNextRun returns the options of a Run's stream: the Events of the next Run
// that starts, from its RunStarted to its RunEnded. Pass them to Subscribe
// before the Run starts.
func SubscribeNextRun() []SubscribeOption {
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
