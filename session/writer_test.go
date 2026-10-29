package session_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestStaleCopyCannotOverwriteANewerRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	id := first.Record().ID()
	second, err := session.OpenWriter(ctx, store, id, "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	if claim := second.Record().Claim(); claim.Instance != "instance-b" || claim.Epoch != 2 {
		t.Fatalf("claim = %+v, want instance-b at epoch 2", claim)
	}
	commit(t, second, gotato.UserMessage("from b"))
	_ = first.Record().Append(gotato.UserMessage("from a, stale"))
	_, err = first.Write(ctx, []gotato.Event{messageEvent(gotato.UserMessage("from a, stale"))})
	if !errors.Is(err, session.ErrConflict) || !gotato.IsCode(err, gotato.CodeConflict) {
		t.Fatalf("stale Flush = %v, want a conflict", err)
	}
	if again := first.Flush(ctx); !errors.Is(again, session.ErrConflict) {
		t.Fatalf("a Writer that failed wrote again: %v", again)
	}
	stored, err := session.Open(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if messages := stored.Messages(); len(messages) != 1 || gotato.TextOf(messages[0]) != "from b" {
		t.Fatalf("stored = %+v, want only the newer writer's Message", messages)
	}
}

func TestCreateRefusesATakenID(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("taken")), "instance-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("taken")), "instance-b"); !errors.Is(err, session.ErrExists) {
		t.Fatalf("Create = %v, want ErrExists", err)
	}
}

// A new Record is busy until its Writer releases it, as one that OpenWriter
// opens is, so another Instance does not take it as idle while its first Run
// is still going. WithIdle, for a Record that nothing runs against, writes the
// release in the same write.
func TestCreateIsBusyUntilReleasedUnlessIdle(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	busy, err := session.CreateWriter(ctx, store, session.New(session.WithID("busy")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "busy"); err != nil || !loaded.Info.Busy || loaded.Head != 2 {
		t.Fatalf("a new Record = %+v, %v; want busy at head 2", loaded.Info, err)
	}
	if err := busy.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "busy"); err != nil || loaded.Info.Busy || loaded.Head != 3 {
		t.Fatalf("after Release = %+v, %v; want idle at head 3", loaded.Info, err)
	}
	if kinds := storedKinds(t, store, "busy", 0); len(kinds) != 3 || kinds[2] != gotato.EventKindSessionReleased {
		t.Fatalf("entries = %v, want session_created, session_claimed, session_released", kinds)
	}

	var published []gotato.Event
	if _, err := session.CreateWriter(ctx, store, session.New(session.WithID("idle")), "instance-a", session.WithIdle(), session.WithPublisher(func(e gotato.Event) { published = append(published, e) })); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "idle"); err != nil || loaded.Info.Busy || loaded.Head != 3 {
		t.Fatalf("an idle Record = %+v, %v; want idle at head 3", loaded.Info, err)
	}
	if len(published) != 3 || published[2].Kind() != gotato.EventKindSessionReleased || published[2].Head().Number != 3 {
		t.Fatalf("published = %v, want the three Events of the write, release last", published)
	}
}

// A Writer that released its Record can claim it again, from memory, while the
// store's head is still the Record's: the Claim keeps its Epoch, and nothing is
// loaded. Once another Instance has written, the copy is stale, and the claim
// is refused.
func TestResumeClaimsAReleasedRecordAgainWhileItsHeadIsCurrent(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	id := writer.Record().ID()
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	stale, err := writer.Stale(ctx)
	if err != nil || stale {
		t.Fatalf("Stale of a current copy = %v, %v", stale, err)
	}
	if err := writer.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if claim := writer.Record().Claim(); claim.Instance != "instance-a" || claim.Epoch != 1 {
		t.Fatalf("claim = %+v, want instance-a at epoch 1", claim)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Head != writer.Record().Head() || loaded.Info.Busy {
		t.Fatalf("store head %d busy %v, want the copy's head %d, idle", loaded.Head, loaded.Info.Busy, writer.Record().Head())
	}

	other, err := session.OpenWriter(ctx, store, id, "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if stale, err := writer.Stale(ctx); err != nil || !stale {
		t.Fatalf("Stale after another Instance wrote = %v, %v, want true", stale, err)
	}
	if err := writer.Resume(ctx); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("Resume of a stale copy = %v, want a conflict", err)
	}
	if again := writer.Resume(ctx); again == nil {
		t.Fatal("a Writer that failed resumed")
	}
}

func TestStaleOfADeletedRecordIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(), "instance-a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, writer.Record().ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Stale(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Stale of a deleted Record = %v, want not found", err)
	}
}

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

