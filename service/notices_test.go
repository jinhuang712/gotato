package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// noticeKinds lists the notice Events of a Session in order, as "kind:label".
func noticeEvents(t *testing.T, runner *service.Runner, id string) (queued []gotato.NoticeQueued, committed []gotato.NoticeCommitted) {
	t.Helper()
	events, err := runner.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		switch e := event.(type) {
		case gotato.NoticeQueued:
			queued = append(queued, e)
		case gotato.NoticeCommitted:
			committed = append(committed, e)
		}
	}
	return queued, committed
}

func noticeText(label, text string) string {
	return `notice:<notice kind="` + label + `">` + text + `</notice>`
}

// A notice sent to a Run goes in right before its next model call, together
// with a Steer and in the order they arrived, and the Event that reports it
// committed names the one that reported it queued.
func TestANoticeReachesARunAtItsNextModelCall(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(false, "first", "second")
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	done := startRun(t, runner, s.ID(), "hello")
	waitCall(t, model, 0)
	notice, err := runner.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "order_update", Text: "Order 42 was delivered."})
	if err != nil || !notice.Queued || notice.Number == 0 || notice.MessageID == "" {
		t.Fatalf("Notice = %+v, %v", notice, err)
	}
	if _, err := runner.Send(ctx, service.MessageRequest{SessionID: s.ID(), Text: "prefer metric"}); err != nil {
		t.Fatal(err)
	}
	close(model.release[0])
	waitCall(t, model, 1)
	want := []string{"user:hello", "assistant:first", noticeText("order_update", "Order 42 was delivered."), "user:prefer metric"}
	if got := texts(model.request(1)); !equal(got, want) {
		t.Fatalf("second request = %v, want %v", got, want)
	}
	close(model.release[1])
	settled(t, done)

	queued, committed := noticeEvents(t, runner, s.ID())
	if len(queued) != 1 || len(committed) != 1 || queued[0].Number != notice.Number || committed[0].Queued != notice.Number {
		t.Fatalf("notice_queued %+v, notice_committed %+v; the commit must name queue entry %d", queued, committed, notice.Number)
	}
	if queued[0].NoticeKind != "order_update" || queued[0].Source != gotato.NoticeFromService || string(queued[0].MessageID) != notice.MessageID {
		t.Fatalf("notice_queued = %+v", queued[0])
	}
}

// A notice sent while no Run is going starts no Run, and waits for the next
// one, where it goes in after the prompt. A notice that arrives as a Run
// settles keeps no Run going, and waits the same way.
func TestANoticeBetweenRunsWaitsForTheNextRun(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(false, "first", "second", "third")
	close(model.release[1])
	close(model.release[2])
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	// No Run going: the notice is stored and starts nothing.
	notice, err := runner.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "k", Text: "idle one"})
	if err != nil || notice.Number == 0 {
		t.Fatalf("Notice = %+v, %v", notice, err)
	}
	if runner.ActiveRuns() != 0 || len(transcriptOf(t, runner, s.ID())) != 0 {
		t.Fatal("a notice started a Run or entered the Transcript")
	}
	// A Run going: the notice arrives while its only model call is in flight.
	done := startRun(t, runner, s.ID(), "hello")
	waitCall(t, model, 0)
	if _, err := runner.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "k", Text: "late one"}); err != nil {
		t.Fatal(err)
	}
	close(model.release[0])
	settled(t, done)
	// The first notice went in after the prompt; the late one is still waiting.
	if got := texts(model.request(0)); !equal(got, []string{"user:hello", noticeText("k", "idle one")}) {
		t.Fatalf("first request = %v", got)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d: a notice kept the Run going", model.calls)
	}
	result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "again"})
	if err != nil || result.Result.Status != gotato.RunCompleted {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	want := []string{"user:hello", noticeText("k", "idle one"), "assistant:first", "user:again", noticeText("k", "late one")}
	if got := texts(model.request(1)); !equal(got, want) {
		t.Fatalf("second request = %v, want %v", got, want)
	}
	queued, committed := noticeEvents(t, runner, s.ID())
	if len(queued) != 2 || len(committed) != 2 {
		t.Fatalf("notice_queued %d, notice_committed %d, want 2 and 2", len(queued), len(committed))
	}
	for i, c := range committed {
		if c.Queued != queued[i].Number {
			t.Errorf("commit %d names %d, queued as %d", i, c.Queued, queued[i].Number)
		}
	}
}

