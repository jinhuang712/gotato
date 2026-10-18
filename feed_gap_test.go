package gotato

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// live waits until a subscription has caught up and takes live Events.
func live(t *testing.T, sub Subscription) {
	t.Helper()
	s := sub.(*subscription)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		behind := s.behind
		s.mu.Unlock()
		if !behind {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the subscription never went live")
}

// A number that is not the next one means an Event was written that the Feed
// was not given. The subscriber reads it, and what follows, from the source,
// and sees each stored Event once, in order.
func TestAGapInTheNumbersIsReadFromTheSource(t *testing.T) {
	source := &sliceSource{}
	feed := NewFeed(source)
	for n := uint64(1); n <= 2; n++ {
		source.add(storedEvent(n))
		feed.Publish(storedEvent(n))
	}
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	live(t, sub)
	for n := uint64(3); n <= 6; n++ {
		source.add(storedEvent(n)) // written, by this Instance or another
	}
	feed.Publish(storedEvent(5)) // 3 and 4 were written elsewhere
	feed.Publish(storedEvent(6))
	if got := numbers(t, sub, 4); !reflect.DeepEqual(got, []uint64{3, 4, 5, 6}) {
		t.Fatalf("stream = %v, want 3 4 5 6", got)
	}
}

// Writers on two goroutines can publish out of order; the stream is still in
// order, and misses nothing.
func TestEventsPublishedOutOfOrderAreDeliveredInOrder(t *testing.T) {
	source := &sliceSource{}
	feed := NewFeed(source)
	for n := uint64(1); n <= 2; n++ {
		source.add(storedEvent(n))
		feed.Publish(storedEvent(n))
	}
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	live(t, sub)
	for n := uint64(3); n <= 5; n++ {
		source.add(storedEvent(n))
	}
	feed.Publish(storedEvent(4))
	feed.Publish(storedEvent(3))
	feed.Publish(storedEvent(5))
	if got := numbers(t, sub, 3); !reflect.DeepEqual(got, []uint64{3, 4, 5}) {
		t.Fatalf("stream = %v, want 3 4 5", got)
	}
}
