package gotato

import (
	"context"
	"testing"
	"time"
)

// stalledJournal never finishes a write of a queued message until its ctx ends.
type stalledJournal struct{ journalTranscript }

func (j *stalledJournal) Write(ctx context.Context, events []Event) ([]Event, error) {
	for _, event := range events {
		if event.Kind() == EventKindMessageQueued {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return j.journalTranscript.Write(ctx, events)
}

// Steer, FollowUp and Interrupt honor their ctx in the store write: a stalled
// store does not hold the caller past its deadline.
func TestSteerHonorsItsContext(t *testing.T) {
	session, err := NewSession(context.Background(), WithModel(&testModel{}), WithTranscript(&stalledJournal{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Steer(ended, UserMessage("late")); !IsCode(err, CodeCanceled) || !NotRun(err) {
		t.Fatalf("Steer with an ended ctx = %v, want canceled, NotRun", err)
	}

	ctx, cancelTimeout := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelTimeout()
	started := time.Now()
	err = session.FollowUp(ctx, UserMessage("stalled"))
	if !IsCode(err, CodeDeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("FollowUp over a stalled store = %v after %v, want deadline_exceeded", err, time.Since(started))
	}
}
