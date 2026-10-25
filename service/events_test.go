package service_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// bulkAgent is an Agent whose first Turn calls a tool many times, so one Run
// stores more Events than a subscriber's buffer holds.
func bulkAgent(name string, calls int) service.Agent {
	uses := make([]gotato.ToolCall, calls)
	for i := range uses {
		uses[i] = gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprintf("call-%d", i)), ToolID: "lookup", Arguments: []byte(`{}`)}
	}
	return service.Agent{
		Name:  name,
		Model: testkit.NewFakeModel(testkit.ToolCalls(uses...), testkit.Text("done")),
		Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
	}
}

// storedOnly keeps the Events that have a number in the Record.
func storedOnly(events []gotato.Event) []gotato.Event {
	var out []gotato.Event
	for _, event := range events {
		if event.Kind().Class() == gotato.ClassStored {
			out = append(out, event)
		}
	}
	return out
}

// numbered renders Events as "kind#number", to compare two lists of them.
func numbered(events []gotato.Event) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = fmt.Sprintf("%s#%d", event.Kind(), event.Head().Number)
	}
	return out
}

// recordRange returns the Record's Events numbered from first to last.
func recordRange(t *testing.T, runner *service.Runner, sessionID string, first, last uint64) []gotato.Event {
	t.Helper()
	stored, err := session.StoredEvents(context.Background(), runner.Store(), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []gotato.Event
	for _, event := range stored {
		if n := event.Head().Number; n >= first && n <= last {
			out = append(out, event)
		}
	}
	return out
}

// checkStreamIsRecord fails unless the stored Events of a Run's stream are, in
// kind and number, the Events of the Record from the stream's first one to its
// RunEnded: none missing, none twice, none out of order.
func checkStreamIsRecord(t *testing.T, runner *service.Runner, sessionID string, streamed []gotato.Event) {
	t.Helper()
	stored := storedOnly(streamed)
	if len(stored) == 0 {
		t.Fatal("the stream carried no stored Event")
	}
	last := stored[len(stored)-1]
	if last.Kind() != gotato.KindRunEnded {
		t.Fatalf("the stream ends with %s, want run_ended", last.Kind())
	}
	first := stored[0].Head().Number
	want := numbered(recordRange(t, runner, sessionID, first, last.Head().Number))
	got := numbered(stored)
	if !slices.Equal(got, want) {
		t.Fatalf("the stream's stored Events differ from the Record's\n got: %v\nwant: %v", got, want)
	}
	for _, event := range streamed {
		if event.Kind().Class() == gotato.ClassInstance {
			t.Fatalf("the Run's stream carries the Instance Event %s", event.Kind())
		}
	}
}

// settledInRecord waits until the Record holds a run_ended, and fails after
// timeout.
func settledInRecord(t *testing.T, runner *service.Runner, sessionID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if stored, err := session.StoredEvents(context.Background(), runner.Store(), sessionID, 0); err == nil {
			for _, event := range stored {
				if event.Kind() == gotato.KindRunEnded {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the Record of %s has no run_ended after %v: the Run waited for its stream", sessionID, timeout)
}

// A subscriber that never reads never slows the Session: the Run settles and
// its Events are all stored while the sink is stuck on its first Event, and
// when the sink goes on it still receives every stored Event once, in order,
// read again from the Record. Only deltas may be missing.
func TestSinkThatNeverReadsDoesNotSlowTheSession(t *testing.T) {
	runner, _ := newRunner(t, bulkAgent("bulk", 400))
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "bulk", nil)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result service.RunResult
		err    error
	}
	gate := make(chan struct{})
	done := make(chan outcome, 1)
	var seen []gotato.Event
	blocked := true
	go func() {
		result, err := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"}, func(event gotato.Event) error {
			if blocked {
				blocked = false
				<-gate
			}
			seen = append(seen, event)
			return nil
		})
		done <- outcome{result, err}
	}()

	// The Run settles, and everything it did is in the Record, while the sink
	// has not taken its first Event.
	settledInRecord(t, runner, s.ID(), 10*time.Second)
	select {
	case <-done:
		t.Fatal("StreamRun returned while its sink was still blocked on the first Event")
	default:
	}
	stored, err := session.StoredEvents(ctx, runner.Store(), s.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[gotato.EventKind]int{}
	for _, event := range stored {
		counts[event.Kind()]++
	}
	if counts[gotato.KindToolStarted] != 400 || counts[gotato.KindToolEnded] != 400 || counts[gotato.KindToolResultCommitted] != 400 || counts[gotato.KindRunEnded] != 1 {
		t.Fatalf("the Record is incomplete: %v", counts)
	}

	close(gate)
	var out outcome
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("StreamRun did not return after its sink went on")
	}
	if out.err != nil || out.result.Result.Status != gotato.RunCompleted {
		t.Fatalf("the Run = %+v, %v", out.result.Result, out.err)
	}
	if got := len(storedOnly(seen)); got < 1200 {
		t.Fatalf("the sink received %d stored Events, want all of them", got)
	}
	checkStreamIsRecord(t, runner, s.ID(), seen)
}

