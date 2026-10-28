package service_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
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
	done   chan service.RunResponse
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
	rig := &handoffRig{runner: runner, hold: newHold(), done: make(chan service.RunResponse, 1)}
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
func (r *handoffRig) release(t *testing.T) service.RunResponse {
	t.Helper()
	close(r.hold.proceed)
	select {
	case result := <-r.done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the held Run did not return")
		return service.RunResponse{}
	}
}

// waitRuns waits until n Runs have ended on the Instance.
func (r *handoffRig) waitRuns(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.ended.OfKind(gotato.EventKindRunEnded)) < n {
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
	id := rig.send(t, service.MessageKindSteer, "late")
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
		rig.send(t, service.MessageKindSteer, "one"),
		rig.send(t, service.MessageKindFollowUp, "two"),
		rig.send(t, service.MessageKindSteer, "three"),
		rig.send(t, service.MessageKindFollowUp, "four"),
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
	id := rig.send(t, service.MessageKindSteer, "late")
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
	id := rig.send(t, service.MessageKindFollowUp, "late")
	if err := rig.runner.AbortSession(context.Background(), rig.id); err != nil {
		t.Fatal(err)
	}
	rig.release(t)

	_, dropped, runs := rig.events(t)
	if runs != 1 || dropped[id] != "run_ended" {
		t.Fatalf("%d Runs, dropped %v; want 1 Run and the message dropped for run_ended", runs, dropped)
	}
}

// A message the live Session refuses while it is still attached (an Interrupt
// after the Run settled) makes Send wait for the Session to be withdrawn; it
// does not go round its loop again and again.
func TestSendDoesNotSpinOnALiveSessionThatRefuses(t *testing.T) {
	rig := startHeld(t)
	var rounds atomic.Int64
	rig.runner.SetOnSendRetry(func() { rounds.Add(1) })
	sent := make(chan error, 1)
	go func() {
		_, err := rig.runner.Send(context.Background(), service.MessageRequest{SessionID: rig.id, Kind: service.MessageKindInterrupt, Text: "stop"})
		sent <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if n := rounds.Load(); n > 2 {
		t.Fatalf("Send went round its loop %d times while the Run was held", n)
	}
	rig.release(t)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Send = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after the Run was released")
	}
}

// loadFails is a Store whose Load fails once armed.
type loadFails struct {
	session.Store
	armed atomic.Bool
}

func (s *loadFails) Load(ctx context.Context, id string) (session.Loaded, error) {
	if s.armed.Load() {
		return session.Loaded{}, errors.New("store down")
	}
	return s.Store.Load(ctx, id)
}

// A following Run that cannot open the Session drops each message it was to
// take, with run_failed, on the Instance's stream; nothing can be written to the
// Record. A Run that follows without failing drops nothing (the tests above).
func TestAFollowingRunThatFailsDropsItsMessages(t *testing.T) {
	store := &loadFails{Store: session.NewMemoryStore()}
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	hold := newHold()
	runner.SetBeforeDetach(hold.hook)
	s, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.SubscribeInstance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recorded := testkit.RecordSubscription(sub)
	done := make(chan struct{})
	go func() {
		_, _ = runner.Run(context.Background(), service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
		close(done)
	}()
	<-hold.reached
	var ids []gotato.MessageID
	for _, text := range []string{"one", "two"} {
		result, err := runner.Send(context.Background(), service.MessageRequest{SessionID: s.ID(), Kind: service.MessageKindFollowUp, Text: text})
		if err != nil || !result.Queued {
			t.Fatalf("Send(%s) = %+v, %v; want queued", text, result, err)
		}
		ids = append(ids, gotato.MessageID(result.MessageID))
	}
	store.armed.Store(true)
	close(hold.proceed)
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for len(recorded.OfKind(gotato.EventKindMessageDropped)) < len(ids) {
		if time.Now().After(deadline) {
			t.Fatalf("got %v; want a message_dropped for each of %v", recorded.Kinds(), ids)
		}
		runtime.Gosched()
	}
	for i, event := range recorded.OfKind(gotato.EventKindMessageDropped) {
		dropped := event.(gotato.MessageDropped)
		if dropped.MessageID != ids[i] || dropped.Why != "run_failed" {
			t.Fatalf("dropped %+v; want message %s with why run_failed", dropped, ids[i])
		}
	}
}

// A following Run that opened the Session and then finds its Agent gone writes
// the drops in the Record, where the message_queued Events are, and they reach
// the Session's subscribers too.
func TestAFollowingRunWithoutItsAgentDropsInTheRecord(t *testing.T) {
	rig := startHeld(t)
	id := rig.send(t, service.MessageKindSteer, "late")
	rig.runner.RemoveAgent("echo")
	rig.release(t)

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, dropped, _ := rig.events(t)
		if dropped[id] == "run_failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dropped %v; want message %s dropped for run_failed in the Record", dropped, id)
		}
		runtime.Gosched()
	}
	if got := rig.ended.OfKind(gotato.EventKindMessageDropped); len(got) != 1 {
		t.Fatalf("the Instance stream has %d message_dropped, want 1", len(got))
	}
	if got := transcriptOf(t, rig.runner, rig.id); !equal(got, []string{"user:hello", "assistant:echo: hello"}) {
		t.Fatalf("transcript = %v", got)
	}
}