// A Checkpoint keeps the Claim: a load that starts at a compaction_ended still
// names the Writer and its Epoch, so another Instance's holder check sees a
// live holder, and a later Claim continues the Epoch.
func TestCheckpointKeepsTheClaim(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	r := session.New(session.WithMessages(gotato.UserMessage("a"), gotato.UserMessage("b")))
	if _, err := session.CreateWriter(ctx, store, r, "instance-a", session.WithIdle()); err != nil {
		t.Fatal(err)
	}
	wa, err := session.OpenWriter(ctx, store, r.ID(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	wa.Record().Checkpoint(gotato.CompactionWhyPastCeiling, []gotato.Message{gotato.UserMessage("summary")}, nil)
	if err := wa.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var seen session.Claim
	var seenBusy bool
	wb, err := session.OpenWriter(ctx, store, r.ID(), "instance-b", session.WithHolderCheck(func(_ context.Context, holder session.Claim, busy bool) error {
		seen, seenBusy = holder, busy
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if seen != (session.Claim{Instance: "instance-a", Epoch: 1}) || !seenBusy {
		t.Fatalf("the holder check saw %+v busy=%v, want instance-a at epoch 1, busy", seen, seenBusy)
	}
	if claim := wb.Record().Claim(); claim != (session.Claim{Instance: "instance-b", Epoch: 2}) {
		t.Fatalf("claim = %+v, want instance-b at epoch 2", claim)
	}
	events, err := session.StoredEvents(ctx, store, r.ID(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var claimed gotato.SessionClaimed
	for _, event := range events {
		if c, ok := event.(gotato.SessionClaimed); ok {
			claimed = c
		}
	}
	if claimed.Instance != "instance-b" || claimed.From != "instance-a" {
		t.Fatalf("last claim = %+v, want instance-b from instance-a", claimed)
	}
}

// TestReopeningByTheHolderWritesTheClaimAgain: the Instance that holds the
// Claim writes it again when it opens the Record again, with the same Epoch.
// The write marks the Record busy in the store, at the next position, so another
// Instance that claims it at the same moment loses that position, or wins it.
func TestReopeningByTheHolderWritesTheClaimAgain(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	created, err := session.CreateWriter(ctx, store, session.New(), "instance-a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	id := created.Record().ID()
	before, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := session.OpenWriter(ctx, store, id, "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if stats := reopened.Stats(); stats.Writes != 1 {
		t.Fatalf("reopening wrote %d times, want one claim", stats.Writes)
	}
	if claim := reopened.Record().Claim(); claim != (session.Claim{Instance: "instance-a", Epoch: 1}) {
		t.Fatalf("claim = %+v, want instance-a still at epoch 1", claim)
	}
	events, err := session.StoredEvents(ctx, store, id, before.Head, 0)
	if err != nil || len(events) != 1 || events[0].Head().Number != before.Head+1 {
		t.Fatalf("written after the head = %v, %v; want one claim at %d", events, err, before.Head+1)
	}
	claimed, ok := events[0].(gotato.SessionClaimed)
	if !ok || claimed.Epoch != 1 || claimed.Instance != "instance-a" || claimed.Why != gotato.ClaimWhyIdle {
		t.Fatalf("event = %+v, want session_claimed, epoch 1, idle", events[0])
	}
	after, err := store.Load(ctx, id)
	if err != nil || !after.Info.Busy {
		t.Fatalf("summary = %+v, %v; want busy", after.Info, err)
	}
	if err := reopened.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if released, err := store.Load(ctx, id); err != nil || released.Info.Busy {
		t.Fatalf("summary after Release = %+v, %v; want idle", released.Info, err)
	}
	if _, err := session.View(ctx, store, id); err != nil {
		t.Fatalf("a Record with a repeated claim does not load: %v", err)
	}
}

// Create writes the Record's state as session_created, and the first Claim as
// session_claimed: Entries 1 and 2, numbered like the Events they hold.
func TestCreateWritesSessionCreatedThenTheFirstClaim(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	record := session.New(session.WithID("c1"), session.WithMetadata(map[string]string{"app": "test"}), session.WithMessages(gotato.UserMessage("history")))
	if _, err := session.CreateWriter(ctx, store, record, "instance-a"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Log(ctx, "c1", 0, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("log = %+v, %v; want two Entries", entries, err)
	}
	if entries[0].Kind != gotato.EventKindSessionCreated || entries[1].Kind != gotato.EventKindSessionClaimed {
		t.Fatalf("kinds = %s, %s", entries[0].Kind, entries[1].Kind)
	}
	for i, entry := range entries {
		event, ok, err := session.EventOf(entry, "c1")
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if entry.Seq != uint64(i+1) || event.Head().Number != entry.Seq {
			t.Fatalf("entry %d: Seq %d, event number %d", i, entry.Seq, event.Head().Number)
		}
	}
	created, _, _ := session.EventOf(entries[0], "c1")
	if c := created.(gotato.SessionCreated); len(c.Messages) != 1 || c.Metadata["app"] != "test" {
		t.Fatalf("session_created = %+v", c)
	}
	claimed, _, _ := session.EventOf(entries[1], "c1")
	if c := claimed.(gotato.SessionClaimed); c.Instance != "instance-a" || c.Epoch != 1 || c.Why != gotato.ClaimWhyNewSession || c.From != "" {
		t.Fatalf("session_claimed = %+v", c)
	}
	if record.Head() != 2 {
		t.Fatalf("Head = %d, want 2", record.Head())
	}
}

// A Claim says why: idle when the holder had let the Session go, writer_gone
// when it had not and is not alive, waited when the caller waited for it.
func TestOpeningAnotherInstancesRecordWritesAClaimWithItsReason(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		prepare func(t *testing.T, store session.Store, id string)
		options []session.WriterOption
		why     gotato.ClaimWhy
	}{
		{
			name: "idle",
			prepare: func(t *testing.T, store session.Store, id string) {
				writer, err := session.OpenWriter(ctx, store, id, "instance-a")
				if err != nil {
					t.Fatal(err)
				}
				if err := writer.Release(ctx); err != nil {
					t.Fatal(err)
				}
			},
			why: gotato.ClaimWhyIdle,
		},
		{
			name: "writer_gone: busy holder",
			prepare: func(t *testing.T, store session.Store, id string) {
				if _, err := session.OpenWriter(ctx, store, id, "instance-a"); err != nil {
					t.Fatal(err)
				}
			},
			why: gotato.ClaimWhyWriterGone,
		},
		{
			name: "writer_gone: a Run that never ended",
			prepare: func(t *testing.T, store session.Store, id string) {
				writer, err := session.OpenWriter(ctx, store, id, "instance-a")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
					t.Fatal(err)
				}
				if err := writer.Release(ctx); err != nil {
					t.Fatal(err)
				}
			},
			why: gotato.ClaimWhyWriterGone,
		},
		{
			name: "waited",
			prepare: func(t *testing.T, store session.Store, id string) {
				writer, err := session.OpenWriter(ctx, store, id, "instance-a")
				if err != nil {
					t.Fatal(err)
				}
				if err := writer.Release(ctx); err != nil {
					t.Fatal(err)
				}
			},
			options: []session.WriterOption{session.WithWaited()},
			why:     gotato.ClaimWhyWaited,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := session.NewMemoryStore()
			created, err := session.CreateWriter(ctx, store, session.New(), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			id := created.Record().ID()
			tc.prepare(t, store, id)
			head, err := store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			second, err := session.OpenWriter(ctx, store, id, "instance-b", tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			if claim := second.Record().Claim(); claim != (session.Claim{Instance: "instance-b", Epoch: 2}) {
				t.Fatalf("claim = %+v, want instance-b at epoch 2", claim)
			}
			events, err := session.StoredEvents(ctx, store, id, head.Head, 0)
			if err != nil || len(events) == 0 {
				t.Fatalf("written = %v, %v", events, err)
			}
			claimed, ok := events[0].(gotato.SessionClaimed)
			if !ok {
				t.Fatalf("first Event written = %s, want session_claimed", events[0].Kind())
			}
			if claimed.Epoch != 2 || claimed.Instance != "instance-b" || claimed.From != "instance-a" || claimed.Why != tc.why || claimed.Number != head.Head+1 {
				t.Fatalf("session_claimed = %+v, want epoch 2 from instance-a, why %s at %d", claimed, tc.why, head.Head+1)
			}
		})
	}
}

// slowStore takes a while over every write, so that another goroutine can
// come in between the steps of one.
type slowStore struct{ *session.MemoryStore }

func (s slowStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, info session.RecordInfo) error {
	time.Sleep(time.Millisecond)
	return s.MemoryStore.Append(ctx, id, after, entries, info)
}

// Write returns the Events it was given, numbered and stored, while another
// goroutine changes the Record and flushes it: the Flush cannot take them
// between their numbering and their write.
func TestWriteReturnsItsOwnEventsWhileAnotherGoroutineFlushes(t *testing.T) {
	ctx := context.Background()
	writer, err := session.CreateWriter(ctx, slowStore{session.NewMemoryStore()}, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			writer.Record().Set("k", strconv.Itoa(i))
			if err := writer.Flush(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()
	for i := range 50 {
		message := gotato.UserMessage(strconv.Itoa(i))
		if err := writer.Append(message); err != nil {
			t.Fatal(err)
		}
		written, err := writer.Write(ctx, []gotato.Event{messageEvent(message)})
		if err != nil {
			t.Fatal(err)
		}
		var own []gotato.Event
		for _, event := range written {
			if _, ok := event.(gotato.MessageCommitted); ok {
				own = append(own, event)
			}
		}
		if len(own) != 1 || own[0].Head().Number == 0 {
			t.Fatalf("write %d returned %d of its own Events (%d in all), want 1, numbered", i, len(own), len(written))
		}
	}
}
