package gotato

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func storedEvent(number uint64) Event {
	return TurnStarted{Header: Header{Time: time.Now(), SessionID: "s", Number: number}}
}

func delta(number uint64, text string) Event {
	return TextDelta{Header: Header{Time: time.Now(), SessionID: "s", Number: number}, Text: text}
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

func numbers(t *testing.T, sub Subscription, count int) []uint64 {
	t.Helper()
	var out []uint64
	for range count {
		out = append(out, next(t, sub).Head().Number)
	}
	return out
}

// An Event is one flat JSON object whose kind names its type, and it comes back
// as the type it was.
func TestEventJSONIsFlatAndRoundTrips(t *testing.T) {
	ended := RunEnded{
		Header: Header{Time: time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC), SessionID: "s1", Number: 7, RunID: "r1", Turn: 2},
		Status: RunFailed, Cause: ErrModelFailure, Turns: 2, Usage: Usage{InputTokens: 5},
	}
	data, err := MarshalEvent(ended)
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]any
	if err := json.Unmarshal(data, &flat); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind", "time", "session_id", "number", "run_id", "turn", "status", "cause"} {
		if _, ok := flat[key]; !ok {
			t.Errorf("RunEnded JSON has no %q: %s", key, data)
		}
	}
	if flat["kind"] != "run_ended" {
		t.Fatalf("kind = %v", flat["kind"])
	}
	back, err := UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, Event(ended)) {
		t.Fatalf("round trip = %#v, want %#v", back, ended)
	}
}

// Every kind round-trips through JSON as its own type, and names itself.
func TestEveryKindDecodesToItsType(t *testing.T) {
	if len(eventDecoders) != len(eventClasses) {
		t.Fatalf("%d decoders for %d classes", len(eventDecoders), len(eventClasses))
	}
	for kind, decode := range eventDecoders {
		event, err := decode([]byte(`{"time":"2026-10-18T00:00:00Z"}`))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if event.Kind() != kind {
			t.Errorf("a %s decodes to a type that says %s", kind, event.Kind())
		}
		data, err := json.Marshal(event)
		if err != nil || !strings.Contains(string(data), `"kind":"`+string(kind)+`"`) {
			t.Errorf("%s marshals as %s (%v)", kind, data, err)
		}
		if kind.Class() == "" {
			t.Errorf("%s has no class", kind)
		}
	}
}

// A kind this version does not know is kept, not refused.
func TestUnknownKindIsKept(t *testing.T) {
	data := []byte(`{"kind":"from_the_future","time":"2026-10-18T00:00:00Z","session_id":"s","number":3,"extra":1}`)
	event, err := UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	unknown, ok := event.(UnknownEvent)
	if !ok || unknown.Kind() != "from_the_future" || unknown.Head().Number != 3 {
		t.Fatalf("event = %#v", event)
	}
	again, _ := json.Marshal(unknown)
	if string(again) != string(data) {
		t.Fatalf("an unknown Event is passed on as it was: %s", again)
	}
}

// Publish never waits for a subscriber that does not read.
func TestPublishNeverBlocks(t *testing.T) {
	feed := NewFeed(nil)
	sub, err := feed.Subscribe(context.Background(), SubscribeBuffer(4))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := uint64(1); n <= 2000; n++ {
			feed.Publish(storedEvent(n))
			feed.Publish(delta(n, "x"))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish waited for a subscriber that does not read")
	}
}

// A subscriber that falls behind stops being buffered and catches up by number:
// it sees every stored Event once, in order. Only deltas are skipped.
func TestSlowSubscriberCatchesUpFromTheRecord(t *testing.T) {
	feed := NewFeed(nil)
	sub, err := feed.Subscribe(context.Background(), SubscribeBuffer(4))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	const total = 500
	for n := uint64(1); n <= total; n++ {
		feed.Publish(storedEvent(n))
		feed.Publish(delta(n, "x"))
	}
	var stored []uint64
	deadline := time.After(10 * time.Second)
	for len(stored) < total {
		select {
		case <-deadline:
			t.Fatalf("saw %d of %d stored Events", len(stored), total)
		default:
		}
		event := next(t, sub)
		if event.Kind().Class() == ClassStored {
			stored = append(stored, event.Head().Number)
		}
	}
	for i, number := range stored {
		if number != uint64(i+1) {
			t.Fatalf("stored Event %d has number %d: a gap or a duplicate", i, number)
		}
	}
}

