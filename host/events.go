package host

import (
	"context"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
)

// feedSet is the Runner's Feeds: the Instance's own stream, and the Feeds of
// the Records in use, one per Session ID and epoch, each mirrored to it.
type feedSet struct {
	instance *gotato.Feed
	mu       sync.Mutex
	byKey    map[feedKey]*sessionFeed
}

// sessionFeed is the Feed of one Record on this Instance, shared by every
// Run, every subscriber and every write that concerns the Record while any of
// them is active. It is mirrored to the Instance's Feed.
type sessionFeed struct {
	feed *gotato.Feed
	refs int
}

// feedKey names the Feed of one Record: its Session's ID, and its epoch. A
// Session deleted and created again under the same ID is a Record with
// another epoch, so it never shares a Feed with the old one: a stream of the
// old Record is never given the new Record's Events, and the new Record's
// streams catch up to its own last number, not to the old one's.
type feedKey struct {
	id    string
	epoch string
}

// acquireFeed returns the Record's Feed, creating it on first use, and counts
// one more user of it. A Feed catches a slow subscriber up from the store.
func (r *Runner) acquireFeed(key feedKey, agent string) *gotato.Feed {
	r.feeds.mu.Lock()
	defer r.feeds.mu.Unlock()
	entry := r.feeds.byKey[key]
	if entry == nil {
		feed := newFeed(storeSource{store: r.store.Records, id: key.id})
		mirrorTo(feed, r.feeds.instance)
		entry = &sessionFeed{feed: feed}
		r.feeds.byKey[key] = entry
	}
	if agent != "" {
		setLabel(entry.feed, agent)
	}
	entry.refs++
	return entry.feed
}

// releaseFeed drops one user of the Record's Feed, and closes the Feed when
// nobody is left.
func (r *Runner) releaseFeed(key feedKey) {
	r.feeds.mu.Lock()
	entry := r.feeds.byKey[key]
	if entry == nil {
		r.feeds.mu.Unlock()
		return
	}
	entry.refs--
	if entry.refs > 0 {
		r.feeds.mu.Unlock()
		return
	}
	delete(r.feeds.byKey, key)
	r.feeds.mu.Unlock()
	entry.feed.Close()
}

// publisher returns the function that hands Events a Writer wrote outside a
// live Session, such as a claim or a release, to the Record's Feed when this
// Instance has one, and to the Instance's otherwise.
func (r *Runner) publisher(key feedKey, agent string) func(gotato.Event) {
	return func(event gotato.Event) {
		r.feeds.mu.Lock()
		entry := r.feeds.byKey[key]
		r.feeds.mu.Unlock()
		if entry != nil {
			publish(entry.feed, event)
			return
		}
		publishLabeled(r.feeds.instance, event, agent)
	}
}

// publishOpened hands the Events that open wrote, and every later write of
// writer, to the Feed of the Record it opened.
func (r *Runner) publishOpened(writer *store.Writer, early []gotato.Event, agent string) {
	publish := r.publisher(feedKey{writer.ID(), writer.Record().RecordEpoch()}, agent)
	writer.SetPublisher(publish)
	for _, event := range early {
		publish(event)
	}
}

// notice publishes an Instance Event: one that happens where no Record can take
// it. It goes to the Instance's subscribers only.
func (r *Runner) notice(event gotato.Event, agent string) {
	publishLabeled(r.feeds.instance, event, agent)
}

// storeSource reads a Session's stored Events again from the store, for a
// subscriber that fell behind or that starts after a number.
type storeSource struct {
	store store.Records
	id    string
}

func (s storeSource) EventsAfter(ctx context.Context, after uint64, limit int) ([]gotato.Event, error) {
	return store.StoredEvents(ctx, s.store, s.id, after, limit)
}

// SubscribeInstance starts a Subscription to the Instance's stream: every
// Event of every Session this Instance serves, and its own Instance Events,
// such as session_loaded and liveness_lapsed. Options keep only some of them:
// ForAgents, or gotato.SubscribeFilter. The stream has no single number: a
// subscriber that reads too slowly skips Events and is told how many, with an
// events_skipped Event; each stored Event names its Session and number, so what
// it missed can be read from the Record. A Run that is not saved, because it
// sets SkipSave, has no Events here.
func (r *Runner) SubscribeInstance(ctx context.Context, options ...gotato.SubscribeOption) (gotato.Subscription, error) {
	return r.feeds.instance.Subscribe(ctx, options...)
}

// ForAgents keeps only the Events of Sessions of the named Agents. The Instance
// Events that concern no Session, such as liveness_lapsed, are left out too.
func ForAgents(agents ...string) gotato.SubscribeOption {
	keep := map[string]bool{}
	for _, agent := range agents {
		keep[agent] = true
	}
	return gotato.SubscribeLabelFilter(func(_ gotato.Event, label string) bool {
		return label != "" && keep[label]
	})
}

// feedSubscription gives a Session's Feed back when its Subscription ends.
type feedSubscription struct {
	gotato.Subscription
	once    sync.Once
	release func()
}

func (s *feedSubscription) Next(ctx context.Context) (gotato.Event, error) {
	event, err := s.Subscription.Next(ctx)
	if err != nil && ctx.Err() == nil {
		s.done()
	}
	return event, err
}

func (s *feedSubscription) Close() error {
	err := s.Subscription.Close()
	s.done()
	return err
}

func (s *feedSubscription) done() { s.once.Do(s.release) }
