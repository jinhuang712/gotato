package gotato_test

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: " + what)
	}
}

// blockedIn polls goroutine stacks until one is blocked on a mutex under a
// frame that contains name.
func blockedIn(t *testing.T, name string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		for g := range strings.SplitSeq(string(buf[:n]), "\n\n") {
			if strings.Contains(g, name) && strings.Contains(g, "sync.(*Mutex).Lock") {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("no goroutine blocked in " + name)
}

// A Steer whose message_queued write is in flight when the Session closes is
// not left in a queue nobody reads: it is dropped, and the caller is told.
func TestSteerQueuedAcrossCloseIsDropped(t *testing.T) {
	j := newHoldJournal(func(e gotato.Event) bool { _, ok := e.(gotato.MessageQueued); return ok })
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(j))
	if err != nil {
		t.Fatal(err)
	}
	res := make(chan error, 1)
	go func() { res <- s.Steer(context.Background(), gotato.UserMessage("late")) }()
	waitFor(t, j.entered, "steer write entered")
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(j.release)
	if err := <-res; !gotato.IsCode(err, gotato.CodeSessionClosed) {
		t.Fatalf("Steer across Close = %v, want session_closed", err)
	}
	if !j.dropped("session_closed") {
		t.Fatal("no message_dropped (session_closed) for the Steer")
	}
}

// The same for a Notice.
func TestNoticeQueuedAcrossCloseIsDropped(t *testing.T) {
	j := newHoldJournal(func(e gotato.Event) bool { _, ok := e.(gotato.NoticeQueued); return ok })
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(j))
	if err != nil {
		t.Fatal(err)
	}
	res := make(chan error, 1)
	go func() { _, err := s.Notice(context.Background(), "k", "late"); res <- err }()
	waitFor(t, j.entered, "notice write entered")
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(j.release)
	if err := <-res; !gotato.IsCode(err, gotato.CodeSessionClosed) {
		t.Fatalf("Notice across Close = %v, want session_closed", err)
	}
	if !j.dropped("session_closed") {
		t.Fatal("no message_dropped (session_closed) for the Notice")
	}
}

// An Interrupt that waits behind the write of run_ended is refused: it does
// not reach the next Run, whose first model call it would cancel.
func TestInterruptDuringRunEndDoesNotLeakIntoTheNextRun(t *testing.T) {
	j := newHoldJournal(func(e gotato.Event) bool { _, ok := e.(gotato.RunEnded); return ok })
	var mu sync.Mutex
	var ctxErrs []error
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		mu.Lock()
		ctxErrs = append(ctxErrs, ctx.Err())
		mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return testkit.Text("ok"), nil
	})
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(j))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	run1 := make(chan error, 1)
	go func() { _, err := s.Prompt(context.Background(), gotato.UserMessage("one")); run1 <- err }()
	waitFor(t, j.entered, "run_ended write entered")
	ierr := make(chan error, 1)
	go func() { ierr <- s.Interrupt(context.Background(), gotato.UserMessage("stale")) }()
	blockedIn(t, "(*Session).enqueueControl")
	close(j.release)
	if err := <-ierr; !gotato.IsCode(err, gotato.CodeInvalidState) {
		t.Fatalf("Interrupt in the ending window = %v, want invalid_state", err)
	}
	if err := <-run1; err != nil {
		t.Fatal(err)
	}
	if !j.dropped("run_ended") {
		t.Fatal("the refused Interrupt has no message_dropped (run_ended)")
	}
	if _, err := s.Prompt(context.Background(), gotato.UserMessage("two")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ctxErrs) != 2 || ctxErrs[1] != nil {
		t.Fatalf("the second Run's model calls saw contexts %v", ctxErrs)
	}
}