// A Run's stream numbers its Events as the Record does: for a Run streamed with
// StreamRun, the stored Events the sink saw are, in kind and number, the
// Events of the Record from the Run's first Event to its run_ended.
func TestRunStreamNumbersAreRecordNumbers(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "demo", Model: testkit.DemoModel{}, Tools: []gotato.Tool{testkit.DemoEchoTool()}})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "use-tool"})
	if err != nil {
		t.Fatal(err)
	}
	var firstEnd uint64
	if record, err := runner.Session(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	} else {
		for _, event := range record.Events() {
			if event.Kind() == gotato.KindRunEnded {
				firstEnd = event.Head().Number
			}
		}
	}
	var streamed []gotato.Event
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "use-tool"}, func(event gotato.Event) error {
		streamed = append(streamed, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkStreamIsRecord(t, runner, first.SessionID, streamed)
	// A number counts the whole Record, not one Run.
	if n := storedOnly(streamed)[0].Head().Number; n <= firstEnd {
		t.Fatalf("the second Run's first Event is numbered %d, but the first Run ended at %d", n, firstEnd)
	}
	// And the numbers are those the store holds.
	stored, err := session.StoredEvents(ctx, runner.Store(), first.SessionID, storedOnly(streamed)[0].Head().Number-1)
	if err != nil {
		t.Fatal(err)
	}
	want := numbered(storedOnly(streamed))
	got := numbered(stored[:len(want)])
	if !slices.Equal(got, want) {
		t.Fatalf("store\n got: %v\nwant: %v", got, want)
	}
}

// SubscribeSession starts after a number: the later stored Events come first,
// read from the Record, and the live Events of a later Run follow with no gap
// and no duplicate.
func TestSubscribeSessionCatchesUpThenGoesLive(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	var after uint64
	record, err := runner.Session(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range record.Events() {
		if event.Kind() == gotato.KindRunEnded {
			after = event.Head().Number
		}
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}

	sub, err := runner.SubscribeSession(ctx, first.SessionID, after)
	if err != nil {
		t.Fatal(err)
	}
	recorder := testkit.RecordSubscription(sub)
	defer recorder.Close()

	// The second Run's Events arrive from the Record, though no Run is going.
	secondEnd := recordRange(t, runner, first.SessionID, after+1, 1<<62)
	last := secondEnd[len(secondEnd)-1].Head().Number
	if !recorder.Wait(5*time.Second, func(event gotato.Event) bool { return event.Head().Number == last }) {
		t.Fatalf("the catch-up never reached #%d: %v", last, numbered(recorder.Events()))
	}
	if got, want := numbered(recorder.Events()), numbered(secondEnd); !slices.Equal(got, want) {
		t.Fatalf("catch-up\n got: %v\nwant: %v", got, want)
	}

	// A later Run is live on the same subscription.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "three"}); err != nil {
		t.Fatal(err)
	}
	all := recordRange(t, runner, first.SessionID, after+1, 1<<62)
	end := all[len(all)-1].Head().Number
	if !recorder.Wait(5*time.Second, func(event gotato.Event) bool {
		return event.Head().Number == end && event.Kind().Class() == gotato.ClassStored
	}) {
		t.Fatalf("the live Events never reached #%d: %v", end, numbered(recorder.Events()))
	}
	got := numbered(storedOnly(recorder.Events()))
	if want := numbered(all); !slices.Equal(got, want) {
		t.Fatalf("catch-up then live\n got: %v\nwant: %v", got, want)
	}
}

// SubscribeInstance carries the Events of every Session of every Agent, and
// ForAgents keeps those of the named Agents. The Instance Events, the store's
// timings, are there and are not in a Run's stream.
func TestSubscribeInstanceSeesAllAgentsAndForAgentsFilters(t *testing.T) {
	runner, _ := newRunner(t,
		service.Agent{Name: "alpha", Model: testkit.EchoModel{}},
		service.Agent{Name: "beta", Model: testkit.EchoModel{}},
	)
	ctx := context.Background()
	all := instanceRecorder(t, runner)
	onlyBeta := instanceRecorder(t, runner, service.ForAgents("beta"))

	var streamed []gotato.Event
	sink := func(event gotato.Event) error { streamed = append(streamed, event); return nil }
	sessions := map[string]string{}
	for _, agent := range []string{"alpha", "beta"} {
		result, err := runner.StreamRun(ctx, service.RunRequest{Agent: agent, Prompt: "hi " + agent}, sink)
		if err != nil {
			t.Fatal(err)
		}
		sessions[agent] = result.SessionID
		// A second Run loads the Session, which is an Instance Event.
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: result.SessionID, Prompt: "again"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range streamed {
		if event.Kind().Class() == gotato.ClassInstance {
			t.Fatalf("a Run's stream carries the Instance Event %s", event.Kind())
		}
	}

	savedTwice := func(session string) func() bool {
		return func() bool {
			saves, ended := 0, 0
			for _, event := range all.Events() {
				if string(event.Head().SessionID) != session {
					continue
				}
				switch event.Kind() {
				case gotato.KindSessionSaved:
					saves++
				case gotato.KindRunEnded:
					ended++
				}
			}
			// A stored Event reaches the stream on the Session's way, and a
			// store timing on the Runner's, so one can arrive before the other.
			return saves >= 2 && ended >= 2
		}
	}
	waitFor(t, savedTwice(sessions["alpha"]))
	waitFor(t, savedTwice(sessions["beta"]))
	waitFor(t, func() bool {
		saves, ended := 0, 0
		for _, event := range onlyBeta.Events() {
			switch event.Kind() {
			case gotato.KindSessionSaved:
				saves++
			case gotato.KindRunEnded:
				ended++
			}
		}
		return saves >= 2 && ended >= 2
	})

	kindsOf := func(rec *testkit.EventRecorder, session string) map[gotato.EventKind]int {
		counts := map[gotato.EventKind]int{}
		for _, event := range rec.Events() {
			if string(event.Head().SessionID) == session {
				counts[event.Kind()]++
			}
		}
		return counts
	}
	for agent, id := range sessions {
		counts := kindsOf(all, id)
		if counts[gotato.KindRunEnded] != 2 || counts[gotato.KindSessionSaved] < 2 || counts[gotato.KindSessionLoaded] < 1 {
			t.Fatalf("the Instance stream's Events of %s's Session = %v, want its two Runs and the store's timings", agent, counts)
		}
	}
	for _, event := range onlyBeta.Events() {
		if got := string(event.Head().SessionID); got != "" && got != sessions["beta"] {
			t.Fatalf("ForAgents(beta) let through %s of Session %q (alpha is %s, beta is %s)", event.Kind(), got, sessions["alpha"], sessions["beta"])
		}
	}
	counts := kindsOf(onlyBeta, sessions["beta"])
	if counts[gotato.KindRunEnded] != 2 || counts[gotato.KindSessionSaved] < 2 || counts[gotato.KindSessionLoaded] < 1 {
		t.Fatalf("ForAgents(beta) = %v, want beta's Runs and its Instance Events", counts)
	}
}

// claimsOf returns the session_claimed Events of the Record.
func claimsOf(t *testing.T, runner *service.Runner, sessionID string) []gotato.SessionClaimed {
	t.Helper()
	record, err := runner.Session(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var claims []gotato.SessionClaimed
	for _, event := range record.Events() {
		if claimed, ok := event.(gotato.SessionClaimed); ok {
			claims = append(claims, claimed)
		}
	}
	return claims
}

// The first Run of a new Session writes session_claimed (new_session) before
// the Run's own Events; a Run on an existing Session claims it again (idle),
// and the end of that Run writes session_released.
func TestRunWritesClaimAndRelease(t *testing.T) {
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, fastLiveness)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if claims := claimsOf(t, a, first.SessionID); len(claims) != 1 || claims[0].Why != gotato.ClaimNewSession ||
		claims[0].Instance != "instance-a" || claims[0].Epoch != 1 || claims[0].From != "" {
		t.Fatalf("claims = %+v, want one: instance-a at epoch 1 for a new_session", claims)
	}
	// The first Run of a new Session releases it too: it is busy from its
	// creation until then.
	if firstRecord, err := a.Session(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	} else if events := firstRecord.Events(); events[len(events)-1].Kind() != gotato.KindSessionReleased {
		t.Fatalf("the first Run left the Record at %v, want session_released last", numbered(events))
	}
	result, err := a.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.Session(ctx, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	events := record.Events()
	claimed, started, ended, released := -1, -1, -1, -1
	for i, event := range events {
		switch event.Kind() {
		case gotato.KindSessionClaimed:
			claimed = i
		case gotato.KindRunStarted:
			started = i
		case gotato.KindRunEnded:
			ended = i
		case gotato.KindSessionReleased:
			released = i
		}
	}
	if claimed < 0 || started < 0 || ended < 0 || released < 0 || claimed >= started || started >= ended || ended >= released {
		t.Fatalf("Events = %v, want session_claimed, run_started, run_ended, session_released in that order", numbered(events))
	}
	claim := events[claimed].(gotato.SessionClaimed)
	if claim.Why != gotato.ClaimIdle || claim.Instance != "instance-a" || claim.Epoch != 1 {
		t.Fatalf("claim = %+v, want instance-a at epoch 1, why idle", claim)
	}
	release := events[released].(gotato.SessionReleased)
	if release.Instance != "instance-a" || release.Epoch != 1 {
		t.Fatalf("release = %+v, want instance-a at epoch 1", release)
	}
	if released != len(events)-1 {
		t.Fatalf("the Record goes on after session_released: %v", numbered(events))
	}
}

// Another Instance that takes over an idle Session writes session_claimed at
// the next Epoch and names the Instance it took the Session from.
func TestTakeoverWritesClaimWithNextEpochAndFrom(t *testing.T) {
	store := session.NewMemoryStore()
	a := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, fastLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, fastLiveness)
	ctx := context.Background()
	first, err := a.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	claims := claimsOf(t, b, first.SessionID)
	if len(claims) != 2 {
		t.Fatalf("claims = %+v, want two", claims)
	}
	if claims[0].Instance != "instance-a" || claims[0].Epoch != 1 || claims[0].Why != gotato.ClaimNewSession {
		t.Fatalf("first claim = %+v", claims[0])
	}
	if got := claims[1]; got.Instance != "instance-b" || got.Epoch != 2 || got.From != "instance-a" || got.Why != gotato.ClaimIdle {
		t.Fatalf("takeover claim = %+v, want instance-b at epoch 2, from instance-a, why idle", got)
	}
}

// deadHolders makes an Instance see every other Instance as not alive, as if
// their Liveness rows had expired, so it takes over a Session that is busy.
type deadHolders struct {
	session.Store
	self string
}

func (s deadHolders) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if id != s.self {
		return session.InstanceState{ID: id}, nil
	}
	return s.Store.Instance(ctx, id)
}

// A write the store refuses because another Instance took the Claim is
// published as a session_fenced Instance Event, and the fenced Instance writes
// nothing more.
func TestFencedWritePublishesSessionFenced(t *testing.T) {
	ctx := context.Background()
	backing := session.NewMemoryStore()
	held := &gateModel{gate: make(chan struct{}), started: make(chan struct{})}
	a := instanceRunner(t, backing, "instance-a", held, fastLiveness)
	b := instanceRunner(t, deadHolders{Store: backing, self: "instance-b"}, "instance-b", testkit.EchoModel{}, fastLiveness)
	fenced := instanceRecorder(t, a, gotato.SubscribeFilter(func(event gotato.Event) bool {
		return event.Kind() == gotato.KindSessionFenced
	}))
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
		done <- err
	}()
	<-held.started
	if _, err := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "taken"}); err != nil {
		t.Fatalf("instance-b taking over the Session = %v", err)
	}
	close(held.gate)
	if err := <-done; err == nil {
		t.Fatal("the fenced Run succeeded")
	}
	if !fenced.Wait(5*time.Second, func(event gotato.Event) bool { return true }) {
		t.Fatal("no session_fenced was published to instance-a's Instance stream")
	}
	event, ok := fenced.Events()[0].(gotato.SessionFenced)
	if !ok || event.Instance != "instance-a" || string(event.SessionID) != s.ID() {
		t.Fatalf("event = %+v, want session_fenced by instance-a for %s", fenced.Events()[0], s.ID())
	}
	// The fenced Instance wrote nothing after the takeover: its reply is not
	// in the Record.
	record, err := b.Session(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range record.Messages() {
		if gotato.TextOf(message) == "released" {
			t.Fatalf("the fenced Instance's reply is in the Record: %v", record.Messages())
		}
	}
}

