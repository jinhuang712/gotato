package gotato

import (
	"context"
	"sync"
	"testing"
)

// A caller may poll Session.Transcript while a Run appends to it.
func TestSessionTranscriptIsSafeToReadDuringARun(t *testing.T) {
	session, err := NewSession(context.Background(), WithModel(echoToolModel("call-1")), WithTools(&scriptedTool{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = session.Transcript().Len()
				_ = session.Transcript().Messages()
			}
		}
	}()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	// The slice a caller gets is its own.
	messages := session.Transcript().Messages()
	messages[0].ID = "mutated"
	if session.Transcript().Messages()[0].ID == "mutated" {
		t.Fatal("Messages returned the live slice")
	}
}
