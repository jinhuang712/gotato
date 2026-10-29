package gotato

import (
	"context"
	"sync"
	"testing"
)

// checkLedger asserts that every message that was queued (message_queued,
// notice_queued) is closed exactly once, by a commit or a drop, and that no
// message is refused after it was queued. It returns the ids still open.
func checkLedger(t *testing.T, events []Event) (open []MessageID) {
	t.Helper()
	state := map[MessageID]string{} // "queued", "closed"
	var order []MessageID
	closeOnce := func(id MessageID, how string) {
		switch state[id] {
		case "queued":
			state[id] = "closed"
		case "closed":
			t.Errorf("message %s is closed twice (second by %s)", id, how)
		}
	}
	for _, event := range events {
		switch e := event.(type) {
		case MessageQueued:
			state[e.MessageID] = "queued"
			order = append(order, e.MessageID)
		case NoticeQueued:
			state[e.MessageID] = "queued"
			order = append(order, e.MessageID)
		case MessageCommitted:
			closeOnce(e.Message.ID, "commit")
		case NoticeCommitted:
			closeOnce(e.Message.ID, "commit")
		case MessageDropped:
			closeOnce(e.MessageID, "drop:"+e.Why)
		case MessageRejected:
			if state[e.MessageID] != "" {
				t.Errorf("message %s is rejected (%s) after it was queued", e.MessageID, e.Why)
			}
		}
	}
	for _, id := range order {
		if state[id] == "queued" {
			open = append(open, id)
		}
	}
	return open
}

// A Steer that finds the queue full is refused before message_queued is
// written, whatever the callers race on: no message is queued and then
// rejected.
func TestConcurrentSteersAtTheLimitNeverQueueThenReject(t *testing.T) {
	limits := defaultLimits()
	limits.MaxSteerMessages = 1
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}, started: make(chan int, 8), gate: make(chan struct{}, 8)}
	session, err := NewSession(context.Background(), WithModel(model), WithTools(&scriptedTool{}), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	seen := recordAll(t, session)
	done := make(chan error, 1)
	go func() {
		_, runErr := session.Prompt(context.Background(), UserMessage("go"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = session.Steer(context.Background(), UserMessage("x"))
		}()
	}
	wg.Wait()
	model.gate <- struct{}{}
	model.gate <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if open := checkLedger(t, seen.events()); len(open) != 0 {
		t.Fatalf("messages left open: %v", open)
	}
}
