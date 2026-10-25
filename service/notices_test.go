package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
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
		if _, err := runner.Notice(ctx, request); !gotato.IsCode(err, gotato.ErrInvalidArgument) || !gotato.NotRun(err) {
			t.Errorf("%s: Notice = %v, want invalid_argument, not run", name, err)
		}
	}
	if _, err := runner.Notice(ctx, service.NoticeRequest{SessionID: "missing", Kind: "k", Text: "t"}); !gotato.IsCode(err, gotato.ErrNotFound) {
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
	if !gotato.IsCode(err, gotato.ErrNotWriter) || !gotato.NotRun(err) || !gotato.Retryable(err) {
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
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(), Instance: "a", Clock: clock, Liveness: fastLiveness,
		Admission: service.Admission{Queue: service.WaitWhileBusy}, Agents: []service.Agent{agent},
	})
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
	record, _ := runner.Session(ctx, s.ID())
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