// A Session's stream starts after any number: first the Events the source
// keeps, then the live ones, without a gap or a duplicate.
func TestSubscribeAfterReadsTheRecordThenGoesLive(t *testing.T) {
	feed := NewFeed(nil)
	for n := uint64(1); n <= 5; n++ {
		feed.Publish(storedEvent(n))
	}
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if got := numbers(t, sub, 3); !reflect.DeepEqual(got, []uint64{3, 4, 5}) {
		t.Fatalf("catch-up = %v, want 3 4 5", got)
	}
	feed.Publish(storedEvent(6))
	feed.Publish(storedEvent(6)) // a duplicate is ignored
	feed.Publish(storedEvent(7))
	if got := numbers(t, sub, 2); !reflect.DeepEqual(got, []uint64{6, 7}) {
		t.Fatalf("live = %v, want 6 7", got)
	}
}

type sliceSource struct {
	mu     sync.Mutex
	events []Event
}

func (s *sliceSource) add(event Event) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *sliceSource) EventsAfter(_ context.Context, after uint64, _ int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, event := range s.events {
		if event.Head().Number > after {
			out = append(out, event)
		}
	}
	return out, nil
}

// Catch-up reads from the Feed's source, when it has one.
func TestCatchUpReadsFromTheSource(t *testing.T) {
	source := &sliceSource{}
	feed := NewFeed(source)
	for n := uint64(1); n <= 50; n++ {
		source.add(storedEvent(n)) // written first, then sent
		feed.Publish(storedEvent(n))
	}
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(10))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	got := numbers(t, sub, 40)
	if got[0] != 11 || got[39] != 50 {
		t.Fatalf("catch-up from the source = %v", got)
	}
}

// The Instance's stream has no number: a slow subscriber skips Events and is
// told how many.
func TestInstanceFeedSaysHowManyWereSkipped(t *testing.T) {
	feed := NewInstanceFeed()
	sub, err := feed.Subscribe(context.Background(), SubscribeBuffer(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	for range 10 {
		feed.Publish(storedEvent(0))
	}
	var skipped uint64
	seen := 0
	for seen+int(skipped) < 10 {
		event := next(t, sub)
		if gap, ok := event.(EventsSkipped); ok {
			skipped += gap.Skipped
			continue
		}
		seen++
	}
	if skipped == 0 || seen+int(skipped) != 10 {
		t.Fatalf("seen %d, skipped %d", seen, skipped)
	}
}

// A Run's stream carries one Run, from its RunStarted to its RunEnded, and ends.
func TestRunStreamEndsWithTheRun(t *testing.T) {
	feed := NewFeed(nil)
	sub, err := feed.Subscribe(context.Background(), NextRun()...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	feed.Publish(TurnStarted{Header: Header{Number: 1}}) // before the Run: not in its stream
	feed.Publish(RunStarted{Header: Header{Number: 2, RunID: "r1"}})
	feed.Publish(TurnStarted{Header: Header{Number: 3, RunID: "r1"}})
	feed.Publish(TurnStarted{Header: Header{Number: 4, RunID: "other"}})
	feed.Publish(RunEnded{Header: Header{Number: 5, RunID: "r1"}})
	feed.Publish(RunStarted{Header: Header{Number: 6, RunID: "r2"}})
	if got := numbers(t, sub, 3); !reflect.DeepEqual(got, []uint64{2, 3, 5}) {
		t.Fatalf("the Run's stream = %v, want 2 3 5", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("after RunEnded, Next = %v, want io.EOF", err)
	}
}

// End lets a subscriber read what is queued, and then ends the stream.
func TestEndDrainsTheQueue(t *testing.T) {
	feed := NewFeed(nil)
	sub, err := feed.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(1); n <= 3; n++ {
		feed.Publish(storedEvent(n))
	}
	sub.End()
	if got := numbers(t, sub, 3); !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("drained = %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after End = %v, want io.EOF", err)
	}
}

// A subscriber needs Events its source cannot give: the stream ends with an
// error instead of waiting for ever.
func TestMissingEventsEndTheStream(t *testing.T) {
	feed := NewFeed(&sliceSource{})
	feed.Publish(storedEvent(1))
	feed.Publish(storedEvent(2))
	sub, err := feed.Subscribe(context.Background(), SubscribeAfter(0))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, ErrEventsLost) {
		t.Fatalf("Next = %v, want ErrEventsLost", err)
	}
}
