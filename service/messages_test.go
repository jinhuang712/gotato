package service_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// stagedModel answers call n with replies[n] once release[n] is closed, and
// says which call started on started. With hang set, call 0 instead streams
// "partial" and waits for its context, so there is a model call in flight to
// interrupt or stop.
type stagedModel struct {
	hang    bool
	started chan int
	release []chan struct{}
	replies []string

	mu       sync.Mutex
	calls    int
	requests [][]gotato.Message
}

func newStagedModel(hang bool, replies ...string) *stagedModel {
	m := &stagedModel{hang: hang, started: make(chan int, 16), replies: replies}
	for range replies {
		m.release = append(m.release, make(chan struct{}))
	}
	return m
}

type hangingStream struct{ sent bool }

func (s *hangingStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if !s.sent {
		s.sent = true
		return gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: "partial"}, nil
	}
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (s *hangingStream) Close() error { return nil }

func (m *stagedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	index := m.calls
	m.calls++
	m.requests = append(m.requests, append([]gotato.Message(nil), request.Messages...))
	m.mu.Unlock()
	m.started <- index
	if m.hang && index == 0 {
		return &hangingStream{}, nil
	}
	select {
	case <-m.release[index]:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return testkit.NewFakeModel(testkit.Text(m.replies[index])).Stream(ctx, request)
}

func (m *stagedModel) request(call int) []gotato.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[call]
}

func waitCall(t *testing.T, m *stagedModel, want int) {
	t.Helper()
	select {
	case got := <-m.started:
		if got != want {
			t.Fatalf("model call %d started, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("model call %d never started", want)
	}
}

func texts(messages []gotato.Message) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, string(m.Role)+":"+gotato.TextOf(m))
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// startRun runs a prompt on the Session in the background.
func startRun(t *testing.T, runner *service.Runner, id, prompt string) chan service.RunResult {
	t.Helper()
	done := make(chan service.RunResult, 1)
	go func() {
		result, err := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- result
	}()
	return done
}

func settled(t *testing.T, done chan service.RunResult) service.RunResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the Run did not settle")
		return service.RunResult{}
	}
}

func transcriptOf(t *testing.T, runner *service.Runner, id string) []string {
	t.Helper()
	record, err := runner.Session(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return texts(record.Messages())
}

// kindsOf lists the message Events of a Session in order, as "kind:source".
func messageKinds(t *testing.T, runner *service.Runner, id string) []string {
	t.Helper()
	events, err := runner.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, event := range events {
		switch e := event.(type) {
		case gotato.MessageQueued:
			out = append(out, "queued:"+string(e.Source))
		case gotato.MessageCommitted:
			if e.Source != gotato.SourcePrompt {
				out = append(out, "committed:"+string(e.Source))
			}
		case gotato.MessageDropped:
			out = append(out, "dropped:"+string(e.Source))
		case gotato.AbortRequested:
			out = append(out, "abort")
		}
	}
	return out
}

func messageRunner(t *testing.T, store session.Store, instance, address string, model gotato.Model) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: instance, Address: address, Liveness: steadyLiveness, Admission: service.Admission{Queue: service.WaitWhileBusy},
		Agents: []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

func TestSteerReachesTheRunAtItsNextTurn(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(false, "first", "second")
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	done := startRun(t, runner, s.ID(), "hello")
	waitCall(t, model, 0)
	result, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: service.MessageSteer, Text: "prefer metric"})
	if err != nil || !result.Queued || result.MessageID == "" || result.Run != nil {
		t.Fatalf("Send = %+v, %v; want queued with an ID", result, err)
	}
	close(model.release[0])
	waitCall(t, model, 1)
	// The steer reached the second call, and was not in the first.
	if got := texts(model.request(1)); !equal(got, []string{"user:hello", "assistant:first", "user:prefer metric"}) {
		t.Fatalf("second request = %v", got)
	}
	close(model.release[1])
	settled(t, done)

	if got := transcriptOf(t, runner, s.ID()); !equal(got, []string{"user:hello", "assistant:first", "user:prefer metric", "assistant:second"}) {
		t.Fatalf("transcript = %v", got)
	}
	if got := messageKinds(t, runner, s.ID()); !equal(got, []string{"queued:steer", "committed:steer"}) {
		t.Fatalf("message events = %v", got)
	}
}

func TestFollowUpKeepsTheRunGoingWhereItWouldSettle(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(false, "first", "second")
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	done := startRun(t, runner, s.ID(), "hello")
	waitCall(t, model, 0)
	if _, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: service.MessageFollowUp, Text: "and then summarize"}); err != nil {
		t.Fatal(err)
	}
	close(model.release[0])
	waitCall(t, model, 1)
	close(model.release[1])
	result := settled(t, done)
	if result.Result.Status != gotato.RunCompleted || result.FinalText != "second" {
		t.Fatalf("result = %+v", result)
	}
	if got := transcriptOf(t, runner, s.ID()); !equal(got, []string{"user:hello", "assistant:first", "user:and then summarize", "assistant:second"}) {
		t.Fatalf("transcript = %v", got)
	}
	if got := messageKinds(t, runner, s.ID()); !equal(got, []string{"queued:follow_up", "committed:follow_up"}) {
		t.Fatalf("message events = %v", got)
	}
}

