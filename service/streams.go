package service

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// defaultStreamPoll is how often an idle Session stream looks in the store for
// Events that another Instance wrote.
const defaultStreamPoll = time.Second

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
//
// A stream is bound to the Record it opened, the one RecordEpoch names: it
// reads that Record's Feed, and every read of the store that returns Events is
// checked against the epoch before they are delivered. So it never delivers an
// Event of a Record created again under the same ID; it ends with
// ErrCannotResume or session.ErrNotFound instead (see StreamSession for when).
type SessionStream struct {
	*feedSubscription
	// RecordEpoch is the Record's epoch, for the client to keep and send back.
	RecordEpoch string
	// Head is the number of the Session's last stored Event when the stream
	// opened.
	Head uint64

	sessionID string

	// started and seen are guarded by the Runner's streamsMu. A stream is
	// registered before it reads the Record, so that what this Instance does
	// to the Record meanwhile reaches it: until it starts, seen collects the
	// epochs of the Records this Instance opened, created or streamed under
	// its ID, "" for a delete, and it checks them against its own first.
	started bool
	seen    []string
	// startedAt is the number of the start (Runner.streamStarts) at which the
	// stream started; 0 until then.
	startedAt uint64

	mu      sync.Mutex
	ended   *Reconnect
	failure error
}

// Next implements gotato.Subscription. A stream that was cut, because its
// Record was deleted or created again, returns that error from then on, and no
// Event after the cut, not even one already queued.
func (s *SessionStream) Next(ctx context.Context) (gotato.Event, error) {
	if failure := s.cut(); failure != nil {
		return nil, failure
	}
	event, err := s.feedSubscription.Next(ctx)
	if failure := s.cut(); failure != nil {
		return nil, failure
	}
	return event, err
}

