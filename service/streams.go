package service

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// defaultStreamPoll is how often an idle Session stream looks in the store for
// Events that another Instance wrote.
const defaultStreamPoll = time.Second

// ErrCannotResume is returned by StreamSession when the position a client
// holds no longer means what it did: the Record's epoch is not the one the
// client saw, because the Record was deleted and created again under the same
// ID, or the number is past the Record's last Event. The client starts over
// from the first Event.
var ErrCannotResume = gotato.ErrorOf(gotato.ErrCannotResume, "service: the stream cannot resume from that position")

// reconnectBase and reconnectJitter set how long a caller waits before it
// reconnects: the jitter keeps the callers of one instance from all coming
// back at the same instant.
const (
	reconnectBase   = 500 * time.Millisecond
	reconnectJitter = time.Second
)

// NewReconnect returns the advice a stream that ends gives its client: to
// reconnect, through the load balancer to any instance, after a wait of half a
// second to one and a half seconds, spread at random. why names the reason,
// such as "draining".
func NewReconnect(why string) *Reconnect {
	wait := reconnectBase + rand.N(reconnectJitter)
	return &Reconnect{RetryAfterMS: wait.Milliseconds(), Why: why}
}

// StreamFrom says where a client's Session stream starts.
type StreamFrom struct {
	// After is the number of the last stored Event the client saw. 0 starts
	// at the Session's first Event.
	After uint64
	// RecordEpoch is the epoch of the Record the client saw it in. Empty
	// skips the check, for a client that has not learned it yet.
	RecordEpoch string
}

// SessionStream is a Session's stream opened for a client: the stored Events
// after a number, read from the Record, and then the live ones, across Runs,
// on whichever Instance it is opened. Deltas are live only: a reconnect does
// not repeat those sent before it, and the finished Message holds their text.
type SessionStream struct {
	*feedSubscription
	// RecordEpoch is the Record's epoch, for the client to keep and send back.
	RecordEpoch string
	// Head is the number of the Session's last stored Event when the stream
	// opened.
	Head uint64

	mu    sync.Mutex
	ended *Reconnect
}

// Reconnect returns the advice to reconnect when the stream ended for a reason
// that a reconnect cures, such as a drain, and nil otherwise, including while
// the stream goes on. Read it after Next returned io.EOF.
func (s *SessionStream) Reconnect() *Reconnect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// endWith ends the stream after what is queued for it, with the advice.
func (s *SessionStream) endWith(hint *Reconnect) {
	s.mu.Lock()
	if s.ended == nil {
		s.ended = hint
	}
	s.mu.Unlock()
	s.End()
}

// StreamSession opens a Session's stream for a client that reconnects, on any
// instance. It starts after from.After, as SubscribeSession does, and first
// checks that the position still means what the client thinks: the Session
// must exist (session.ErrNotFound), and the client's epoch and number must be
// the Record's (ErrCannotResume). An idle stream looks in the store for Events
// that another Instance writes, every Config.StreamPoll.
//
// When the Instance's drain is over, the stream ends after the Events already
// stored, and Reconnect says to come back.
func (r *Runner) StreamSession(ctx context.Context, sessionID string, from StreamFrom) (*SessionStream, error) {
	loaded, err := r.store.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	epoch := loaded.Summary.Epoch()
	if from.RecordEpoch != "" && from.RecordEpoch != epoch {
		return nil, cannotResume("the Record is not the one the client saw", epoch, loaded.Head)
	}
	if from.After > loaded.Head {
		return nil, cannotResume("the position is past the Record's last Event", epoch, loaded.Head)
	}
	sub, err := r.openSessionStream(ctx, sessionID, from.After)
	if err != nil {
		return nil, err
	}
	stream := &SessionStream{feedSubscription: sub, RecordEpoch: epoch, Head: loaded.Head}
	sub.release = func() {
		r.releaseFeed(sessionID)
		r.streamsMu.Lock()
		delete(r.streams, stream)
		r.streamsMu.Unlock()
	}
	r.streamsMu.Lock()
	if r.streams == nil {
		r.streams = map[*SessionStream]struct{}{}
	}
	r.streams[stream] = struct{}{}
	over := r.streamsOver
	r.streamsMu.Unlock()
	if over {
		stream.endWith(NewReconnect("draining"))
	}
	return stream, nil
}

// cannotResume builds the error of a position that cannot be resumed, with the
// Record's epoch and last number for the client to start over from.
func cannotResume(why, epoch string, head uint64) error {
	err := gotato.ErrorOf(gotato.ErrCannotResume, "service: the stream cannot resume: "+why)
	err.Details = map[string]string{"record_epoch": epoch, "head": strconv.FormatUint(head, 10)}
	return err
}

// endStreams ends every Session stream with the advice to reconnect, once the
// drain is over: the Runs are saved and released, so the clients come back to
// a Session that no instance is using.
func (r *Runner) endStreams() {
	r.streamsMu.Lock()
	r.streamsOver = true
	open := make([]*SessionStream, 0, len(r.streams))
	for stream := range r.streams {
		open = append(open, stream)
	}
	r.streamsMu.Unlock()
	for _, stream := range open {
		stream.endWith(NewReconnect("draining"))
	}
}

// openSessionStream subscribes to a Session's Feed after a number, with the
// tail that follows other Instances' writes. The Feed is given back when the
// subscription ends.
func (r *Runner) openSessionStream(ctx context.Context, sessionID string, after uint64) (*feedSubscription, error) {
	feed := r.acquireFeed(sessionID, "")
	options := []gotato.SubscribeOption{gotato.SubscribeAfter(after)}
	if r.streamPoll > 0 {
		options = append(options, gotato.SubscribeTail(r.streamPoll))
	}
	sub, err := feed.Subscribe(ctx, options...)
	if err != nil {
		r.releaseFeed(sessionID)
		return nil, err
	}
	return &feedSubscription{Subscription: sub, release: func() { r.releaseFeed(sessionID) }}, nil
}
