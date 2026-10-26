package gotato

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSource holds EventsAfter until released, and says when it is called.
type blockingSource struct {
	sliceSource
	entered chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func (b *blockingSource) EventsAfter(ctx context.Context, after uint64, limit int) ([]Event, error) {
	if b.armed.Load() {
		select {
		case b.entered <- struct{}{}:
			<-b.release
		default:
		}
	}
	return b.sliceSource.EventsAfter(ctx, after, limit)
}

// The tail looks in the source while the stream is live, and a delta published
// during the look is not lost: the look is not a catch-up.
func TestTailDoesNotDropDeltasWhileItReads(t *testing.T) {
	source := &blockingSource{entered: make(chan struct{}, 1), release: make(chan struct{})}
	feed := NewFeed(source)
	source.add(storedEvent(1))
	feed.Publish(storedEvent(1))
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(1), SubscribeTail(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	live(t, sub)
	source.armed.Store(true)
	select {
	case <-source.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tail never looked in the source")
	}
	feed.Publish(TextDelta{Header: Header{Number: 1}, Text: "hello"})
	close(source.release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("the delta published during the look was lost: %v", err)
	}
	if _, ok := event.(TextDelta); !ok {
		t.Fatalf("got %s, want the delta", event.Kind())
	}
}