func TestInterruptCutsTheModelCallAndStartsTheNextTurn(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(true, "", "B done")
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	done := startRun(t, runner, s.ID(), "do A")
	waitCall(t, model, 0)
	if _, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: service.MessageInterrupt, Text: "do B instead"}); err != nil {
		t.Fatal(err)
	}
	waitCall(t, model, 1)
	close(model.release[1])
	result := settled(t, done)
	if result.Result.Status != gotato.RunCompleted || result.FinalText != "B done" {
		t.Fatalf("result = %+v", result)
	}
	// The Transcript keeps the cut-off reply; the model does not read it.
	if got := transcriptOf(t, runner, s.ID()); !equal(got, []string{"user:do A", "assistant:partial", "user:do B instead", "assistant:B done"}) {
		t.Fatalf("transcript = %v", got)
	}
	if got := texts(model.request(1)); !equal(got, []string{"user:do A", "user:do B instead"}) {
		t.Fatalf("second request = %v", got)
	}
	if got := messageKinds(t, runner, s.ID()); !equal(got, []string{"queued:interrupt", "committed:interrupt"}) {
		t.Fatalf("message events = %v", got)
	}
}

func TestStopCancelsTheRunAndKeepsWhatItCommitted(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(true)
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	done := make(chan service.RunResult, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "do A"})
		done <- result
	}()
	waitCall(t, model, 0)
	if err := runner.CancelSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	result := settled(t, done)
	if result.Result.Status != gotato.RunCanceled {
		t.Fatalf("status = %q, want canceled", result.Result.Status)
	}
	if got := transcriptOf(t, runner, s.ID()); !equal(got, []string{"user:do A", "assistant:partial"}) {
		t.Fatalf("transcript = %v", got)
	}
	if got := messageKinds(t, runner, s.ID()); !equal(got, []string{"abort"}) {
		t.Fatalf("message events = %v", got)
	}
	// With nothing running, a Stop finds nothing to stop.
	if err := runner.CancelSession(ctx, s.ID()); !errors.Is(err, service.ErrNoRun) {
		t.Fatalf("Stop with no Run = %v", err)
	}
}

func TestAMessageAfterTheRunStartsANewRun(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(false, "first", "second")
	close(model.release[0])
	close(model.release[1])
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []service.MessageKind{service.MessageFollowUp} {
		result, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: kind, Text: "one more thing"})
		if err != nil || result.Queued || result.Run == nil || result.Run.FinalText != "second" {
			t.Fatalf("Send = %+v, %v; want a new Run", result, err)
		}
	}
	if got := transcriptOf(t, runner, s.ID()); !equal(got, []string{"user:hello", "assistant:first", "user:one more thing", "assistant:second"}) {
		t.Fatalf("transcript = %v", got)
	}
}

func TestSendRefusesWhatItCannotUse(t *testing.T) {
	ctx := context.Background()
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", testkit.EchoModel{})
	s, _ := runner.CreateSession(ctx, "echo", nil)
	for name, request := range map[string]service.MessageRequest{
		"no session": {Text: "hi"},
		"no text":    {SessionID: s.ID(), Text: "  "},
		"bad kind":   {SessionID: s.ID(), Kind: "shout", Text: "hi"},
	} {
		_, err := runner.Send(ctx, request)
		if !gotato.IsCode(err, gotato.ErrInvalidArgument) || !gotato.NotRun(err) {
			t.Fatalf("%s: Send = %v, want invalid_argument, not run", name, err)
		}
	}
	if _, err := runner.Send(ctx, service.MessageRequest{SessionID: "missing", Text: "hi"}); !gotato.IsCode(err, gotato.ErrNotFound) {
		t.Fatalf("Send to a missing Session = %v, want not_found", err)
	}
}

func TestAMessageForARunOnAnotherInstanceIsNotWriter(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := newStagedModel(false, "first", "taken over")
	a := messageRunner(t, store, "instance-a", "http://instance-a.example:8080", model)
	b := messageRunner(t, store, "instance-b", "http://instance-b.example:8080", testkit.EchoModel{})
	s, _ := a.CreateSession(ctx, "echo", nil)

	done := startRun(t, a, s.ID(), "hello")
	waitCall(t, model, 0)

	for _, kind := range []service.MessageKind{service.MessageSteer, service.MessageFollowUp, service.MessageInterrupt} {
		result, err := b.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: kind, Text: "from b"})
		if !gotato.IsCode(err, gotato.ErrNotWriter) || !gotato.NotRun(err) || !gotato.Retryable(err) {
			t.Fatalf("%s: Send = %+v, %v; want not_writer, not run, retryable", kind, result, err)
		}
		if address, ok := service.WriterOf(err); !ok || address != "http://instance-a.example:8080" {
			t.Fatalf("%s: writer = %q, %v; want instance-a's address", kind, address, ok)
		}
	}
	// A Stop is a message too.
	if err := b.CancelSession(ctx, s.ID()); !gotato.IsCode(err, gotato.ErrNotWriter) {
		t.Fatalf("CancelSession on the other instance = %v, want not_writer", err)
	}

	// The Run is untouched, and the writer takes the message itself.
	if _, err := a.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: service.MessageSteer, Text: "from a"}); err != nil {
		t.Fatal(err)
	}
	close(model.release[0])
	waitCall(t, model, 1)
	close(model.release[1])
	settled(t, done)
	want := []string{"user:hello", "assistant:first", "user:from a", "assistant:taken over"}
	if got := transcriptOf(t, a, s.ID()); !equal(got, want) {
		t.Fatalf("transcript = %v, want %v", got, want)
	}

	// With no Run going, the other instance takes the Session over and runs.
	result, err := b.Send(ctx, service.MessageRequest{SessionID: s.ID(), Text: "now from b"})
	if err != nil || result.Queued || result.Run == nil || result.Run.Result.Status != gotato.RunCompleted {
		t.Fatalf("Send with no Run going = %+v, %v; want a new Run on b", result, err)
	}
	view, _ := b.Session(ctx, s.ID())
	if claim := view.Claim(); !isInstance(claim.Instance, "instance-b") {
		t.Fatalf("claim = %+v, want instance-b", claim)
	}
}

