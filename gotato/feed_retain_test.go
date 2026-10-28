package gotato

import "testing"

// A Feed without a source drops the Events it trims from memory, not only from
// its slice.
func TestRetainZeroesTheTrimmedTail(t *testing.T) {
	feed := NewFeed(nil)
	feed.ringCap = 4
	for n := uint64(1); n <= 5; n++ {
		feed.Publish(storedEvent(n))
	}
	feed.mu.Lock()
	defer feed.mu.Unlock()
	full := feed.ring[:cap(feed.ring)]
	for i := len(feed.ring); i < len(full); i++ {
		if full[i] != nil {
			t.Fatalf("ring[%d] still holds an Event past len %d", i, len(feed.ring))
		}
	}
}
