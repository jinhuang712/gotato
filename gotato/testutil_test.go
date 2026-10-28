package gotato

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func storedEvent(number uint64) Event {
	return TurnStarted{Header: Header{Time: time.Now(), SessionID: "s", Number: number}}
}

// next reads one Event, failing the test when none comes.
func next(t *testing.T, sub Subscription) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

// journalTranscript is a Transcript that is a Journal: it numbers the Events
// it is given, keeps each write, and records the number of Messages committed
// before it. It fails a write that holds an Event of kind failAt with failErr.
// If hold is not nil, a write that holds a kind in holdKinds waits on it.
type journalTranscript struct {
	recordingTranscript
	mu        sync.Mutex
	writes    [][]Event
	before    []int
	last      uint64
	failAt    EventKind
	failErr   error
	hold      chan struct{}
	holdKinds map[EventKind]bool
	writing   chan struct{}
}

func (j *journalTranscript) Write(_ context.Context, events []Event) ([]Event, error) {
	committed := j.Len()
	for _, event := range events {
		if j.hold != nil && j.holdKinds[event.Kind()] {
			if j.writing != nil {
				j.writing <- struct{}{}
			}
			<-j.hold
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, event := range events {
		if j.failAt != "" && event.Kind() == j.failAt {
			return nil, j.failErr
		}
	}
	out := make([]Event, len(events))
	for i, event := range events {
		j.last++
		out[i] = WithNumber(event, j.last)
	}
	j.writes = append(j.writes, out)
	j.before = append(j.before, committed)
	return out, nil
}

func (j *journalTranscript) kinds() []EventKind {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []EventKind
	for _, write := range j.writes {
		for _, event := range write {
			out = append(out, event.Kind())
		}
	}
	return out
}

// shape names each write by the kinds it holds, joined by "+".
func (j *journalTranscript) shape() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []string
	for _, write := range j.writes {
		names := make([]string, len(write))
		for i, event := range write {
			names[i] = string(event.Kind())
		}
		out = append(out, strings.Join(names, "+"))
	}
	return out
}

func echoToolModel(calls ...string) *testModel {
	var first []ModelEvent
	for _, id := range calls {
		first = append(first, ModelEvent{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: ToolCallID(id), ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}})
	}
	first = append(first, ModelEvent{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls})
	return &testModel{scripts: [][]ModelEvent{first, {{Kind: ModelEventKindTextDelta, Text: "done"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}}
}

// recorder reads a Session's stream on its own goroutine.
type recorder struct {
	t   *testing.T
	s   *Session
	sub Subscription
	mu  sync.Mutex
	got []Event
}

func recordAll(t *testing.T, s *Session) *recorder {
	t.Helper()
	sub, err := s.Subscribe(context.Background(), SubscribeBuffer(1<<14))
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{t: t, s: s, sub: sub}
	go func() {
		for {
			event, err := sub.Next(context.Background())
			if err != nil {
				return
			}
			r.mu.Lock()
			r.got = append(r.got, event)
			r.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = sub.Close() })
	return r
}

// events returns the Events once the recorder has read all that the Session
// published.
func (r *recorder) events() []Event {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		count := uint64(len(r.got))
		r.mu.Unlock()
		if count >= r.s.Published() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.got...)
}

func (r *recorder) kinds() []EventKind {
	var out []EventKind
	for _, event := range r.events() {
		out = append(out, event.Kind())
	}
	return out
}
