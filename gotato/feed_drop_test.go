package gotato

import "testing"

// A full queue of deltas drops the oldest one to make room, and then takes the
// new one: one delta is lost, not two.
func TestFullQueueDropsOneDeltaAndKeepsTheNewOne(t *testing.T) {
	feed := NewFeed(nil)
	s := &subscription{feed: feed, max: 2, wake: make(chan struct{}, 1)}
	s.queue = []Event{delta(0, "a"), delta(0, "b")}
	s.offer(delta(0, "c"), "")
	var got []string
	for _, e := range s.queue {
		got = append(got, e.(TextDelta).Text)
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("queue = %v, want [b c]", got)
	}
}

// A mixed Feed counts one skipped Event when one delta is dropped for another.
func TestMixedFeedCountsOneSkippedDelta(t *testing.T) {
	feed := NewInstanceFeed()
	s := &subscription{feed: feed, max: 2, wake: make(chan struct{}, 1)}
	s.queue = []Event{delta(0, "a"), delta(0, "b")}
	s.offer(delta(0, "c"), "")
	if s.skipped != 1 || len(s.queue) != 2 {
		t.Fatalf("skipped = %d, queue = %d, want 1 and 2", s.skipped, len(s.queue))
	}
}
