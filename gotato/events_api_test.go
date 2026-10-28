package gotato_test

import (
	"context"
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
	if rest := s.Requeue(in); len(rest) != len(in) {
		t.Fatalf("Requeue into a closed Session returned %d of %d messages as rest", len(rest), len(in))
	}
}

// appEvent is an Event defined outside the core: embedding Header is enough.
type appEvent struct{ gotato.Header }

func (appEvent) Kind() gotato.EventKind { return "app_event" }

func TestAnEventTypeOutsideTheCoreEmbedsHeader(t *testing.T) {
	var event gotato.Event = appEvent{}
	if event.Kind() != "app_event" {
		t.Fatal("kind")
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
	rest := s.Requeue([]gotato.Queued{other.Queued()})
	close(journal.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 {
		t.Fatalf("a second notice fit a queue of 1 whose place was held: rest = %d", len(rest))
	}
}