// A Steer, FollowUp or Notice over MaxMessageBytes is refused where it
// arrives, with message_rejected (too_large), and the Run it was meant for
// still completes.
func TestOversizedControlMessageIsRefusedAtTheDoor(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		once.Do(func() { close(started) })
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return testkit.Text("the answer"), nil
	})
	j := newHoldJournal(nil)
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(j), gotato.WithLimits(gotato.CoreLimits{MaxMessageBytes: 1024}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	done := make(chan gotato.RunResult, 1)
	go func() {
		r, _ := s.Prompt(context.Background(), gotato.UserMessage("hi"))
		done <- r
	}()
	waitFor(t, started, "model call started")
	big := gotato.UserMessage(strings.Repeat("x", 4096))
	for name, err := range map[string]error{
		"Steer":     s.Steer(context.Background(), big),
		"FollowUp":  s.FollowUp(context.Background(), big),
		"Interrupt": s.Interrupt(context.Background(), big),
	} {
		if !gotato.IsCode(err, gotato.CodeLimitExceeded) {
			t.Errorf("%s of 4096 bytes = %v, want limit_exceeded", name, err)
		}
	}
	if _, err := s.Notice(context.Background(), "k", strings.Repeat("x", 4096)); !gotato.IsCode(err, gotato.CodeLimitExceeded) {
		t.Errorf("Notice of 4096 bytes = %v, want limit_exceeded", err)
	}
	close(gate)
	if r := <-done; r.Status != gotato.RunStatusCompleted {
		t.Fatalf("status %s, error %v", r.Status, r.Error)
	}
	if !j.rejected("too_large") {
		t.Fatal("no message_rejected (too_large)")
	}
}

// Requeue into a Session that has closed must hand every message back as
// rest, so the host can report it, instead of holding it where nothing will
// take it.
func TestRequeueIntoAClosedSessionReturnsEveryMessage(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := []gotato.Queued{
		{Message: gotato.UserMessage("steer"), Source: gotato.MessageSourceSteer},
		{Message: gotato.UserMessage("follow"), Source: gotato.MessageSourceFollowUp},
	}
	if rest := requeue(s, in); len(rest) != len(in) {
		t.Fatalf("Requeue into a closed Session returned %d of %d messages as rest", len(rest), len(in))
	}
}

// appEvent is an Event defined outside the core: it embeds Header and states
// its own Kind and Class, and needs no MarshalJSON.
type appEvent struct {
	gotato.Header
	Tenant string `json:"tenant"`
	Count  int    `json:"count,omitempty"`
}

func (appEvent) Kind() gotato.EventKind   { return "app_event" }
func (appEvent) Class() gotato.EventClass { return gotato.EventClassInstance }

// A delta of another package says it is one, whatever its kind is called.
type appDelta struct{ gotato.Header }

func (appDelta) Kind() gotato.EventKind   { return "app_delta" }
func (appDelta) Class() gotato.EventClass { return gotato.EventClassDelta }

func TestAnEventTypeOutsideTheCoreEmbedsHeader(t *testing.T) {
	var event gotato.Event = appEvent{}
	if event.Kind() != "app_event" || event.Class() != gotato.EventClassInstance {
		t.Fatalf("kind %q class %q", event.Kind(), event.Class())
	}
	if (appDelta{}).Class() != gotato.EventClassDelta {
		t.Fatal("a type states its own class, not its kind's")
	}
}

