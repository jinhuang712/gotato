package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A message answered "queued" is in the Record as message_queued. When the
// writer that took it dies before it commits, the Instance that takes the
// Session over must say what became of it: message_dropped with the reason
// writer_gone, so the Record never keeps an accepted message that nothing
// resolves.
func TestQueuedMessageOfADeadWriterIsDroppedOnTakeover(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	sent, err := a.Send(ctx, service.MessageRequest{SessionID: s.ID(), Text: "steer"})
	if err != nil || !sent.Queued {
		t.Fatalf("Send = %+v, %v; want queued", sent, err)
	}

	// The writer dies: its row is gone, and its Run writes nothing more.
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	close(block)
	<-done

	events, err := b.Events(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	var queued, resolved bool
	for _, event := range events {
		switch e := event.(type) {
		case gotato.MessageQueued:
			queued = queued || string(e.MessageID) == sent.MessageID
		case gotato.MessageCommitted:
			resolved = resolved || string(e.Message.ID) == sent.MessageID
		case gotato.MessageDropped:
			if string(e.MessageID) == sent.MessageID {
				resolved = true
				if e.Why != "writer_gone" {
					t.Fatalf("message_dropped why = %q, want writer_gone", e.Why)
				}
			}
		}
	}
	if !queued {
		t.Fatal("the Record has no message_queued for the 202 the sender holds")
	}
	if !resolved {
		t.Fatal("the message_queued of a dead writer is never resolved: no message_committed, no message_dropped")
	}
}
