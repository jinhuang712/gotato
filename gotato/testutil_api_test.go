package gotato_test

import (
	"context"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
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

func (j *holdJournal) rejected(why string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.events {
		if d, ok := e.(gotato.MessageRejected); ok && d.Why == why {
			return true
		}
	}
	return false
}