// holdBeforeRelease starts a Run and holds it after run_ended, when it takes no
// message any more and still holds the Session.
func holdBeforeRelease(t *testing.T) (runner *service.Runner, id string, hold *holdAtTheEnd, done chan service.RunResponse) {
	t.Helper()
	return holdBeforeReleaseOn(t, session.NewMemoryStore())
}

// holdBeforeReleaseOn is holdBeforeRelease over the given Store.
func holdBeforeReleaseOn(t *testing.T, store session.Store) (runner *service.Runner, id string, hold *holdAtTheEnd, done chan service.RunResponse) {
	t.Helper()
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	hold = newHold()
	runner.SetBeforeRelease(hold.hook)
	s, err := runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	id = s.ID()
	done = make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "hello"})
		done <- result
	}()
	select {
	case <-hold.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached the end")
	}
	return runner, id, hold, done
}

// A message that arrives after run_ended and before the Run releases the
// Session waits for the release and runs in the next Run; it is not busy.
func TestAMessageWhileARunIsEndingRunsInTheNextRun(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	type sent struct {
		result service.MessageResult
		err    error
	}
	answered := make(chan sent, 1)
	go func() {
		result, err := runner.Send(context.Background(), service.MessageRequest{SessionID: id, Kind: service.MessageKindFollowUp, Text: "again"})
		answered <- sent{result, err}
	}()
	select {
	case got := <-answered:
		t.Fatalf("Send answered before the Run released the Session: %+v, %v", got.result, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(hold.proceed)
	if first := <-done; first.FinalText != "echo: hello" {
		t.Fatalf("first Run = %q", first.FinalText)
	}
	select {
	case got := <-answered:
		if got.err != nil || got.result.Queued || got.result.Run == nil || got.result.Run.FinalText != "echo: again" {
			t.Fatalf("Send = %+v, %v; want a following Run answering \"echo: again\"", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never answered")
	}
}

// The wait is bounded by the caller's context.
func TestAMessageWhileARunIsEndingGivesUpWithItsContext(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := runner.Send(ctx, service.MessageRequest{SessionID: id, Kind: service.MessageKindSteer, Text: "again"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send = %v; want the context's deadline", err)
	}
	close(hold.proceed)
	<-done
}

// A Stop in the same window finds no Run to stop, and a notice waits for the
// next Run like any notice between Runs.
func TestAStopAndANoticeWhileARunIsEnding(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	err := runner.AbortSession(context.Background(), id)
	t.Logf("AbortSession in the window: %v", err)
	if err != nil && !errors.Is(err, service.ErrNoRun) {
		t.Fatalf("AbortSession = %v; want nil or ErrNoRun", err)
	}
	answered := make(chan error, 1)
	go func() {
		_, err := runner.Notice(context.Background(), service.NoticeRequest{SessionID: id, Kind: "info", Text: "fyi"})
		answered <- err
	}()
	close(hold.proceed)
	<-done
	if err := <-answered; err != nil {
		t.Fatalf("Notice = %v", err)
	}
}

// AbortRun(first) names a Run that already settled (its end is
// stored, only its save is pending). Its documented answer is ErrNoRun, "already
// stopped". The handle still carries the first Run's ID and its stopped flag
// is never cleared by rearm, so the call succeeds and cancels the Run that
// follows with the messages the first one left.
func TestAbortOfSettledRunMustNotStopTheFollowOnRun(t *testing.T) {
	store := newKindGate(session.NewMemoryStore(), gotato.EventKindSessionReleased)
	releaseHold := store.holds[gotato.EventKindSessionReleased]
	inFirst, failFirst := make(chan struct{}), make(chan struct{})
	inSecond, leaveSecond := make(chan struct{}), make(chan struct{})
	var model *testkit.FuncModel
	model = testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		if model.Calls() == 1 {
			close(inFirst)
			<-failFirst
			return nil, errors.New("provider down")
		}
		close(inSecond)
		select {
		case <-leaveSecond:
			return testkit.Text("second"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	runner := newRunnerOn(t, store, service.Agent{Name: "a", Model: model, Warm: service.WarmPolicy{TTL: -1}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.SubscribeInstance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := testkit.RecordSubscription(sub)
	go func() { _, _ = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "one"}) }()
	waitChan(t, "the first model call", inFirst)
	// A message the first Run will not take before it fails: it is left for
	// the Run that follows.
	if r, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Kind: service.MessageKindSteer, Text: "late"}); err != nil || !r.Queued {
		t.Fatalf("Send = %+v, %v", r, err)
	}
	var first gotato.RunID
	rec.Wait(5*time.Second, func(e gotato.Event) bool {
		if e.Kind() == gotato.EventKindRunStarted {
			first = e.Head().RunID
		}
		return first != ""
	})
	close(failFirst)
	waitChan(t, "the first Run's release write", releaseHold.entered)
	// The first Run is settled and its end is stored; only its save is held.
	abortErr := runner.AbortSession(ctx, s.ID(), service.AbortRun(first))
	close(releaseHold.proceed)
	if !errors.Is(abortErr, service.ErrNoRun) {
		t.Errorf("AbortRun(settled run) = %v, want ErrNoRun", abortErr)
	}
	waitChan(t, "second model call", inSecond)
	close(leaveSecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ended := rec.OfKind(gotato.EventKindRunEnded)
		if len(ended) >= 2 {
			if second := ended[1].(gotato.RunEnded); second.Status != gotato.RunStatusCompleted {
				t.Fatalf("the follow-on Run ended %q, want completed (it was never the target of the abort)", second.Status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow-on Run never ended; kinds %v, model calls %d", rec.Kinds(), model.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
