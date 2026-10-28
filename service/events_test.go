package service_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
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
		if event.Kind().Class() == gotato.EventClassStored {
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
	stored, err := session.StoredEvents(context.Background(), runner.Store(), sessionID, 0, 0)
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
	if last.Kind() != gotato.EventKindRunEnded {
		t.Fatalf("the stream ends with %s, want run_ended", last.Kind())
	}
	first := stored[0].Head().Number
	want := numbered(recordRange(t, runner, sessionID, first, last.Head().Number))
	got := numbered(stored)
	if !slices.Equal(got, want) {
		t.Fatalf("the stream's stored Events differ from the Record's\n got: %v\nwant: %v", got, want)
	}
	for _, event := range streamed {
		if event.Kind().Class() == gotato.EventClassInstance {
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
		if stored, err := session.StoredEvents(context.Background(), runner.Store(), sessionID, 0, 0); err == nil {
			for _, event := range stored {
				if event.Kind() == gotato.EventKindRunEnded {
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
		result service.RunResponse
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
	stored, err := session.StoredEvents(ctx, runner.Store(), s.ID(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[gotato.EventKind]int{}
	for _, event := range stored {
		counts[event.Kind()]++
	}
	if counts[gotato.EventKindToolStarted] != 400 || counts[gotato.EventKindToolEnded] != 400 || counts[gotato.EventKindToolResultCommitted] != 400 || counts[gotato.EventKindRunEnded] != 1 {
		t.Fatalf("the Record is incomplete: %v", counts)
	}

	close(gate)
	var out outcome
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("StreamRun did not return after its sink went on")
	}
	if out.err != nil || out.result.Result.Status != gotato.RunStatusCompleted {
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
	if record, err := runner.Record(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	} else {
		for _, event := range record.Events() {
			if event.Kind() == gotato.EventKindRunEnded {
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
	stored, err := session.StoredEvents(ctx, runner.Store(), first.SessionID, storedOnly(streamed)[0].Head().Number-1, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := numbered(storedOnly(streamed))
	got := numbered(stored[:len(want)])
	if !slices.Equal(got, want) {
		t.Fatalf("store\n got: %v\nwant: %v", got, want)
	}
}

// StreamSession starts after a number: the later stored Events come first,
// read from the Record, and the live Events of a later Run follow with no gap
// and no duplicate.
func TestStreamSessionCatchesUpThenGoesLive(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	var after uint64
	record, err := runner.Record(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range record.Events() {
		if event.Kind() == gotato.EventKindRunEnded {
			after = event.Head().Number
		}
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}

	sub, err := runner.StreamSession(ctx, first.SessionID, service.StreamFrom{After: after})
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
		return event.Head().Number == end && event.Kind().Class() == gotato.EventClassStored
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
		service.Agent{Name: "alpha", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}},
		service.Agent{Name: "beta", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}},
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
		if event.Kind().Class() == gotato.EventClassInstance {
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
				case gotato.EventKindSessionSaved:
					saves++
				case gotato.EventKindRunEnded:
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
			case gotato.EventKindSessionSaved:
				saves++
			case gotato.EventKindRunEnded:
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
		if counts[gotato.EventKindRunEnded] != 2 || counts[gotato.EventKindSessionSaved] < 2 || counts[gotato.EventKindSessionLoaded] < 1 {
			t.Fatalf("the Instance stream's Events of %s's Session = %v, want its two Runs and the store's timings", agent, counts)
		}
	}
	for _, event := range onlyBeta.Events() {
		if got := string(event.Head().SessionID); got != "" && got != sessions["beta"] {
			t.Fatalf("ForAgents(beta) let through %s of Session %q (alpha is %s, beta is %s)", event.Kind(), got, sessions["alpha"], sessions["beta"])
		}
	}
	counts := kindsOf(onlyBeta, sessions["beta"])
	if counts[gotato.EventKindRunEnded] != 2 || counts[gotato.EventKindSessionSaved] < 2 || counts[gotato.EventKindSessionLoaded] < 1 {
		t.Fatalf("ForAgents(beta) = %v, want beta's Runs and its Instance Events", counts)
	}
}

// A load and a save are Instance Events: they time the store, they go to the
// Instance's subscribers, and they are neither in a Run's stream nor in the
// Session's Record.
func TestStoreEventsAreInstanceEvents(t *testing.T) {
	// No warm copy, so the second Run loads the Session and times the load.
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo", Warm: service.WarmPolicy{TTL: -1}})
	instance := instanceRecorder(t, runner)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}

	var events []gotato.Event
	result, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}, func(event gotato.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(events); len(kinds) == 0 || !slices.Contains(kinds, gotato.EventKindRunStarted) || kinds[len(kinds)-1] != gotato.EventKindRunEnded {
		t.Fatalf("the Run's stream = %v, want it to hold run_started and end at run_ended", kinds)
	}
	for _, event := range events {
		if event.Kind().Class() == gotato.EventClassInstance {
			t.Fatalf("the Run's stream carries the Instance Event %s", event.Kind())
		}
	}

	isSaved := func(event gotato.Event) bool {
		saved, ok := event.(gotato.SessionSaved)
		return ok && saved.RunID == result.Result.RunID
	}
	if !instance.Wait(5*time.Second, isSaved) {
		t.Fatalf("the Instance stream never saw the save of %s: %v", result.Result.RunID, instance.Kinds())
	}
	var loaded gotato.SessionLoaded
	var saved gotato.SessionSaved
	for _, event := range instance.Events() {
		switch e := event.(type) {
		case gotato.SessionLoaded:
			if string(e.SessionID) == first.SessionID {
				loaded = e
			}
		case gotato.SessionSaved:
			if e.RunID == result.Result.RunID {
				saved = e
			}
		}
	}
	if loaded.Kind() == "" || loaded.RunID != "" || loaded.Number != 0 || loaded.Messages != first.Messages || loaded.ElapsedMS < 0 || loaded.Failed {
		t.Fatalf("session_loaded = %+v, want the %d stored messages", loaded, first.Messages)
	}
	if string(saved.SessionID) != first.SessionID || saved.Messages != result.Messages || saved.Failed {
		t.Fatalf("session_saved = %+v, want %d messages", saved, result.Messages)
	}

	// They time the store; they are not part of the Session's own Record.
	stored, err := session.StoredEvents(ctx, store, first.SessionID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range stored {
		if event.Kind().Class() == gotato.EventClassInstance {
			t.Fatalf("the Record holds %s", event.Kind())
		}
	}
}

func TestStoreEventsFlagFailuresWithoutTheirText(t *testing.T) {
	store := &failingSaveStore{Store: session.NewMemoryStore(), failFinal: true}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	instance := instanceRecorder(t, runner)
	ctx := context.Background()
	sink := func(gotato.Event) error { return nil }
	if _, err := runner.StreamRun(ctx, service.RunRequest{Prompt: "hi"}, sink); !errors.Is(err, service.ErrNotPersisted) {
		t.Fatalf("err = %v, want ErrNotPersisted", err)
	}
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: "missing", Prompt: "hi"}, sink); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	instance.Wait(5*time.Second, func(event gotato.Event) bool {
		loaded, ok := event.(gotato.SessionLoaded)
		return ok && loaded.Failed
	})
	var flagged []gotato.EventKind
	for _, event := range instance.Events() {
		raw, err := gotato.MarshalEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "disk full") {
			t.Fatalf("%s carries the store's error text: %s", event.Kind(), raw)
		}
		switch e := event.(type) {
		case gotato.SessionSaved:
			if e.Failed {
				flagged = append(flagged, e.Kind())
			}
		case gotato.SessionLoaded:
			if e.Failed {
				flagged = append(flagged, e.Kind())
			}
		}
	}
	if len(flagged) != 2 || flagged[0] != gotato.EventKindSessionSaved || flagged[1] != gotato.EventKindSessionLoaded {
		t.Fatalf("flagged = %v, want the failed save and the failed load", flagged)
	}
}
