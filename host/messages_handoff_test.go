package host_test

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

type handoffRig struct {
	runner *host.Runner
	hold   *holdAtTheEnd
	id     string
	ended  *testkit.EventRecorder
	done   chan host.RunResponse
}

// startHeld starts a Run on a Runner with the default busy policy (reject),
// and returns when it has settled and is held.
func startHeld(t *testing.T) *handoffRig {
	t.Helper()
	return startHeldAgent(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}})
}

// startHeldAgent is startHeld for the Agent "echo" as given.
func startHeldAgent(t *testing.T, agent profile.AgentProfile, mutate ...func(*host.Config)) *handoffRig {
	t.Helper()
	cfg := host.Config{
		Store:    memStore(),
		Profiles: registry(t, agent), DefaultAgent: agent.Name,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	runner, err := host.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	rig := &handoffRig{runner: runner, hold: newHold(), done: make(chan host.RunResponse, 1)}
	runner.SetBeforeDetach(rig.hold.hook)
	s, err := runner.CreateSession(context.Background(), host.CreateSessionRequest{Agent: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	rig.id = s.ID
	sub, err := runner.SubscribeInstance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rig.ended = testkit.RecordSubscription(sub)
	go func() {
		result, _ := runner.Run(context.Background(), host.RunRequest{SessionID: rig.id, Prompt: "hello"})
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
func (r *handoffRig) release(t *testing.T) host.RunResponse {
	t.Helper()
	close(r.hold.proceed)
	select {
	case result := <-r.done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the held Run did not return")
		return host.RunResponse{}
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

func (r *handoffRig) send(t *testing.T, kind host.MessageKind, text string) gotato.MessageID {
	t.Helper()
	result, err := r.runner.Send(context.Background(), host.MessageRequest{SessionID: r.id, Kind: kind, Text: text})
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
	id := rig.send(t, host.MessageKindSteer, "late")
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
		rig.send(t, host.MessageKindSteer, "one"),
		rig.send(t, host.MessageKindFollowUp, "two"),
		rig.send(t, host.MessageKindSteer, "three"),
		rig.send(t, host.MessageKindFollowUp, "four"),
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
	id := rig.send(t, host.MessageKindSteer, "late")
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
	id := rig.send(t, host.MessageKindFollowUp, "late")
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
		_, err := rig.runner.Send(context.Background(), host.MessageRequest{SessionID: rig.id, Kind: host.MessageKindInterrupt, Text: "stop"})
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
	parts
	armed atomic.Bool
}

func (s *loadFails) Load(ctx context.Context, id string) (record.Loaded, error) {
	if s.armed.Load() {
		return record.Loaded{}, errors.New("store down")
	}
	return s.parts.Load(ctx, id)
}

// A following Run that cannot open the Session drops each message it was to
// take, with run_failed, on the Instance's stream; nothing can be written to the
// Record. A Run that follows without failing drops nothing (the tests above).
func TestAFollowingRunThatFailsDropsItsMessages(t *testing.T) {
	store := &loadFails{parts: partsOf(memStore())}
	runner, err := host.New(host.Config{
		Store:    asStore(store),
		Profiles: registry(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}), DefaultAgent: "echo", Warm: host.WarmPolicy{TTL: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	hold := newHold()
	runner.SetBeforeDetach(hold.hook)
	s, err := runner.CreateSession(context.Background(), host.CreateSessionRequest{Agent: "echo"})
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
		_, _ = runner.Run(context.Background(), host.RunRequest{SessionID: s.ID, Prompt: "hello"})
		close(done)
	}()
	<-hold.reached
	var ids []gotato.MessageID
	for _, text := range []string{"one", "two"} {
		result, err := runner.Send(context.Background(), host.MessageRequest{SessionID: s.ID, Kind: host.MessageKindFollowUp, Text: text})
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

// vanishingDefinitions are Definitions whose rows can be dropped.
type vanishingDefinitions struct {
	store.Definitions
	gone atomic.Bool
}

func (d *vanishingDefinitions) vanish() { d.gone.Store(true) }

func (d *vanishingDefinitions) LoadDefinition(ctx context.Context, kind, name string) ([]byte, error) {
	if d.gone.Load() {
		return nil, record.ErrNotFound
	}
	return d.Definitions.LoadDefinition(ctx, kind, name)
}

// A following Run that opened the Session and then finds its Agent gone writes
// the drops in the Record, where the message_queued Events are, and they reach
// the Session's subscribers too.
func TestAFollowingRunWithoutItsAgentDropsInTheRecord(t *testing.T) {
	defs := &vanishingDefinitions{Definitions: store.NewMemoryDefinitions()}
	if err := defs.SaveDefinition(context.Background(), profile.KindAgentProfile, "echo", []byte(`{"model":"echo"}`)); err != nil {
		t.Fatal(err)
	}
	rig := startHeldAgent(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}}, func(cfg *host.Config) {
		catalog, err := profile.NewCatalog(map[string]gotato.Model{"echo": testkit.EchoModel{}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		reg := profile.NewRegistry(catalog)
		if err := reg.RegisterAgentProfileWithStore("echo", defs); err != nil {
			t.Fatal(err)
		}
		cfg.Profiles = reg
	})
	id := rig.send(t, host.MessageKindSteer, "late")
	defs.vanish() // as a deploy that drops the profile would
	rig.release(t)

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, dropped, _ := rig.events(t)
		// The Record has the drop before the Instance's stream delivers it.
		if dropped[id] == "run_failed" && len(rig.ended.OfKind(gotato.EventKindMessageDropped)) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dropped %v, Instance stream has %d; want message %s dropped for run_failed in the Record and once on the stream", dropped, len(rig.ended.OfKind(gotato.EventKindMessageDropped)), id)
		}
		runtime.Gosched()
	}
	if got := transcriptOf(t, rig.runner, rig.id); !equal(got, []string{"user:hello", "assistant:echo: hello"}) {
		t.Fatalf("transcript = %v", got)
	}
}

// A following Run whose first message cannot be committed (the Session is at
// its message limit) ends without taking it: the message is reported dropped,
// so its message_queued is closed in the Record.
func TestAFollowingRunThatCannotCommitItsFirstMessageDropsIt(t *testing.T) {
	rig := startHeldAgent(t, profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}, Limits: &gotato.CoreLimits{MaxMessages: 2}})
	id := rig.send(t, host.MessageKindSteer, "late")
	rig.release(t)
	rig.waitRuns(t, 2)

	// The following Run saves after it ends, so the Record is read until the
	// drop is in it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := rig.runner.Events(context.Background(), rig.id)
		if err != nil {
			t.Fatal(err)
		}
		// Every message_queued is closed once, by a commit or a drop.
		open := map[gotato.MessageID]int{}
		why := map[gotato.MessageID]string{}
		for _, event := range events {
			switch e := event.(type) {
			case gotato.MessageQueued:
				open[e.MessageID]++
			case gotato.MessageCommitted:
				if open[e.Message.ID] > 0 {
					open[e.Message.ID]--
				}
			case gotato.MessageDropped:
				open[e.MessageID]--
				why[e.MessageID] = e.Why
			}
		}
		if open[id] == 0 && why[id] == "run_failed" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %s: open count %d, dropped for %q; want closed by a run_failed drop", id, open[id], why[id])
		}
		runtime.Gosched()
	}
}

// holdBeforeRelease starts a Run and holds it after run_ended, when it takes no
// message any more and still holds the Session.
func holdBeforeRelease(t *testing.T) (runner *host.Runner, id string, hold *holdAtTheEnd, done chan host.RunResponse) {
	t.Helper()
	return holdBeforeReleaseOn(t, memStore())
}

// A message that arrives after run_ended and before the Run releases the
// Session waits for the release and runs in the next Run; it is not busy.
func TestAMessageWhileARunIsEndingRunsInTheNextRun(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	type sent struct {
		result host.MessageResult
		err    error
	}
	answered := make(chan sent, 1)
	go func() {
		result, err := runner.Send(context.Background(), host.MessageRequest{SessionID: id, Kind: host.MessageKindFollowUp, Text: "again"})
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
	_, err := runner.Send(ctx, host.MessageRequest{SessionID: id, Kind: host.MessageKindSteer, Text: "again"})
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
	if err != nil && !errors.Is(err, host.ErrNoRun) {
		t.Fatalf("AbortSession = %v; want nil or ErrNoRun", err)
	}
	answered := make(chan error, 1)
	go func() {
		_, err := runner.Notice(context.Background(), host.NoticeRequest{SessionID: id, Kind: "info", Text: "fyi"})
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
	store := newKindGate(memStore(), gotato.EventKindSessionReleased)
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
	runner := newRunnerOn(t, asStore(store), profile.AgentProfile{Name: "a", Model: model}, noWarm)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := runner.CreateSession(ctx, host.CreateSessionRequest{Agent: "a"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.SubscribeInstance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec := testkit.RecordSubscription(sub)
	go func() { _, _ = runner.Run(ctx, host.RunRequest{SessionID: s.ID, Prompt: "one"}) }()
	waitChan(t, "the first model call", inFirst)
	// A message the first Run will not take before it fails: it is left for
	// the Run that follows.
	if r, err := runner.Send(ctx, host.MessageRequest{SessionID: s.ID, Kind: host.MessageKindSteer, Text: "late"}); err != nil || !r.Queued {
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
	abortErr := runner.AbortSession(ctx, s.ID, host.AbortRun(first))
	close(releaseHold.proceed)
	if !errors.Is(abortErr, host.ErrNoRun) {
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