func TestNotWriterWithoutAnAdvertisedAddress(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := newStagedModel(false, "first")
	a := messageRunner(t, store, "instance-a", "", model)
	b := messageRunner(t, store, "instance-b", "", testkit.EchoModel{})
	s, _ := a.CreateSession(ctx, "echo", nil)
	done := startRun(t, a, s.ID(), "hello")
	waitCall(t, model, 0)

	sub, subErr := b.SubscribeInstance(ctx)
	if subErr != nil {
		t.Fatal(subErr)
	}
	seen := testkit.RecordSubscription(sub)
	_, err := b.Send(ctx, service.MessageRequest{SessionID: s.ID(), Text: "from b"})
	// The refusal is an Instance Event of the instance that refused.
	if !seen.Wait(5*time.Second, func(event gotato.Event) bool {
		rejected, ok := event.(gotato.RunRejected)
		return ok && rejected.Why == "not_writer"
	}) {
		t.Fatalf("no run_rejected with why not_writer; got %v", seen.Kinds())
	}
	address, ok := service.WriterOf(err)
	if !gotato.IsCode(err, gotato.ErrNotWriter) || !ok || address != "" {
		t.Fatalf("Send = %v (writer %q, %v); want not_writer without an address", err, address, ok)
	}
	close(model.release[0])
	settled(t, done)
}

func TestMessageEventsReachTheInstanceStream(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(false, "first", "second")
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)
	sub, err := runner.SubscribeInstance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := testkit.RecordSubscription(sub)

	done := startRun(t, runner, s.ID(), "hello")
	waitCall(t, model, 0)
	if _, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Text: "steer"}); err != nil {
		t.Fatal(err)
	}
	close(model.release[0])
	waitCall(t, model, 1)
	close(model.release[1])
	settled(t, done)

	for _, kind := range []gotato.EventKind{gotato.KindMessageQueued, gotato.KindMessageCommitted} {
		// The Instance stream is delivered after the Run settles.
		seen.Wait(5*time.Second, func(event gotato.Event) bool { return event.Kind() == kind })
		if len(seen.OfKind(kind)) == 0 {
			t.Fatalf("the Instance stream carried no %s; got %v", kind, seen.Kinds())
		}
	}
}

// A message that a Run's live Session accepted is never lost without a word:
// each ends as committed or dropped, also when it arrives as the Run ends.
func TestEveryQueuedMessageIsCommittedOrDropped(t *testing.T) {
	ctx := context.Background()
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", testkit.EchoModel{})
	for i := range 30 {
		s, _ := runner.CreateSession(ctx, "echo", nil)
		done := startRun(t, runner, s.ID(), "hello")
		var senders sync.WaitGroup
		for range 4 {
			senders.Add(1)
			go func() {
				defer senders.Done()
				// A message sent after the Run ended starts a Run of its own.
				if _, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Text: "steer"}); err != nil && !gotato.IsCode(err, gotato.ErrBusy) {
					t.Errorf("Send: %v", err)
				}
			}()
		}
		senders.Wait()
		settled(t, done)
		// A message queued as the Run settled runs in a Run that follows it.
		for deadline := time.Now().Add(5 * time.Second); runner.ActiveRuns() > 0; runtime.Gosched() {
			if time.Now().After(deadline) {
				t.Fatal("the Runs did not end")
			}
		}
		events, err := runner.Events(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		ended := map[gotato.MessageID]bool{}
		var queued []gotato.MessageID
		for _, event := range events {
			switch e := event.(type) {
			case gotato.MessageQueued:
				queued = append(queued, e.MessageID)
			case gotato.MessageCommitted:
				ended[e.Message.ID] = true
			case gotato.MessageDropped:
				ended[e.MessageID] = true
			}
		}
		for _, id := range queued {
			if !ended[id] {
				t.Fatalf("iteration %d: message %s was queued and neither committed nor dropped", i, id)
			}
		}
	}
}