func TestNoticeRefusals(t *testing.T) {
	ctx := context.Background()
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", testkit.EchoModel{})
	s, _ := runner.CreateSession(ctx, "echo", nil)
	for name, request := range map[string]service.NoticeRequest{
		"no session": {Kind: "k", Text: "t"},
		"no kind":    {SessionID: s.ID(), Text: "t"},
		"reserved":   {SessionID: s.ID(), Kind: gotato.NoticeTimeAway, Text: "t"},
		"no text":    {SessionID: s.ID(), Kind: "k", Text: " "},
	} {
		if _, err := runner.Notice(ctx, request); !gotato.IsCode(err, gotato.CodeInvalidArgument) || !gotato.NotRun(err) {
			t.Errorf("%s: Notice = %v, want invalid_argument, not run", name, err)
		}
	}
	if _, err := runner.Notice(ctx, service.NoticeRequest{SessionID: "missing", Kind: "k", Text: "t"}); !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Errorf("Notice to a missing Session = %v, want not_found", err)
	}
}

// A notice for a Run going on another instance is answered not_writer with
// the writer's address, as a message is; the writer takes its own.
func TestANoticeForARunOnAnotherInstanceIsNotWriter(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := newStagedModel(false, "first")
	a := messageRunner(t, store, "instance-a", "http://instance-a.example:8080", model)
	b := messageRunner(t, store, "instance-b", "http://instance-b.example:8080", testkit.EchoModel{})
	s, _ := a.CreateSession(ctx, "echo", nil)

	done := startRun(t, a, s.ID(), "hello")
	waitCall(t, model, 0)
	_, err := b.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "k", Text: "from b"})
	if !gotato.IsCode(err, gotato.CodeNotWriter) || !gotato.NotRun(err) || !gotato.Retryable(err) {
		t.Fatalf("Notice = %v; want not_writer, not run, retryable", err)
	}
	if address, ok := service.WriterOf(err); !ok || address != "http://instance-a.example:8080" {
		t.Fatalf("writer = %q, %v", address, ok)
	}
	if _, err := a.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "k", Text: "from a"}); err != nil {
		t.Fatal(err)
	}
	close(model.release[0])
	settled(t, done)
}

