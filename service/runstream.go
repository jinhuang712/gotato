package service

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// runStream is the Run's stream of a StreamRun: it is handed the Subscription
// as soon as the Run holds its Session.
type runStream struct {
	ready func(gotato.Subscription)
	// admitted receives one value once the Run is admitted.
	admitted chan struct{}
	sub      gotato.Subscription
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
		ElapsedMS: float64(elapsed) / float64(time.Millisecond),
		Failed:    err != nil,
	}
}

func savedEvent(sessionID string, runID gotato.RunID, messages int, stats session.WriteStats, err error) gotato.SessionSaved {
	return gotato.SessionSaved{
		Header:    gotato.NewHeader(gotato.SessionID(sessionID), runID, 0),
		Messages:  messages,
		ElapsedMS: float64(stats.Elapsed) / float64(time.Millisecond),
		Writes:    stats.Writes,
		Failed:    err != nil,
	}
}
