package gotato

import (
	"context"
	"errors"
	"fmt"
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

// failSteerJournal refuses the write of a committed Steer, as a Journal that
// fails after the Transcript took the message.
type failSteerJournal struct{ journalTranscript }

func (j *failSteerJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	for _, event := range events {
		if committed, ok := event.(MessageCommitted); ok && committed.Source == MessageSourceSteer {
			return nil, errors.New("journal down")
		}
	}
	return j.journalTranscript.Write(ctx, events)
}

// dropsOf returns the why of each message_dropped by message text.
func dropWhys(events []Event, texts map[MessageID]string) map[string]string {
	out := map[string]string{}
	for _, event := range events {
		if dropped, ok := event.(MessageDropped); ok {
			out[texts[dropped.MessageID]] = dropped.Why
		}
	}
	return out
}

// A Run that fails while it commits what it took from the queue loses none of
// it: what it committed is committed, the message that failed is dropped with
// commit_failed, and the rest waits: for the host under handoff, else it is
// dropped when the Run ends.
func TestAFailedCommitSettlesEveryTakenMessage(t *testing.T) {
	const steers = 3
	for _, handoff := range []bool{false, true} {
		for fits := range steers {
			t.Run(fmt.Sprintf("handoff=%v/commits=%d", handoff, fits), func(t *testing.T) {
				limits := defaultLimits()
				// The prompt, the tool call and its result are three messages.
				limits.MaxMessages = uint32(3 + fits)
				model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}, started: make(chan int, 8), gate: make(chan struct{}, 8)}
				options := []SessionOption{WithModel(model), WithTools(&scriptedTool{}), WithLimits(limits)}
				if handoff {
					options = append(options, WithQueueHandoff())
				}
				session, err := NewSession(context.Background(), options...)
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
				texts := map[MessageID]string{}
				var ids []MessageID
				for i := range steers {
					message := UserMessage(fmt.Sprintf("s%d", i))
					message.ID = MessageID(fmt.Sprintf("steer-%d", i))
					texts[message.ID] = fmt.Sprintf("s%d", i)
					ids = append(ids, message.ID)
					if err := session.Steer(context.Background(), message); err != nil {
						t.Fatal(err)
					}
				}
				model.gate <- struct{}{}
				if runErr := <-done; !IsCode(runErr, CodeLimitExceeded) {
					t.Fatalf("Run error = %v, want limit_exceeded", runErr)
				}
				failed, rest := ids[fits], ids[fits+1:]
				var left []Queued
				if handoff {
					left = session.TakeQueued()
					if len(left) != len(rest) {
						t.Fatalf("TakeQueued = %d messages, want the %d that were not committed", len(left), len(rest))
					}
					for i, queued := range left {
						if queued.Message.ID != rest[i] {
							t.Fatalf("TakeQueued[%d] = %s, want %s: the order is lost", i, queued.Message.ID, rest[i])
						}
					}
					session.DropQueued(left, "run_failed")
				}
				if err := session.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				events := seen.events()
				if open := checkLedger(t, events); len(open) != 0 {
					t.Fatalf("messages left open: %v", open)
				}
				whys := dropWhys(events, texts)
				if got := whys[texts[failed]]; got != "commit_failed" {
					t.Fatalf("the message that failed was dropped with %q, want commit_failed: %v", got, whys)
				}
				wantRest := "run_ended"
				if handoff {
					wantRest = "run_failed"
				}
				for _, id := range rest {
					if got := whys[texts[id]]; got != wantRest {
						t.Fatalf("%s was dropped with %q, want %q: %v", texts[id], got, wantRest, whys)
					}
				}
			})
		}
	}
}

// A notice that cannot be committed at the head of a Turn is dropped, and the
// notices behind it wait for the next Run.
func TestAFailedNoticeCommitKeepsTheNoticesBehindIt(t *testing.T) {
	const notices = 3
	for fits := range notices {
		t.Run(fmt.Sprintf("commits=%d", fits), func(t *testing.T) {
			limits := defaultLimits()
			// The prompt, then the notices that fit.
			limits.MaxMessages = uint32(1 + fits)
			session := newNoticeSession(t, &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}, WithLimits(limits))
			seen := recordAll(t, session)
			texts := map[MessageID]string{}
			for i := range notices {
				message := NewNotice(NoticeSourceService, "k", fmt.Sprintf("n%d", i))
				message.ID = MessageID(fmt.Sprintf("notice-%d", i))
				texts[message.ID] = fmt.Sprintf("n%d", i)
				if _, err := session.QueueNotice(context.Background(), message); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := session.Prompt(context.Background(), UserMessage("go")); !IsCode(err, CodeLimitExceeded) {
				t.Fatalf("Run error = %v, want limit_exceeded", err)
			}
			waiting := session.TakeQueued()
			if len(waiting) != notices-fits-1 {
				t.Fatalf("%d notices wait, want %d", len(waiting), notices-fits-1)
			}
			session.DropQueued(waiting, "run_ended")
			if err := session.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			events := seen.events()
			if open := checkLedger(t, events); len(open) != 0 {
				t.Fatalf("notices left open: %v", open)
			}
			if got := dropWhys(events, texts)[fmt.Sprintf("n%d", fits)]; got != "commit_failed" {
				t.Fatalf("the notice that failed was dropped with %q, want commit_failed", got)
			}
		})
	}
}

// A message the Transcript took is committed, even when its Event could not be
// written: it never goes back to the queue, or the next Run commits it twice.
func TestACommittedMessageIsNeverPutBack(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{toolCallScript(), finalScript("done")}, started: make(chan int, 8), gate: make(chan struct{}, 8)}
	session, err := NewSession(context.Background(), WithModel(model), WithTools(&scriptedTool{}), WithQueueHandoff(), WithTranscript(&failSteerJournal{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, runErr := session.Prompt(context.Background(), UserMessage("go"))
		done <- runErr
	}()
	waitForCall(t, model.started, 0)
	for _, text := range []string{"s0", "s1"} {
		if err := session.Steer(context.Background(), UserMessage(text)); err != nil {
			t.Fatal(err)
		}
	}
	model.gate <- struct{}{}
	if runErr := <-done; runErr == nil {
		t.Fatal("the Run did not fail")
	}
	left := session.TakeQueued()
	if len(left) != 1 || TextOf(left[0].Message) != "s1" {
		t.Fatalf("TakeQueued = %+v, want only the message the Transcript did not take", left)
	}
}