// timeAwayRunner is an Instance whose clock the test moves, over an echo model
// that remembers its requests.
func timeAwayRunner(t *testing.T, clock *fakeClock, agent service.Agent) (*service.Runner, *testkit.FakeModel) {
	t.Helper()
	scripts := make([]testkit.Script, 8)
	for i := range scripts {
		scripts[i] = testkit.Text("ok")
	}
	model := testkit.NewFakeModel(scripts...)
	agent.Name, agent.Model = "echo", model
	agent.Warm.NoIdleCompaction = true
	runner, err := service.New(service.WithClock(service.Config{
		Store: session.NewMemoryStore(), Instance: "a", Liveness: steadyLiveness,
		Admission: service.Admission{Queue: service.WaitWhileBusy}, Agents: []service.Agent{agent},
	}, clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner, model
}

func lastRequestTexts(model *testkit.FakeModel) []string {
	request, _ := model.LastRequest()
	return texts(request.Messages)
}

// A message that starts a Run after the Session was idle longer than the
// threshold is followed by a time_away notice with how long it has been and the
// time now; inside the threshold there is none. The idle time is read from the
// Record, so a Session that left memory gets the notice the same.
func TestATimeAwayNoticeFollowsAMessageAfterALongAbsence(t *testing.T) {
	ctx := context.Background()
	// The Record's times are the system's; the Runner's clock starts there.
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	runner, model := timeAwayRunner(t, clock, service.Agent{})
	s, _ := runner.CreateSession(ctx, "echo", nil)
	runOn(t, runner, s.ID(), "first")
	if got := lastRequestTexts(model); !equal(got, []string{"user:first"}) {
		t.Fatalf("the first Run got %v", got)
	}

	// Inside the threshold: none. The Session is still warm.
	clock.Advance(29 * time.Minute)
	runOn(t, runner, s.ID(), "soon")
	if got := lastRequestTexts(model); got[len(got)-1] != "user:soon" {
		t.Fatalf("a message inside the threshold got %v", got)
	}

	// Past it: 3 days. The warm copy left at its TTL; the Record tells.
	clock.Advance(72 * time.Hour)
	if runner.WarmSessions() != 0 {
		t.Fatalf("%d warm Sessions after 3 days", runner.WarmSessions())
	}
	runOn(t, runner, s.ID(), "back")
	got := lastRequestTexts(model)
	now := clock.Now().UTC().Format(time.RFC3339)
	want := noticeText(gotato.NoticeTimeAway, "It has been 3 days since the last message. It is now "+now+".")
	if len(got) < 2 || got[len(got)-2] != "user:back" || got[len(got)-1] != want {
		t.Fatalf("a message after 3 days got %v, want it followed by %s", got, want)
	}

	// The notice is kept in the Record, as Gotato's own, with its Events.
	record, _ := runner.Record(ctx, s.ID())
	messages := record.Messages()
	last := messages[len(messages)-2]
	if last.Role != gotato.RoleNotice || last.Notice.Kind != gotato.NoticeTimeAway || last.Notice.Source != gotato.NoticeFromGotato {
		t.Fatalf("the Record keeps %+v", last)
	}
}

func TestTimeAwayThresholdIsPerAgentAndCanBeTurnedOff(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		threshold time.Duration
		gap       time.Duration
		want      string
	}{
		{"a short threshold fires", 10 * time.Minute, 11*time.Minute + 30*time.Second, "It has been 11 minutes since the last message."},
		{"an hour reads as one hour", time.Minute, time.Hour + 30*time.Second, "It has been 1 hour since the last message."},
		{"the default is 30 minutes", 0, 31*time.Minute + 30*time.Second, "It has been 31 minutes since the last message."},
		{"off", -1, 1000 * time.Hour, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
			runner, model := timeAwayRunner(t, clock, service.Agent{TimeAwayAfter: tc.threshold})
			s, _ := runner.CreateSession(ctx, "echo", nil)
			runOn(t, runner, s.ID(), "first")
			clock.Advance(tc.gap)
			runOn(t, runner, s.ID(), "second")
			got := lastRequestTexts(model)
			last := got[len(got)-1]
			if tc.want == "" {
				if last != "user:second" {
					t.Fatalf("got %v, want no notice", got)
				}
				return
			}
			if !strings.Contains(last, tc.want) {
				t.Fatalf("got %v, want a notice with %q", got, tc.want)
			}
		})
	}
}

// The time is shown in the zone the Agent sets for its Segments' times.
func TestTimeAwayShowsTheTimeInTheAgentsZone(t *testing.T) {
	ctx := context.Background()
	zone := time.FixedZone("UTC+8", 8*3600)
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	agent := service.Agent{}
	agent.Compact.Location = zone
	runner, model := timeAwayRunner(t, clock, agent)
	s, _ := runner.CreateSession(ctx, "echo", nil)
	runOn(t, runner, s.ID(), "first")
	clock.Advance(2 * time.Hour)
	runOn(t, runner, s.ID(), "second")
	got := lastRequestTexts(model)
	if want := "It is now " + clock.Now().In(zone).Format(time.RFC3339) + "."; !strings.Contains(got[len(got)-1], want) || !strings.Contains(got[len(got)-1], "+08:00") {
		t.Fatalf("got %v, want %q", got, want)
	}
}

// A notice that found no Run is in the Record, not in the memory of the
// instance that took it: after that instance is gone, the Session's next Run on
// another one still gets it, after the prompt, and commits it once.
func TestANoticeBetweenRunsOutlivesItsInstance(t *testing.T) {
	ctx := context.Background()
	stores := map[string]func(t *testing.T) session.Store{
		"memory": func(*testing.T) session.Store { return session.NewMemoryStore() },
		"file": func(t *testing.T) session.Store {
			store, err := session.NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return store
		},
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			first, err := service.New(service.Config{Store: store, Instance: "first", Liveness: steadyLiveness, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
			if err != nil {
				t.Fatal(err)
			}
			s, _ := first.CreateSession(ctx, "echo", nil)
			notice, err := first.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "order_update", Text: "Order 42 was delivered."})
			if err != nil || notice.Number == 0 {
				t.Fatalf("Notice = %+v, %v", notice, err)
			}
			if err := first.Close(ctx); err != nil {
				t.Fatal(err)
			}

			model := testkit.NewFakeModel(testkit.Text("one"), testkit.Text("two"))
			second, err := service.New(service.Config{Store: store, Instance: "second", Liveness: steadyLiveness, Agents: []service.Agent{{Name: "echo", Model: model}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = second.Close(ctx) })
			runOn(t, second, s.ID(), "hello")
			request, _ := model.LastRequest()
			if got := texts(request.Messages); !equal(got, []string{"user:hello", noticeText("order_update", "Order 42 was delivered.")}) {
				t.Fatalf("request = %v", got)
			}
			// It was committed, so the Run after it does not carry it twice.
			runOn(t, second, s.ID(), "again")
			request, _ = model.LastRequest()
			if got := texts(request.Messages); len(got) != 4 || got[3] != "user:again" {
				t.Fatalf("second request = %v", got)
			}
		})
	}
}

