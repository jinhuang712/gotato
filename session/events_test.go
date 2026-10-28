package session_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// Write returns the Events it wrote, numbered, and the ones the Record had
// queued before them come first.
func TestWriteReturnsQueuedEventsFirst(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(session.WithID("w1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	writer.Record().Set("agent", "support")
	events, err := writer.Write(ctx, []gotato.Event{messageEvent(gotato.UserMessage("hi")), gotato.TurnStarted{Header: header()}})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []gotato.EventKind{gotato.EventKindMetadataChanged, gotato.EventKindMessageCommitted, gotato.EventKindTurnStarted}
	if got := kindsOf(events); len(got) != 3 || got[0] != wantKinds[0] || got[1] != wantKinds[1] || got[2] != wantKinds[2] {
		t.Fatalf("Write returned %v, want %v", got, wantKinds)
	}
	for i, event := range events {
		if event.Head().Number != uint64(3+i) || event.Head().SessionID != "w1" {
			t.Fatalf("event %d = %s numbered %d for %q, want number %d", i, event.Kind(), event.Head().Number, event.Head().SessionID, 3+i)
		}
	}
	if got := storedKinds(t, store, "w1", 2); len(got) != 3 || got[0] != wantKinds[0] || got[2] != wantKinds[2] {
		t.Fatalf("stored %v", got)
	}
}

func TestEventsAfterReadsStoredEventsByNumber(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(session.WithID("e1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("one"))
	commit(t, writer, gotato.AssistantMessage("two"))
	all, err := writer.EventsAfter(ctx, 0, 0)
	if err != nil || len(all) != 4 {
		t.Fatalf("EventsAfter(0) = %v, %v", kindsOf(all), err)
	}
	after, err := writer.EventsAfter(ctx, 2, 0)
	if err != nil || len(after) != 2 || after[0].Head().Number != 3 || after[1].Head().Number != 4 {
		t.Fatalf("EventsAfter(2) = %v, %v", kindsOf(after), err)
	}
	if m, ok := after[0].(gotato.MessageCommitted); !ok || gotato.TextOf(m.Message) != "one" {
		t.Fatalf("event 3 = %+v", after[0])
	}
	if _, ok := after[1].(gotato.ReplyEnded); !ok {
		t.Fatalf("event 4 = %+v", after[1])
	}
	none, err := writer.EventsAfter(ctx, 4, 0)
	if err != nil || len(none) != 0 {
		t.Fatalf("EventsAfter(4) = %v, %v", kindsOf(none), err)
	}
}

func turnStarted() gotato.Event { return gotato.TurnStarted{Header: header()} }

func TestEventLimitBoundsRetention(t *testing.T) {
	s := session.New(session.WithEventLimit(3))
	for range 10 {
		if _, err := s.Write(context.Background(), []gotato.Event{turnStarted()}); err != nil {
			t.Fatal(err)
		}
	}
	events := s.Events()
	if len(events) != 3 || events[0].Head().Number != 8 || events[2].Head().Number != 10 {
		t.Fatalf("events = %+v, want numbers 8 to 10", events)
	}
}

func TestEventLimitZeroKeepsDefault(t *testing.T) {
	s := session.New(session.WithEventLimit(0))
	if _, err := s.Write(context.Background(), []gotato.Event{turnStarted()}); err != nil {
		t.Fatal(err)
	}
	if len(s.Events()) != 1 {
		t.Fatalf("zero limit retained %d events, want 1", len(s.Events()))
	}
	disabled := session.New(session.WithEventLimit(-1))
	if _, err := disabled.Write(context.Background(), []gotato.Event{turnStarted()}); err != nil {
		t.Fatal(err)
	}
	if len(disabled.Events()) != 0 {
		t.Fatalf("negative limit retained %d events", len(disabled.Events()))
	}
}

// The Events a Record returns are a copy of its window: a caller that edits
// the slice does not change what the Record keeps.
func TestEventsReturnsACopyOfTheWindow(t *testing.T) {
	s := session.New()
	if _, err := s.Write(context.Background(), []gotato.Event{turnStarted(), gotato.TurnEnded{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	events := s.Events()
	events[0] = gotato.RunEnded{Header: header()}
	if kinds := kindsOf(s.Events()); len(kinds) != 2 || kinds[0] != gotato.EventKindTurnStarted || kinds[1] != gotato.EventKindTurnEnded {
		t.Fatalf("a caller's edit reached the Record's window: %v", kinds)
	}
}

// Events keep their own typed fields: a RunEnded read back holds what was
// written, whatever the caller does to its own copy of the value afterwards.
func TestEventsKeepTheirFields(t *testing.T) {
	s := session.New()
	ended := gotato.RunEnded{Header: gotato.NewHeader("", "run-1", 0), Status: gotato.RunStatusCompleted, Turns: 2, Usage: gotato.Usage{TotalTokens: 9}}
	if _, err := s.Write(context.Background(), []gotato.Event{ended}); err != nil {
		t.Fatal(err)
	}
	ended.Turns = 99
	got, ok := s.Events()[0].(gotato.RunEnded)
	if !ok || got.Turns != 2 || got.Usage.TotalTokens != 9 || got.RunID != "run-1" {
		t.Fatalf("event = %+v", s.Events()[0])
	}
}

func TestRingBufferKeepsNewestInOrder(t *testing.T) {
	s := session.New(session.WithEventLimit(2))
	for i := range 5 {
		hd := header()
		hd.Turn = gotato.TurnNumber(i)
		if _, err := s.Write(context.Background(), []gotato.Event{gotato.TurnStarted{Header: hd}}); err != nil {
			t.Fatal(err)
		}
	}
	events := s.Events()
	if len(events) != 2 || events[0].Head().Number != 4 || events[1].Head().Number != 5 || events[0].Head().Turn != 3 || events[1].Head().Turn != 4 {
		t.Fatalf("events = %+v", events)
	}
}

// A Session over a Record that is not stored can read again only the Events its
// window holds. A stream that starts before the window says so, with
// ErrEventsLost, where it used to start at the window as if nothing was missing.
func TestStreamBeforeTheWindowOfAnUnstoredRecordIsLost(t *testing.T) {
	ctx := context.Background()
	r := session.New()
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(r))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx) }()
	for i := range 200 {
		if _, err := s.Prompt(ctx, gotato.UserMessage(fmt.Sprintf("p%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := s.Subscribe(ctx, gotato.SubscribeAfter(0))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if event, err := sub.Next(read); !errors.Is(err, gotato.ErrEventsLost) {
		t.Fatalf("Subscribe(SubscribeAfter(0)) after the window = %v, %v; want ErrEventsLost", event, err)
	}
	// A stream that starts inside the window is fine.
	events := r.Events()
	from := events[len(events)/2].Head().Number
	inside, err := s.Subscribe(ctx, gotato.SubscribeAfter(from))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inside.Close() }()
	if event, err := inside.Next(read); err != nil || event.Head().Number != from+1 {
		t.Fatalf("Subscribe(SubscribeAfter(%d)) = %v, %v; want %d", from, event, err, from+1)
	}
}
