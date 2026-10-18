package gotato

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// A Session's stream that follows a Record another Instance writes gets no
// Events from its own Feed. SubscribeTail has it look in the source now and
// then, so the stored Events that the other Instance wrote still arrive, in
// order, once each.
func TestTailReadsEventsThatThisFeedWasNeverGiven(t *testing.T) {
	source := &sliceSource{}
	feed := NewFeed(source)
	for n := uint64(1); n <= 2; n++ {
		source.add(storedEvent(n))
	}
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(0), SubscribeTail(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if got := numbers(t, sub, 2); !reflect.DeepEqual(got, []uint64{1, 2}) {
		t.Fatalf("stream = %v, want 1 2", got)
	}
	live(t, sub)

	// Written elsewhere: nothing is published here.
	source.add(storedEvent(3))
	source.add(storedEvent(4))
	if got := numbers(t, sub, 2); !reflect.DeepEqual(got, []uint64{3, 4}) {
		t.Fatalf("stream = %v, want 3 4", got)
	}

	// This Instance becomes the writer: its own publishes join the stream
	// without a repeat.
	source.add(storedEvent(5))
	feed.Publish(storedEvent(5))
	source.add(storedEvent(6))
	feed.Publish(storedEvent(6))
	if got := numbers(t, sub, 2); !reflect.DeepEqual(got, []uint64{5, 6}) {
		t.Fatalf("stream = %v, want 5 6", got)
	}
}

// Without SubscribeTail a stream reads only what its Feed is given.
func TestWithoutTailAnIdleStreamStaysIdle(t *testing.T) {
	source := &sliceSource{}
	feed := NewFeed(source)
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(0))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	live(t, sub)
	source.add(storedEvent(1))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if event, err := sub.Next(ctx); err == nil {
		t.Fatalf("an idle stream delivered %v without being given it", event.Kind())
	}
}