func (s *SessionStream) cut() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// failWith cuts the stream with err at once: what is queued, and what a
// catch-up under way has read, is dropped, and Next returns err.
func (s *SessionStream) failWith(err error) {
	s.mu.Lock()
	if s.failure == nil {
		s.failure = err
	}
	s.mu.Unlock()
	_ = s.Close()
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
// instance. It starts after from.After, like a Subscription's SubscribeAfter, and first
// checks that the position still means what the client thinks: the Session
// must exist (session.ErrNotFound), and the client's epoch and number must be
// the Record's (ErrCannotResume). An idle stream looks in the store for Events
// that another Instance writes, every Config.StreamPoll.
//
// The stream follows the Record it opened, and no other. When the Session is
// deleted, or deleted and created again, the stream delivers none of the new
// Record's Events, and ends with session.ErrNotFound or ErrCannotResume:
//   - at once, when this Instance deletes the Session, opens or creates a
//     Record of it with another epoch, or opens a stream on one; a stream
//     still opening at that moment reads the epoch again before it starts;
//   - otherwise, when a look finds the Record gone, or a read of the store
//     returns Events of another epoch. An old stream on an Instance that does
//     none of the above stays open, and delivers nothing, until the new
//     Record has more Events than the stream has seen.
//
// When the Instance's drain is over, the stream ends after the Events already
// stored, and Reconnect says to come back.
func (r *Runner) StreamSession(ctx context.Context, sessionID string, from StreamFrom) (*SessionStream, error) {
	stream := &SessionStream{sessionID: sessionID}
	r.addStream(stream)
	loaded, err := r.store.Load(ctx, sessionID)
	if err != nil {
		r.dropStream(stream)
		return nil, err
	}
	epoch := loaded.Info.RecordEpoch
	if from.RecordEpoch != "" && from.RecordEpoch != epoch {
		r.dropStream(stream)
		return nil, cannotResume("the Record is not the one the client saw", epoch, loaded.Head)
	}
	if from.After > loaded.Head {
		r.dropStream(stream)
		return nil, cannotResume("the position is past the Record's last Event", epoch, loaded.Head)
	}
	// The epoch is checked after every read of the Record that returned Events,
	// and before they are delivered: an epoch never comes back, so if the Record
	// still has the stream's epoch after the read, the Events came from it.
	verify := func(ctx context.Context) error {
		now, err := r.store.Load(ctx, sessionID)
		if err != nil {
			return err
		}
		if now.Info.RecordEpoch != epoch {
			return cannotResume("the Record was created again", now.Info.RecordEpoch, now.Head)
		}
		return nil
	}
	key := feedKey{sessionID, epoch}
	sub, err := r.openSessionStream(ctx, key, from.After, gotato.SubscribeVerify(verify))
	if err != nil {
		r.dropStream(stream)
		return nil, err
	}
	stream.feedSubscription, stream.RecordEpoch, stream.Head = sub, epoch, loaded.Head
	sub.release = func() {
		r.releaseFeed(key)
		r.dropStream(stream)
	}
	for {
		recheck, older, over := r.startStream(stream)
		if !recheck {
			for _, old := range older {
				old.failWith(cannotResume("the Record was created again", epoch, loaded.Head))
			}
			if over {
				stream.endWith(NewReconnect("draining"))
			}
			return stream, nil
		}
		// While this stream read the Record, this Instance saw the Record
		// with another epoch, or deleted it: read the epoch again, to know
		// which of the two is older.
		now, err := r.store.Load(ctx, sessionID)
		if err == nil && now.Info.RecordEpoch != epoch {
			err = cannotResume("the Record was created again", now.Info.RecordEpoch, now.Head)
		}
		if err != nil {
			_ = sub.Close()
			return nil, err
		}
	}
}

// cannotResume builds the error of a position that cannot be resumed, with the
// Record's epoch and last number for the client to start over from.
func cannotResume(why, epoch string, head uint64) error {
	err := gotato.ErrorOf(gotato.CodeCannotResume, "service: the stream cannot resume: "+why)
	err.Cause = ErrCannotResume
	err.Details = map[string]string{"record_epoch": epoch, "head": strconv.FormatUint(head, 10)}
	return err
}

// addStream registers a Session stream that is opening, before it reads the
// Record.
func (r *Runner) addStream(stream *SessionStream) {
	r.streamsMu.Lock()
	if r.streams == nil {
		r.streams = map[*SessionStream]struct{}{}
	}
	r.streams[stream] = struct{}{}
	r.streamsMu.Unlock()
}

func (r *Runner) dropStream(stream *SessionStream) {
	r.streamsMu.Lock()
	delete(r.streams, stream)
	r.streamsMu.Unlock()
}

// startStream starts a stream that has read its Record's epoch, unless this
// Instance saw the Record with another epoch, or deleted it, while the stream
// read it: then it reports recheck, and the stream reads the epoch again.
// Otherwise it returns the streams of the same Session that this one shows to
// be of an older Record: each started before this one was registered, or
// marked it while it opened and so made it read the epoch again, so it read
// its epoch before this one did. The streams still opening are marked with
// this one's epoch. over says the drain is over.
func (r *Runner) startStream(stream *SessionStream) (recheck bool, older []*SessionStream, over bool) {
	r.streamsMu.Lock()
	defer r.streamsMu.Unlock()
	for _, epoch := range stream.seen {
		if epoch != stream.RecordEpoch {
			recheck = true
		}
	}
	stream.seen = nil
	if recheck {
		return true, nil, false
	}
	stream.started = true
	r.streamStarts++
	stream.startedAt = r.streamStarts
	for other := range r.streams {
		if other == stream || other.sessionID != stream.sessionID {
			continue
		}
		if !other.started {
			other.seen = append(other.seen, stream.RecordEpoch)
			continue
		}
		if other.RecordEpoch != stream.RecordEpoch {
			older = append(older, other)
		}
	}
	return false, older, r.streamsOver
}

// streamMark returns the number of the last stream start, to take before a
// read of the Record. A stream that started after it may have read the Record
// after that read, and so be of a newer Record than the one read.
func (r *Runner) streamMark() uint64 {
	r.streamsMu.Lock()
	defer r.streamsMu.Unlock()
	return r.streamStarts
}

// cutStreams ends the Session streams of a Session whose Record has another
// epoch than theirs, with cannot_resume. This Instance calls it when it has
// opened or created a Record, so the open streams of the Record it replaced
// end at once, instead of waiting for a read of the store to show it. mark is
// streamMark from before the open read the Record, or before create wrote it.
//
// A stream that started before mark, with another epoch, is of an older
// Record, and is cut. One that started after mark may be of an older or of a
// newer Record, since the Record can be deleted and created again on another
// Instance after the claim was written, and a client can stream the new one
// here before this runs. For those it reads the Record once more, and cuts the
// ones whose epoch is not the Record's now: an epoch never comes back, so such
// a stream is of a Record that was replaced. Should that read fail, they stay,
// and the checks of the stream itself cut them. A stream still opening is
// marked instead, and reads its epoch again before it starts.
func (r *Runner) cutStreams(ctx context.Context, sessionID, epoch string, head uint64, mark uint64) {
	stale, unsure := r.markStreams(sessionID, epoch, mark)
	for _, stream := range stale {
		stream.failWith(cannotResume("the Record was created again", epoch, head))
	}
	if len(unsure) == 0 {
		return
	}
	now, err := r.store.Load(ctx, sessionID)
	if err != nil {
		return
	}
	for _, stream := range unsure {
		if stream.RecordEpoch != now.Info.RecordEpoch {
			stream.failWith(cannotResume("the Record was created again", now.Info.RecordEpoch, now.Head))
		}
	}
}

// endSessionStreams ends the Session streams open on this Instance for a
// Session that was deleted, with err, at once, so a Session created again
// under the same ID is not read as a continuation. A stream still opening is
// marked instead, and reads the Record again before it starts.
func (r *Runner) endSessionStreams(sessionID string, err error) {
	stale, _ := r.markStreams(sessionID, "", math.MaxUint64)
	for _, stream := range stale {
		stream.failWith(err)
	}
}

// markStreams returns the started streams of a Session whose epoch is not
// epoch, "" for a Session deleted, and marks those still opening with it. The
// ones that started up to mark are stale; the later ones are unsure (see
// cutStreams).
func (r *Runner) markStreams(sessionID, epoch string, mark uint64) (stale, unsure []*SessionStream) {
	r.streamsMu.Lock()
	defer r.streamsMu.Unlock()
	for stream := range r.streams {
		if stream.sessionID != sessionID {
			continue
		}
		if !stream.started {
			stream.seen = append(stream.seen, epoch)
			continue
		}
		switch {
		case stream.RecordEpoch == epoch:
		case stream.startedAt <= mark:
			stale = append(stale, stream)
		default:
			unsure = append(unsure, stream)
		}
	}
	return stale, unsure
}

// endStreams ends every Session stream with the advice to reconnect, once the
// drain is over: the Runs are saved and released, so the clients come back to
// a Session that no instance is using. A stream still opening sees the drain
// when it starts.
func (r *Runner) endStreams() {
	r.streamsMu.Lock()
	r.streamsOver = true
	open := make([]*SessionStream, 0, len(r.streams))
	for stream := range r.streams {
		if stream.started {
			open = append(open, stream)
		}
	}
	r.streamsMu.Unlock()
	for _, stream := range open {
		stream.endWith(NewReconnect("draining"))
	}
}

// openSessionStream subscribes to a Record's Feed after a number, with the
// tail that follows other Instances' writes. The Feed is given back when the
// subscription ends.
func (r *Runner) openSessionStream(ctx context.Context, key feedKey, after uint64, extra ...gotato.SubscribeOption) (*feedSubscription, error) {
	feed := r.acquireFeed(key, "")
	options := append([]gotato.SubscribeOption{gotato.SubscribeAfter(after)}, extra...)
	if r.streamPoll > 0 {
		options = append(options, gotato.SubscribeTail(r.streamPoll))
	}
	sub, err := feed.Subscribe(ctx, options...)
	if err != nil {
		r.releaseFeed(key)
		return nil, err
	}
	return &feedSubscription{Subscription: sub, release: func() { r.releaseFeed(key) }}, nil
}

// Reconnect tells a caller to send its next request for the Session through
// the load balancer, to any instance, after waiting RetryAfterMS. When the
// Run has a RunID, its prompt is saved, and the caller continues the Session;
// otherwise nothing ran, and the caller sends the request again.
type Reconnect struct {
	RetryAfterMS int64 `json:"retry_after_ms"`
	// Why names the reason, such as "draining".
	Why string `json:"why,omitempty"`
}