// MarshalEvent writes the kind of any Event, with no MarshalJSON of its own,
// and UnmarshalEvent reads it back as an UnknownEvent that writes the same
// bytes again.
func TestAnEventOfAnotherPackageRoundTrips(t *testing.T) {
	original := appEvent{Header: gotato.NewHeader("s-1", "r-1", 2), Tenant: "acme", Count: 3}
	original.Number = 7
	data, err := gotato.MarshalEvent(original)
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]any
	if err := json.Unmarshal(data, &flat); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	if flat["kind"] != "app_event" || flat["tenant"] != "acme" || flat["count"] != float64(3) || flat["session_id"] != "s-1" || flat["number"] != float64(7) {
		t.Fatalf("MarshalEvent wrote %s, want a flat object with the kind first, the Header's and the type's fields", data)
	}
	if !strings.HasPrefix(string(data), `{"kind":"app_event",`) {
		t.Fatalf("the kind is not the first key: %s", data)
	}

	back, err := gotato.UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	unknown, ok := back.(gotato.UnknownEvent)
	if !ok || unknown.Kind() != "app_event" || unknown.Head().SessionID != "s-1" || unknown.Head().Number != 7 {
		t.Fatalf("UnmarshalEvent = %#v, want an UnknownEvent with the kind and the Header", back)
	}
	if unknown.Class() != "" {
		t.Fatalf("an UnknownEvent has class %q: its JSON does not say", unknown.Class())
	}
	again, err := gotato.MarshalEvent(back)
	if err != nil || string(again) != string(data) {
		t.Fatalf("round trip = %s (%v), want %s", again, err, data)
	}

	// An Event of the core still writes its own kind, once.
	core, err := gotato.MarshalEvent(gotato.TurnStarted{Header: gotato.NewHeader("s", "r", 1)})
	if err != nil || strings.Count(string(core), `"kind"`) != 1 || !strings.Contains(string(core), `"kind":"turn_started"`) {
		t.Fatalf("a core Event = %s (%v)", core, err)
	}
	if _, err := gotato.MarshalEvent(nil); err == nil {
		t.Fatal("MarshalEvent(nil) returned no error")
	}
}

// A Feed sorts an Event of another package by the class the Event states.
func TestAFeedHonoursTheClassAnEventStates(t *testing.T) {
	feed := newInstanceFeed()
	defer feed.Close()
	sub, err := feed.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	publish(feed, appEvent{Header: gotato.NewHeader("", "", 0), Tenant: "acme"})
	publish(feed, appDelta{Header: gotato.NewHeader("", "", 0)})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, want := range []gotato.EventKind{"app_event", "app_delta"} {
		event, err := sub.Next(ctx)
		if err != nil || event.Kind() != want {
			t.Fatalf("Next = %v, %v, want %s", event, err, want)
		}
	}
}

// gatedJournal is a Transcript and Journal that holds the write of a
// notice_queued Event until the test lets it go.
type gatedJournal struct {
	mu       sync.Mutex
	messages []gotato.Message
	last     uint64
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (j *gatedJournal) Len() int { j.mu.Lock(); defer j.mu.Unlock(); return len(j.messages) }
func (j *gatedJournal) Messages() []gotato.Message {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]gotato.Message(nil), j.messages...)
}
func (j *gatedJournal) Capabilities() gotato.Capabilities { return gotato.Capabilities{Journal: j} }
func (j *gatedJournal) Append(m gotato.Message) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.messages = append(j.messages, m)
	return nil
}

func (j *gatedJournal) Write(_ context.Context, events []gotato.Event) ([]gotato.Event, error) {
	for _, event := range events {
		switch event.(type) {
		case gotato.NoticeQueued:
			j.once.Do(func() {
				close(j.entered)
				<-j.release
			})
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]gotato.Event, len(events))
	for i, event := range events {
		j.last++
		out[i] = gotato.WithNumber(event, j.last)
	}
	return out, nil
}

// A Notice holds its place in the queue while its Event is written: a notice
// that arrives meanwhile, here one a Run puts back, finds the queue full. The
// check of the limit and the claim of the place are one step, not a check
// before a write and an append after it.
func TestANoticeBeingWrittenHoldsItsPlaceInTheQueue(t *testing.T) {
	journal := &gatedJournal{entered: make(chan struct{}), release: make(chan struct{})}
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(journal), gotato.WithLimits(gotato.CoreLimits{MaxNotices: 1}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	first := make(chan error, 1)
	go func() {
		_, err := s.Notice(context.Background(), "k", "first")
		first <- err
	}()
	<-journal.entered // the first notice has passed the check and waits in its write
	other := gotato.PendingNotice{MessageID: "other", Kind: "k", Source: gotato.NoticeSourceService, Text: "other"}
	rest := requeue(s, []gotato.Queued{other.Queued()})
	close(journal.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 {
		t.Fatalf("a second notice fit a queue of 1 whose place was held: rest = %d", len(rest))
	}
}
