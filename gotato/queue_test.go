package gotato

import (
	"context"
	"testing"
)

// A message accepted as the Run ends is taken by the host, in the order it was
// accepted, and runs in the next Run of another Session; one that does not fit
// is returned, and DropQueued reports it.
func TestTakeQueuedKeepsAcceptanceOrderAcrossQueues(t *testing.T) {
	first, err := NewSession(context.Background(), WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("a")}}), WithQueueHandoff())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	// Accepted after the Run's last point of taking messages.
	first.endingHook = func() {
		for _, send := range []func(Message) error{first.FollowUp, first.Steer, first.FollowUp} {
			if err := send(UserMessage("m")); err != nil {
				t.Error(err)
			}
		}
	}
	seen := recordAll(t, first)
	if _, err := first.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	for _, event := range seen.events() {
		if dropped, ok := event.(MessageDropped); ok {
			t.Fatalf("a message was dropped at the end of a completed Run: %+v", dropped)
		}
	}

	left := first.TakeQueued()
	if len(left) != 3 || left[0].Source != MessageSourceFollowUp || left[1].Source != MessageSourceSteer || left[2].Source != MessageSourceFollowUp {
		t.Fatalf("TakeQueued = %+v, want the three in the order they were accepted", left)
	}
	if again := first.TakeQueued(); len(again) != 0 {
		t.Fatalf("TakeQueued twice = %d messages", len(again))
	}

	limits := DefaultLimits()
	limits.MaxSteerMessages, limits.MaxFollowUpMessages = 1, 1
	second, err := NewSession(context.Background(), WithModel(&recordingModel{}), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	rest := second.Requeue(left)
	if len(rest) != 1 || rest[0].Message.ID != left[2].Message.ID {
		t.Fatalf("Requeue returned %+v, want only the last message", rest)
	}
	second.DropQueued(rest, "queue_full")
}

// A canceled Run still drops what is queued, as before.
func TestACanceledRunStillDropsItsQueue(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{{{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}, started: make(chan int, 4), gate: make(chan struct{}, 4)}
	session, err := NewSession(context.Background(), WithModel(model), WithQueueHandoff())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, runErr := session.Prompt(context.Background(), UserMessage("cancel me"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	if err := session.FollowUp(UserMessage("leftover")); err != nil {
		t.Fatal(err)
	}
	session.Abort()
	model.gate <- struct{}{}
	<-done
	if left := session.TakeQueued(); len(left) != 0 {
		t.Fatalf("a canceled Run left %d messages for the host", len(left))
	}
}

// A message queued while a Run fails is kept for the host, which decides
// whether another Run takes it.
func TestAFailedRunKeepsItsQueueForTheHost(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{{{Kind: "unknown"}}}, started: make(chan int, 4), gate: make(chan struct{}, 4)}
	session, err := NewSession(context.Background(), WithModel(model), WithQueueHandoff())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	seen := recordAll(t, session)
	done := make(chan error, 1)
	go func() {
		_, runErr := session.Prompt(context.Background(), UserMessage("fail"))
		done <- runErr
	}()
	// The model sends an event the Loop cannot read, so the Run fails.
	waitForCall(t, model.started, 0)
	if err := session.FollowUp(UserMessage("kept")); err != nil {
		t.Fatal(err)
	}
	model.gate <- struct{}{}
	if err := <-done; err == nil {
		t.Fatal("the Run did not fail")
	}
	left := session.TakeQueued()
	if len(left) != 1 || TextOf(left[0].Message) != "kept" {
		t.Fatalf("TakeQueued = %+v, want the queued message", left)
	}
	for _, event := range seen.events() {
		if _, ok := event.(MessageDropped); ok {
			t.Fatal("the message was dropped though the host could take it")
		}
	}
}