// A Liveness row that lapsed publishes liveness_lapsed once, however many
// writes are then refused.
func TestLapsedLivenessPublishesOnce(t *testing.T) {
	ctx := context.Background()
	backing := session.NewMemoryStore()
	paused := &pausableStore{Store: backing}
	a := instanceRunner(t, paused, "instance-a", testkit.EchoModel{}, fastLiveness)
	lapsed := instanceRecorder(t, a, gotato.SubscribeFilter(func(event gotato.Event) bool {
		return event.Kind() == gotato.KindLivenessLapsed
	}))
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	paused.paused.Store(true)
	time.Sleep(2 * fastLiveness.TTL)
	var refused atomic.Int32
	for range 3 {
		if _, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "late"}); err != nil && !errors.Is(err, context.Canceled) {
			refused.Add(1)
		}
	}
	if refused.Load() != 3 {
		t.Fatalf("%d of 3 late Runs were refused", refused.Load())
	}
	if !lapsed.Wait(5*time.Second, func(gotato.Event) bool { return true }) {
		t.Fatal("no liveness_lapsed was published")
	}
	time.Sleep(100 * time.Millisecond)
	events := lapsed.Events()
	if len(events) != 1 {
		t.Fatalf("liveness_lapsed was published %d times, want once", len(events))
	}
	if event, ok := events[0].(gotato.LivenessLapsed); !ok || event.Instance != "instance-a" {
		t.Fatalf("event = %+v, want liveness_lapsed of instance-a", events[0])
	}
}
