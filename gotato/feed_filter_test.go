package gotato

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A filter with plain captured state is never called by two goroutines at
// once: the publisher's and the subscriber's catch-up. Run with -race.
func TestSubscribeFilterIsNotCalledConcurrently(t *testing.T) {
	source := &sliceSource{}
	feed := NewFeed(source)
	calls := 0 // unsynchronized on purpose
	sub, err := feed.Subscribe(context.Background(),
		SubscribeAfter(0), SubscribeBuffer(1),
		SubscribeFilter(func(Event) bool { calls++; return true }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	const total = 300
	for n := uint64(1); n <= total; n++ {
		source.add(storedEvent(n))
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := uint64(1); n <= total; n++ {
			feed.Publish(storedEvent(n))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range total {
		if _, err := sub.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
