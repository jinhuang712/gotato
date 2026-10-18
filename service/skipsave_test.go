package service_test

import (
	"context"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// A Run that is not saved leaves no Event in the Record, so its stream does not
// number its Events as Record positions: it numbers them from 1, on its own
// stream, and the next saved Run goes on from the Record's head.
func TestSkipSaveRunDoesNotUseRecordNumbers(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := runner.Events(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	head := before[len(before)-1].Head().Number

	var skipped []gotato.Event
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two", SkipSave: true}, func(e gotato.Event) error {
		skipped = append(skipped, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stored := storedOnly(skipped)
	if len(stored) == 0 || stored[0].Head().Number != 1 {
		t.Fatalf("the unsaved Run's first stored Event is numbered %v, want 1: its numbers are not Record positions", numbered(stored))
	}
	if after, err := session.StoredEvents(ctx, store, first.SessionID, head); err != nil || len(after) != 0 {
		t.Fatalf("the unsaved Run wrote %d Events (%v)", len(after), err)
	}

	var saved []gotato.Event
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "three"}, func(e gotato.Event) error {
		saved = append(saved, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := storedOnly(saved)[0].Head().Number; got != head+1 {
		t.Fatalf("the next saved Run starts at %d, want %d, after the Record's head", got, head+1)
	}
}
