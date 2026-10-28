package gotato_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

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
