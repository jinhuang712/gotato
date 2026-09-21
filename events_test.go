package gotato

import (
	"context"
	"strings"
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

// When the buffer holds nothing but protected events, an arriving protected
// event cannot make room, so the subscription ends with a buffer-full error
// instead of silently dropping state.
func TestProtectedEventBufferFullEndsSubscription(t *testing.T) {
	hub := newEventHub()
	stream, err := hub.subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// One more protected event than the subscription buffer holds.
	for i := 0; i < cap(stream.(*eventSubscription).ch)+1; i++ {
		hub.publish(Event{Kind: EventAgentEnd, Class: EventProtected})
	}
	_, nextErr := stream.Next(context.Background())
	if nextErr == nil || !strings.Contains(nextErr.Error(), "protected event buffer full") {
		t.Fatalf("overflow error = %v", nextErr)
	}
}
