package gotato_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// A Steer, FollowUp or Notice over MaxMessageBytes is refused where it
// arrives, with message_rejected (too_large), and the Run it was meant for
// still completes.
func TestOversizedControlMessageIsRefusedAtTheDoor(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		once.Do(func() { close(started) })
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testkit.Text("the answer"), nil
	})
	j := newHoldJournal(nil)
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(j), gotato.WithLimits(gotato.CoreLimits{MaxMessageBytes: 1024}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	done := make(chan gotato.RunResult, 1)
	go func() {
		r, _ := s.Prompt(context.Background(), gotato.UserMessage("hi"))
		done <- r
	}()
	waitFor(t, started, "model call started")
	big := gotato.UserMessage(strings.Repeat("x", 4096))
	for name, err := range map[string]error{
		"Steer":     s.Steer(context.Background(), big),
		"FollowUp":  s.FollowUp(context.Background(), big),
		"Interrupt": s.Interrupt(context.Background(), big),
	} {
		if !gotato.IsCode(err, gotato.CodeLimitExceeded) {
			t.Errorf("%s of 4096 bytes = %v, want limit_exceeded", name, err)
		}
	}
	if _, err := s.Notice(context.Background(), "k", strings.Repeat("x", 4096)); !gotato.IsCode(err, gotato.CodeLimitExceeded) {
		t.Errorf("Notice of 4096 bytes = %v, want limit_exceeded", err)
	}
	close(gate)
	if r := <-done; r.Status != gotato.RunStatusCompleted {
		t.Fatalf("status %s, error %v", r.Status, r.Error)
	}
	if !j.rejected("too_large") {
		t.Fatal("no message_rejected (too_large)")
	}
}

func (j *holdJournal) rejected(why string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.events {
		if d, ok := e.(gotato.MessageRejected); ok && d.Why == why {
			return true
		}
	}
	return false
}
