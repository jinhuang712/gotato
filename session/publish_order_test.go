package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Events reach the publisher in the order they were written, however many
// goroutines write: Flush publishes before it lets go of the lock that orders
// the writes.
func TestPublisherSeesEventsInWriteOrder(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	var mu sync.Mutex
	var numbers []uint64
	writer, err := session.CreateWriter(ctx, store, session.New(session.WithID("order")), "instance-a", session.WithPublisher(func(e gotato.Event) {
		mu.Lock()
		numbers = append(numbers, e.Head().Number)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	numbers = nil
	mu.Unlock()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				writer.Record().Set("k", string(rune('a'+g))+string(rune('a'+i%26)))
				if err := writer.Flush(ctx); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(numbers); i++ {
		if numbers[i] <= numbers[i-1] {
			t.Fatalf("published %d after %d", numbers[i], numbers[i-1])
		}
	}
	if len(numbers) == 0 {
		t.Fatal("nothing was published")
	}
}

// The publisher gets Events after their write has succeeded, in order: what
// Create, Flush and Release write, and not what a live Session writes through
// Write, which it is handed back to send itself.
func TestPublisherReceivesWhatWasWrittenAfterTheWrite(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	var published []gotato.Event
	publish := func(event gotato.Event) {
		// The Entry is in the store by now.
		entries, err := store.Log(ctx, "p1", 0, 0)
		if err != nil || uint64(len(entries)) < event.Head().Number {
			t.Errorf("%s numbered %d was published before its write: %d Entries stored, %v", event.Kind(), event.Head().Number, len(entries), err)
		}
		published = append(published, event)
	}
	writer, err := session.CreateWriter(ctx, store, session.New(session.WithID("p1")), "instance-a", session.WithPublisher(publish))
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 2 || got[0] != gotato.EventKindSessionCreated || got[1] != gotato.EventKindSessionClaimed {
		t.Fatalf("Create published %v", got)
	}
	writer.Record().Set("agent", "support")
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 3 || got[2] != gotato.EventKindMetadataChanged {
		t.Fatalf("Flush published %v", got)
	}
	// What a live Session writes through Write comes back to it, not to the publisher.
	events, err := writer.Write(ctx, []gotato.Event{messageEvent(gotato.UserMessage("hi"))})
	if err != nil || len(events) != 1 {
		t.Fatalf("Write = %v, %v", events, err)
	}
	if len(published) != 3 {
		t.Fatalf("Write reached the publisher: %v", kindsOf(published))
	}
	if err := writer.Flush(ctx); err != nil || len(published) != 3 {
		t.Fatalf("a Flush with nothing queued published %v, %v", kindsOf(published), err)
	}
	for i, event := range published {
		if event.Head().Number != uint64(i+1) {
			t.Fatalf("published %d is %s numbered %d, want %d", i, event.Kind(), event.Head().Number, i+1)
		}
	}
}

// Release writes session_released, and hands it to the publisher after the
// write, with the number it holds in the Record. A Release with nothing to let
// go writes nothing.
func TestReleasePublishesSessionReleased(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("rel")), "instance-a"); err != nil {
		t.Fatal(err)
	}
	var published []gotato.Event
	writer, err := session.OpenWriter(ctx, store, "rel", "instance-a", session.WithPublisher(func(e gotato.Event) { published = append(published, e) }))
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 1 || got[0] != gotato.EventKindSessionClaimed || published[0].Head().Number != 3 {
		t.Fatalf("OpenWriter published %v", got)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 2 || got[1] != gotato.EventKindSessionReleased || published[1].Head().Number != 4 {
		t.Fatalf("Release published %v", got)
	}
	if released := published[1].(gotato.SessionReleased); released.Instance != "instance-a" || released.Epoch != 1 {
		t.Fatalf("session_released = %+v", released)
	}
	entries, err := store.Log(ctx, "rel", 3, 0)
	if err != nil || len(entries) != 1 || entries[0].Kind != gotato.EventKindSessionReleased {
		t.Fatalf("stored after the claim = %+v, %v", entries, err)
	}
	if err := writer.Release(ctx); err != nil || len(published) != 2 {
		t.Fatalf("a second Release published %v, %v", kindsOf(published), err)
	}
}

// failingAppend fails every Append once armed.
type failingAppend struct {
	session.Store
	fail bool
}

func (s *failingAppend) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	if s.fail {
		return errors.New("disk full")
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestPublisherReceivesNothingOfAFailedWrite(t *testing.T) {
	ctx := context.Background()
	store := &failingAppend{Store: session.NewMemoryStore()}
	var published []gotato.Event
	writer, err := session.CreateWriter(ctx, store, session.New(), "instance-a", session.WithPublisher(func(e gotato.Event) { published = append(published, e) }))
	if err != nil {
		t.Fatal(err)
	}
	published = nil
	store.fail = true
	writer.Record().Set("agent", "support")
	if err := writer.Flush(ctx); err == nil || !gotato.IsCode(err, gotato.CodeStoreFailure) {
		t.Fatalf("Flush = %v, want a store failure", err)
	}
	if len(published) != 0 {
		t.Fatalf("an Event of a failed write was published: %v", kindsOf(published))
	}
	store.fail = false
	if err := writer.Release(ctx); err == nil || len(published) != 0 {
		t.Fatalf("a Writer that failed went on: %v, published %v", err, kindsOf(published))
	}
}
