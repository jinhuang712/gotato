package service

import (
	"context"
	"io"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// sessionFeed is the Feed of one Session on this Instance, shared by every
// Run, every subscriber and every write that concerns the Session while any
// of them is active. It is mirrored to the Instance's Feed.
type sessionFeed struct {
	feed *gotato.Feed
	refs int
}

// acquireFeed returns the Session's Feed, creating it on first use, and counts
// one more user of it. A Feed catches a slow subscriber up from the store.
func (r *Runner) acquireFeed(sessionID, agent string) *gotato.Feed {
	r.feedsMu.Lock()
	defer r.feedsMu.Unlock()
	entry := r.feeds[sessionID]
	if entry == nil {
		feed := gotato.NewFeed(storeSource{store: r.store, id: sessionID})
		feed.MirrorTo(r.instanceFeed)
		entry = &sessionFeed{feed: feed}
		r.feeds[sessionID] = entry
	}
	if agent != "" {
		entry.feed.SetLabel(agent)
	}
	entry.refs++
	return entry.feed
}

// releaseFeed drops one user of the Session's Feed, and closes the Feed when
// nobody is left.
func (r *Runner) releaseFeed(sessionID string) {
	r.feedsMu.Lock()
	entry := r.feeds[sessionID]
	if entry == nil {
		r.feedsMu.Unlock()
		return
	}
	entry.refs--
	if entry.refs > 0 {
		r.feedsMu.Unlock()
		return
	}
	delete(r.feeds, sessionID)
	r.feedsMu.Unlock()
	entry.feed.Close()
}

// publisher returns the function that hands Events a Writer wrote outside a
// live Session, such as a claim or a release, to the Session's Feed when it
// has one, and to the Instance's otherwise.
func (r *Runner) publisher(sessionID, agent string) func(gotato.Event) {
	return func(event gotato.Event) {
		r.feedsMu.Lock()
		entry := r.feeds[sessionID]
		r.feedsMu.Unlock()
		if entry != nil {
			entry.feed.Publish(event)
			return
		}
		r.instanceFeed.PublishLabeled(event, agent)
	}
}

// notice publishes an Instance Event: one that happens where no Record can take
// it. It goes to the Instance's subscribers only.
func (r *Runner) notice(event gotato.Event, agent string) {
	r.instanceFeed.PublishLabeled(event, agent)
}

// storeSource reads a Session's stored Events again from the store, for a
// subscriber that fell behind or that starts after a number.
type storeSource struct {
	store session.Store
	id    string
}

func (s storeSource) EventsAfter(ctx context.Context, after uint64, _ int) ([]gotato.Event, error) {
	return session.StoredEvents(ctx, s.store, s.id, after)
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
	return r.instanceFeed.Subscribe(ctx, options...)
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

// SubscribeSession starts a Subscription to a Session's stream after the stored
// Event numbered after: the later Events first, read from the Record, and then
// the live ones, across Runs, with the same numbers on every Instance. A
// client that was cut off reconnects with the number of the last Event it saw.
// after 0 starts at the Session's first Event. The Subscription ends with
// ErrNotFound's error when the Session does not exist.
func (r *Runner) SubscribeSession(ctx context.Context, sessionID string, after uint64) (gotato.Subscription, error) {
	sub, err := r.openSessionStream(ctx, sessionID, after)
	if err != nil {
		return nil, err
	}
	return sub, nil
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

// runStream is the Run's stream of a StreamRun: it is handed the Subscription
// as soon as the Run holds its Session.
type runStream struct {
	ready func(gotato.Subscription)
	sub   gotato.Subscription
}

// tracker follows the Run the live Session started: its ID, which the
// Run's stream ends at.
type tracker struct {
	mu    sync.Mutex
	runID gotato.RunID
}

func (t *tracker) set(id gotato.RunID) {
	t.mu.Lock()
	t.runID = id
	t.mu.Unlock()
}

func (t *tracker) get() gotato.RunID {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runID
}

// ends reports whether event is the RunEnded of the tracked Run.
func (t *tracker) ends(event gotato.Event) bool {
	ended, ok := event.(gotato.RunEnded)
	return ok && ended.RunID == t.get() && ended.RunID != ""
}

// streamTo subscribes a Run's stream to feed and hands it over. It ends at the
// RunEnded of the Run the tracker follows.
func (r *Runner) streamTo(ctx context.Context, feed *gotato.Feed, stream *runStream, track *tracker) error {
	if stream == nil {
		return nil
	}
	sub, err := feed.Subscribe(ctx, gotato.SubscribeBuffer(1024), gotato.SubscribeUntil(track.ends))
	if err != nil {
		return err
	}
	stream.sub = sub
	stream.ready(sub)
	return nil
}

// deliver reads sub until it ends and calls sink for each Event, on the
// caller's goroutine. A sink error stops delivery, not the Run.
func deliver(ctx context.Context, sub gotato.Subscription, sink func(gotato.Event) error) {
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			if err != io.EOF {
				_ = sub.Close()
			}
			return
		}
		if sink(event) != nil {
			_ = sub.Close()
			return
		}
	}
}

// storeNotice builds the Instance Event for a load or a save, with the time the
// store took. A failure is only flagged: the store's error text can name paths
// and hosts, and Events reach clients.
func loadedEvent(sessionID string, messages int, elapsed time.Duration, err error) gotato.SessionLoaded {
	return gotato.SessionLoaded{
		Header:    gotato.NewHeader(gotato.SessionID(sessionID), "", 0),
		Messages:  messages,
		ElapsedMS: float64(elapsed.Microseconds()) / 1000,
		Failed:    err != nil,
	}
}

func savedEvent(sessionID string, runID gotato.RunID, messages int, stats session.WriteStats, err error) gotato.SessionSaved {
	return gotato.SessionSaved{
		Header:    gotato.NewHeader(gotato.SessionID(sessionID), runID, 0),
		Messages:  messages,
		ElapsedMS: float64(stats.Elapsed.Microseconds()) / 1000,
		Writes:    stats.Writes,
		Failed:    err != nil,
	}
}
