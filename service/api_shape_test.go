package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// runIDOf returns the ID of the Session's last started Run, read from its
// stored Events.
func runIDOf(t *testing.T, runner *service.Runner, sessionID string) gotato.RunID {
	t.Helper()
	events, err := runner.Events(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var id gotato.RunID
	for _, event := range events {
		if started, ok := event.(gotato.RunStarted); ok {
			id = started.RunID
		}
	}
	if id == "" {
		t.Fatal("no run_started in the Session's Events")
	}
	return id
}

// An abort that names a Run stops that Run only. A repeat of an abort whose
// answer was lost, with the ID of the Run it meant, finds that Run over and
// leaves the Run that follows alone.
func TestAbortSessionNamesARun(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("first"), testkit.Text("second"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}

	// The first Run is over; the next one is going.
	block := make(chan struct{})
	model.Block = block
	result := make(chan service.RunResponse, 1)
	go func() {
		response, _ := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "two"})
		result <- response
	}()
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the second Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	second := runIDOf(t, a, s.ID())
	if second == first.Result.RunID {
		t.Fatalf("both Runs have the ID %q", second)
	}

	// The late abort of the first Run: it names a Run that is over.
	if err := a.AbortSession(ctx, s.ID(), service.AbortRun(first.Result.RunID)); !errors.Is(err, service.ErrNoRun) {
		t.Fatalf("AbortSession of a Run that is over = %v, want ErrNoRun", err)
	}
	select {
	case response := <-result:
		t.Fatalf("the abort of another Run stopped the second Run: %+v", response.Result)
	case <-time.After(100 * time.Millisecond):
	}
	// Naming the Run that is going stops it.
	if err := a.AbortSession(ctx, s.ID(), service.AbortRun(second)); err != nil {
		t.Fatalf("AbortSession of the Run going = %v", err)
	}
	select {
	case response := <-result:
		if response.Result.Status != gotato.RunStatusCanceled {
			t.Fatalf("the aborted Run settled as %q, want canceled", response.Result.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the named abort did not stop the Run")
	}
	close(block)
}

// A request ID is recorded in run_started and in the Run's record, and
// echoed in the response, for a Run and for a message that starts one.
func TestRequestIDIsRecordedAndEchoed(t *testing.T) {
	ctx := context.Background()
	a := instanceRunner(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, steadyLiveness)
	response, err := a.Run(ctx, service.RunRequest{Prompt: "hi", RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "req-1" {
		t.Fatalf("RunResponse.RequestID = %q, want req-1", response.RequestID)
	}
	sent, err := a.Send(ctx, service.MessageRequest{SessionID: response.SessionID, Text: "again", RequestID: "req-2"})
	if err != nil {
		t.Fatal(err)
	}
	if sent.RequestID != "req-2" || sent.Run == nil || sent.Run.RequestID != "req-2" {
		t.Fatalf("Send result = %+v, want req-2 on the result and on the Run it started", sent)
	}
	events, err := a.Events(ctx, response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for _, event := range events {
		if started, ok := event.(gotato.RunStarted); ok {
			recorded = append(recorded, started.RequestID)
		}
	}
	if len(recorded) != 2 || recorded[0] != "req-1" || recorded[1] != "req-2" {
		t.Fatalf("run_started request IDs = %v, want [req-1 req-2]", recorded)
	}
	record, err := a.Record(ctx, response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runs := record.Runs()
	if len(runs) != 2 || runs[0].RequestID != "req-1" || runs[1].RequestID != "req-2" {
		t.Fatalf("Runs = %+v, want request IDs req-1 and req-2", runs)
	}
}

// A Session ID that is taken has a code of its own.
func TestSessionExistsHasItsOwnCode(t *testing.T) {
	ctx := context.Background()
	a := instanceRunner(t, session.NewMemoryStore(), "instance-a", testkit.EchoModel{}, steadyLiveness)
	if _, err := a.CreateSessionExclusive(ctx, "echo", "taken", nil); err != nil {
		t.Fatal(err)
	}
	_, err := a.CreateSessionExclusive(ctx, "echo", "taken", nil)
	if !errors.Is(err, service.ErrSessionExists) || !gotato.IsCode(err, gotato.CodeAlreadyExists) {
		t.Fatalf("second create = %v, want ErrSessionExists with code already_exists", err)
	}
	if gotato.IsCode(err, gotato.CodeInvalidState) {
		t.Fatalf("second create = %v: a taken ID is no longer invalid_state", err)
	}
	if gotato.IsCode(service.ErrNoRun, gotato.CodeAlreadyExists) {
		t.Fatal("ErrNoRun has the code of a taken ID")
	}
}
