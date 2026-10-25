package service_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// holdAtTheEnd holds a Run after it has settled and before its live Session
// stops taking messages, once: the last moment a message is accepted as queued
// by a Run that no longer takes any.
type holdAtTheEnd struct {
	reached chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func newHold() *holdAtTheEnd {
	return &holdAtTheEnd{reached: make(chan struct{}), proceed: make(chan struct{})}
}

func (h *holdAtTheEnd) hook() {
	h.once.Do(func() {
		close(h.reached)
		<-h.proceed
	})
}

type handoffRig struct {
	runner *service.Runner
	hold   *holdAtTheEnd
	id     string
	ended  *testkit.EventRecorder
	done   chan service.RunResult
}

// startHeld starts a Run on a Runner with the default busy policy (reject),
// and returns when it has settled and is held.
func startHeld(t *testing.T) *handoffRig {
	t.Helper()
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	rig := &handoffRig{runner: runner, hold: newHold(), done: make(chan service.RunResult, 1)}
	runner.SetBeforeDetach(rig.hold.hook)
	s, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	rig.id = s.ID()
	sub, err := runner.SubscribeInstance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rig.ended = testkit.RecordSubscription(sub)
	go func() {
		result, _ := runner.Run(context.Background(), service.RunRequest{SessionID: rig.id, Prompt: "hello"})
		rig.done <- result
	}()
	select {
	case <-rig.hold.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never settled")
	}
	return rig
}

// release lets the held Run go and returns its result.
func (r *handoffRig) release(t *testing.T) service.RunResult {
	t.Helper()
	close(r.hold.proceed)
	select {
	case result := <-r.done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the held Run did not return")
		return service.RunResult{}
	}
}

// waitRuns waits until n Runs have ended on the Instance.
func (r *handoffRig) waitRuns(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.ended.OfKind(gotato.KindRunEnded)) < n {
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d Runs ended; got %v", n, r.ended.Kinds())
		}
		runtime.Gosched()
	}
}

func (r *handoffRig) send(t *testing.T, kind service.MessageKind, text string) gotato.MessageID {
	t.Helper()
	result, err := r.runner.Send(context.Background(), service.MessageRequest{SessionID: r.id, Kind: kind, Text: text})
	if err != nil || !result.Queued {
		t.Fatalf("Send(%s) = %+v, %v; want queued", text, result, err)
	}
	return gotato.MessageID(result.MessageID)
}

func (r *handoffRig) events(t *testing.T) (committed []gotato.MessageID, dropped map[gotato.MessageID]string, runs int) {
	t.Helper()
	events, err := r.runner.Events(context.Background(), r.id)
	if err != nil {
		t.Fatal(err)
	}
	dropped = map[gotato.MessageID]string{}
	for _, event := range events {
		switch e := event.(type) {
		case gotato.MessageCommitted:
			committed = append(committed, e.Message.ID)
		case gotato.MessageDropped:
			dropped[e.MessageID] = e.Why
		case gotato.RunStarted:
			runs++
		}
	}
	return committed, dropped, runs
}

// A message the Runner accepted as queued after the Run's last point of taking
// one runs in a Run that follows, on the same Session.
func TestAMessageAcceptedAtTheEndRunsInAFollowingRun(t *testing.T) {
	rig := startHeld(t)
	id := rig.send(t, service.MessageSteer, "late")
	first := rig.release(t)
	if first.FinalText != "echo: hello" {
		t.Fatalf("the first Run answered %q; its own result is unchanged", first.FinalText)
	}
	rig.waitRuns(t, 2)

	if got := transcriptOf(t, rig.runner, rig.id); !equal(got, []string{"user:hello", "assistant:echo: hello", "user:late", "assistant:echo: late"}) {
		t.Fatalf("transcript = %v", got)
	}
	committed, dropped, runs := rig.events(t)
	if runs != 2 || len(dropped) != 0 {
		t.Fatalf("%d Runs, dropped %v; want 2 Runs and nothing dropped", runs, dropped)
	}
	found := false
	for _, committedID := range committed {
		found = found || committedID == id
	}
	if !found {
		t.Fatalf("message %s the caller was told was queued was never committed (%v)", id, committed)
	}
}

// Several leftovers keep the order they were accepted in, a Steer and a
// FollowUp alike, and run in one Run.
func TestLeftoverMessagesKeepTheirOrder(t *testing.T) {
	rig := startHeld(t)
	ids := []gotato.MessageID{
		rig.send(t, service.MessageSteer, "one"),
		rig.send(t, service.MessageFollowUp, "two"),
		rig.send(t, service.MessageSteer, "three"),
		rig.send(t, service.MessageFollowUp, "four"),
	}
	rig.release(t)
	rig.waitRuns(t, 2)

	want := []string{"user:hello", "assistant:echo: hello", "user:one", "assistant:echo: one", "user:two", "user:three", "user:four", "assistant:echo: four"}
	if got := transcriptOf(t, rig.runner, rig.id); !equal(got, want) {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
	committed, dropped, runs := rig.events(t)
	if runs != 2 || len(dropped) != 0 {
		t.Fatalf("%d Runs, dropped %v", runs, dropped)
	}
	// The committed IDs after the first Run's prompt are the accepted IDs, in order.
	if len(committed) != 5 || committed[1] != ids[0] || committed[2] != ids[1] || committed[3] != ids[2] || committed[4] != ids[3] {
		t.Fatalf("committed %v, want the accepted %v in order after the first prompt", committed, ids)
	}
}

// A drain does not start a Run for what is left: the message is dropped, and
// the Event says why.
func TestADrainDropsTheLeftoversWithTheReason(t *testing.T) {
	rig := startHeld(t)
	id := rig.send(t, service.MessageSteer, "late")
	drained := make(chan error, 1)
	go func() { drained <- rig.runner.Drain(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for !rig.runner.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("the drain never began")
		}
		runtime.Gosched()
	}
	rig.release(t)
	if err := <-drained; err != nil {
		t.Fatal(err)
	}

	_, dropped, runs := rig.events(t)
	if runs != 1 || dropped[id] != "draining" {
		t.Fatalf("%d Runs, dropped %v; want 1 Run and the message dropped for draining", runs, dropped)
	}
	if got := transcriptOf(t, rig.runner, rig.id); !equal(got, []string{"user:hello", "assistant:echo: hello"}) {
		t.Fatalf("transcript = %v", got)
	}
}

// A Stop that reaches the Run ends the work the leftovers were meant for.
func TestAStopDropsTheLeftovers(t *testing.T) {
	rig := startHeld(t)
	id := rig.send(t, service.MessageFollowUp, "late")
	if err := rig.runner.CancelSession(context.Background(), rig.id); err != nil {
		t.Fatal(err)
	}
	rig.release(t)

	_, dropped, runs := rig.events(t)
	if runs != 1 || dropped[id] != "run_ended" {
		t.Fatalf("%d Runs, dropped %v; want 1 Run and the message dropped for run_ended", runs, dropped)
	}
}