// A Stop ends the Run's work, not a notice: one the stopped Run did not take
// waits for the next Run.
func TestANoticeLeftByAStoppedRunWaitsForTheNextRun(t *testing.T) {
	ctx := context.Background()
	model := newStagedModel(true, "", "after the stop")
	close(model.release[1])
	runner := messageRunner(t, session.NewMemoryStore(), "instance-a", "", model)
	s, _ := runner.CreateSession(ctx, "echo", nil)

	done := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
		done <- result
	}()
	waitCall(t, model, 0)
	if _, err := runner.Notice(ctx, service.NoticeRequest{SessionID: s.ID(), Kind: "k", Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.AbortSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	if result := settled(t, done); result.Result.Status != gotato.RunCanceled {
		t.Fatalf("the Run = %+v, want canceled", result.Result)
	}
	runOn(t, runner, s.ID(), "next")
	if got := texts(model.request(1)); got[len(got)-2] != "user:next" || got[len(got)-1] != noticeText("k", "kept") {
		t.Fatalf("request = %v, want the notice after the prompt", got)
	}
}

// Only a service's notice is durable. A Run that queued a time_away and died
// before it ended leaves no pending notice behind: the next Run works out its
// own, afresh, from the last Run that did end.
func TestATimeAwayOfACrashedRunIsNotKept(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	first, model := timeAwayRunner2(t, store, "first", clock)
	s, _ := first.CreateSession(ctx, "echo", nil)
	runOn(t, first, s.ID(), "hello")
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// A writer that queued a time_away and died: no run_ended, no release.
	dead, err := session.OpenWriter(ctx, store, s.ID(), "dead")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.Write(ctx, []gotato.Event{gotato.NoticeQueued{
		Header: gotato.NewHeader(gotato.SessionID(s.ID()), "", 0), MessageID: "stale", NoticeKind: gotato.NoticeTimeAway,
		Source: gotato.NoticeFromGotato, Text: "It has been 40 minutes since the last message. It is now stale.",
	}}); err != nil {
		t.Fatal(err)
	}

	clock.Advance(73 * time.Hour)
	second, _ := timeAwayRunner2(t, store, "second", clock)
	_ = model
	runOn(t, second, s.ID(), "back")
	record, _ := second.Record(ctx, s.ID())
	count := 0
	for _, m := range record.Messages() {
		if m.Role == gotato.RoleNotice && m.Notice.Kind == gotato.NoticeTimeAway {
			count++
			if !strings.Contains(gotato.TextOf(m), "3 days") || strings.Contains(gotato.TextOf(m), "stale") {
				t.Fatalf("the time_away = %q, want one worked out afresh", gotato.TextOf(m))
			}
		}
	}
	if count != 1 {
		t.Fatalf("%d time_away notices in the Record, want exactly 1", count)
	}
}

func timeAwayRunner2(t *testing.T, store session.Store, name string, clock *fakeClock) (*service.Runner, *testkit.FakeModel) {
	t.Helper()
	model := testkit.NewFakeModel(testkit.Text("ok"), testkit.Text("ok"), testkit.Text("ok"))
	runner, err := service.New(service.WithClock(service.Config{
		Store: store, Instance: name, Liveness: steadyLiveness,
		Admission: service.Admission{Queue: service.WaitWhileBusy},
		Agents:    []service.Agent{{Name: "echo", Model: model, Warm: service.WarmPolicy{NoIdleCompaction: true}}},
	}, clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner, model
}
