package gotato

import (
	"context"
	"testing"
)

// A slow subscriber that accumulates coalescable progress must not lose the
// protected lifecycle events that follow it.
func TestProtectedEventSurvivesCoalescableBacklog(t *testing.T) {
	hub := newEventHub()
	stream, err := hub.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	for i := 0; i < 300; i++ {
		hub.publish(Event{Kind: EventToolExecutionUpdate, Class: EventCoalescable})
	}
	hub.publish(Event{Kind: EventAgentEnd, Class: EventProtected})

	for i := 0; i < 200; i++ {
		event, nextErr := stream.Next(context.Background())
		if nextErr != nil {
			t.Fatalf("subscription ended before the protected event: %v", nextErr)
		}
		if event.Kind == EventAgentEnd {
			return
		}
	}
	t.Fatal("protected event never arrived")
}
