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

// holdJournal is a Transcript and Journal that holds the write of the first
// Event for which hold says so, until release is closed.
type holdJournal struct {
	mu       sync.Mutex
	messages []gotato.Message
	events   []gotato.Event
	last     uint64
	hold     func(gotato.Event) bool
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func newHoldJournal(hold func(gotato.Event) bool) *holdJournal {
	return &holdJournal{hold: hold, entered: make(chan struct{}), release: make(chan struct{})}
}

func (j *holdJournal) Len() int { j.mu.Lock(); defer j.mu.Unlock(); return len(j.messages) }
func (j *holdJournal) Messages() []gotato.Message {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]gotato.Message(nil), j.messages...)
}
func (j *holdJournal) Append(m gotato.Message) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.messages = append(j.messages, m)
	return nil
}
func (j *holdJournal) Write(_ context.Context, events []gotato.Event) ([]gotato.Event, error) {
	for _, e := range events {
		if j.hold != nil && j.hold(e) {
			j.once.Do(func() {
				close(j.entered)
				<-j.release
			})
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]gotato.Event, len(events))
	for i, e := range events {
		j.last++
		out[i] = gotato.WithNumber(e, j.last)
	}
	j.events = append(j.events, out...)
	return out, nil
}
func (j *holdJournal) dropped(why string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.events {
		if d, ok := e.(gotato.MessageDropped); ok && d.Why == why {
			return true
		}
	}
	return false
}
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
