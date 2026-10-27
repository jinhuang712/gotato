package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestStaleCopyCannotOverwriteANewerRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.Create(ctx, store, session.New(), "instance-a")
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
	if !errors.Is(err, session.ErrConflict) || !gotato.IsCode(err, gotato.ErrConflict) {
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

// TestReopeningByTheHolderWritesTheClaimAgain: the Instance that holds the
// Claim writes it again when it opens the Record again, with the same Epoch.
// The write marks the Record busy in the store, at the next position, so another
// Instance that claims it at the same moment loses that position, or wins it.
func TestReopeningByTheHolderWritesTheClaimAgain(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	created, err := session.Create(ctx, store, session.New(), "instance-a", session.WithIdle())
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
	events, err := session.StoredEvents(ctx, store, id, before.Head)
	if err != nil || len(events) != 1 || events[0].Head().Number != before.Head+1 {
		t.Fatalf("written after the head = %v, %v; want one claim at %d", events, err, before.Head+1)
	}
	claimed, ok := events[0].(gotato.SessionClaimed)
	if !ok || claimed.Epoch != 1 || claimed.Instance != "instance-a" || claimed.Why != gotato.ClaimIdle {
		t.Fatalf("event = %+v, want session_claimed, epoch 1, idle", events[0])
	}
	after, err := store.Load(ctx, id)
	if err != nil || !after.Summary.Busy {
		t.Fatalf("summary = %+v, %v; want busy", after.Summary, err)
	}
	if err := reopened.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if released, err := store.Load(ctx, id); err != nil || released.Summary.Busy {
		t.Fatalf("summary after Release = %+v, %v; want idle", released.Summary, err)
	}
	if _, err := session.View(ctx, store, id); err != nil {
		t.Fatalf("a Record with a repeated claim does not load: %v", err)
	}
}

func storedKinds(t *testing.T, store session.Store, id string, after uint64) []gotato.EventKind {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, after)
	if err != nil {
		t.Fatal(err)
	}
	return kindsOf(events)
}

// Create writes the Record's state as session_created, and the first Claim as
// session_claimed: Entries 1 and 2, numbered like the Events they hold.
func TestCreateWritesSessionCreatedThenTheFirstClaim(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	record := session.New(session.WithID("c1"), session.WithMetadata(map[string]string{"app": "test"}), session.WithMessages(gotato.UserMessage("history")))
	if _, err := session.Create(ctx, store, record, "instance-a"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Log(ctx, "c1", 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("log = %+v, %v; want two Entries", entries, err)
	}
	if entries[0].Kind != gotato.KindSessionCreated || entries[1].Kind != gotato.KindSessionClaimed {
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
	if c := claimed.(gotato.SessionClaimed); c.Instance != "instance-a" || c.Epoch != 1 || c.Why != gotato.ClaimNewSession || c.From != "" {
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
			why: gotato.ClaimIdle,
		},
		{
			name: "writer_gone: busy holder",
			prepare: func(t *testing.T, store session.Store, id string) {
				if _, err := session.OpenWriter(ctx, store, id, "instance-a"); err != nil {
					t.Fatal(err)
				}
			},
			why: gotato.ClaimWriterGone,
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
			why: gotato.ClaimWriterGone,
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
			why:     gotato.ClaimWaited,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := session.NewMemoryStore()
			created, err := session.Create(ctx, store, session.New(), "instance-a")
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
			events, err := session.StoredEvents(ctx, store, id, head.Head)
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

// The publisher gets Events after their write has succeeded, in order: what
// Create, Flush and Release write, and not what a live Session writes through
// Write, which it is handed back to send itself.
func TestPublisherReceivesWhatWasWrittenAfterTheWrite(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	var published []gotato.Event
	publish := func(event gotato.Event) {
		// The Entry is in the store by now.
		entries, err := store.Log(ctx, "p1", 0)
		if err != nil || uint64(len(entries)) < event.Head().Number {
			t.Errorf("%s numbered %d was published before its write: %d Entries stored, %v", event.Kind(), event.Head().Number, len(entries), err)
		}
		published = append(published, event)
	}
	writer, err := session.Create(ctx, store, session.New(session.WithID("p1")), "instance-a", session.WithPublisher(publish))
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 2 || got[0] != gotato.KindSessionCreated || got[1] != gotato.KindSessionClaimed {
		t.Fatalf("Create published %v", got)
	}
	writer.Record().Set("agent", "support")
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 3 || got[2] != gotato.KindMetadataChanged {
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
	if _, err := session.Create(ctx, store, session.New(session.WithID("rel")), "instance-a"); err != nil {
		t.Fatal(err)
	}
	var published []gotato.Event
	writer, err := session.OpenWriter(ctx, store, "rel", "instance-a", session.WithPublisher(func(e gotato.Event) { published = append(published, e) }))
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 1 || got[0] != gotato.KindSessionClaimed || published[0].Head().Number != 3 {
		t.Fatalf("OpenWriter published %v", got)
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(published); len(got) != 2 || got[1] != gotato.KindSessionReleased || published[1].Head().Number != 4 {
		t.Fatalf("Release published %v", got)
	}
	if released := published[1].(gotato.SessionReleased); released.Instance != "instance-a" || released.Epoch != 1 {
		t.Fatalf("session_released = %+v", released)
	}
	entries, err := store.Log(ctx, "rel", 3)
	if err != nil || len(entries) != 1 || entries[0].Kind != gotato.KindSessionReleased {
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

func (s *failingAppend) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	if s.fail {
		return errors.New("disk full")
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestPublisherReceivesNothingOfAFailedWrite(t *testing.T) {
	ctx := context.Background()
	store := &failingAppend{Store: session.NewMemoryStore()}
	var published []gotato.Event
	writer, err := session.Create(ctx, store, session.New(), "instance-a", session.WithPublisher(func(e gotato.Event) { published = append(published, e) }))
	if err != nil {
		t.Fatal(err)
	}
	published = nil
	store.fail = true
	writer.Record().Set("agent", "support")
	if err := writer.Flush(ctx); err == nil || !gotato.IsCode(err, gotato.ErrStoreFailure) {
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

// Write returns the Events it wrote, numbered, and the ones the Record had
// queued before them come first.
func TestWriteReturnsQueuedEventsFirst(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(session.WithID("w1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	writer.Record().Set("agent", "support")
	events, err := writer.Write(ctx, []gotato.Event{messageEvent(gotato.UserMessage("hi")), gotato.TurnStarted{Header: header()}})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []gotato.EventKind{gotato.KindMetadataChanged, gotato.KindMessageCommitted, gotato.KindTurnStarted}
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
	writer, err := session.Create(ctx, store, session.New(session.WithID("e1")), "instance-a")
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

func TestTakeoverEndsAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write(ctx, []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}}); err != nil {
		t.Fatal(err)
	}
	commit(t, first, gotato.UserMessage("hi"))
	// instance-a stops here, and never ends its Run.
	id := first.Record().ID()
	if _, err := session.OpenWriter(ctx, store, id, "instance-b"); err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	runs := view.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunFailed || runs[0].Error != "interrupted" {
		t.Fatalf("runs = %+v, want run-1 ended as interrupted", runs)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].Runs != 1 || list[0].Running != "" {
		t.Fatalf("summary = %+v, %v; want one Run, none running", list, err)
	}
}

func TestFileStoreIgnoresUnlinkedTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := session.Create(ctx, store, session.New(session.WithID("r1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	// A crash between writing a temporary file and linking it leaves this.
	if err := os.WriteFile(filepath.Join(dir, "r1", ".tmp-12345"), []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("hi"))
	reopened, err := session.Open(ctx, store, "r1")
	if err != nil || reopened.Len() != 1 {
		t.Fatalf("Open = %v, %v", reopened, err)
	}
}

func TestCreateRefusesATakenID(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	if _, err := session.Create(ctx, store, session.New(session.WithID("taken")), "instance-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Create(ctx, store, session.New(session.WithID("taken")), "instance-b"); !errors.Is(err, session.ErrExists) {
		t.Fatalf("Create = %v, want ErrExists", err)
	}
}

// toolCallReply is an assistant reply that calls the demo tool twice.
func toolCallReply() gotato.Message {
	return gotato.Message{ID: "reply-1", Role: gotato.RoleAssistant, StopReason: gotato.StopToolCalls, ToolCalls: []gotato.ToolCall{
		{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{}`)},
		{ID: "call-2", ToolID: "demo.echo", Arguments: []byte(`{}`)},
	}}
}

func TestOpeningAnswersToolCallsLeftWithoutResult(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("go"))
	commit(t, writer, toolCallReply())
	// call-1 started, then the instance stopped: call-2 never started.
	if _, err := writer.Write(ctx, []gotato.Event{gotato.ToolStarted{Header: header(), MessageID: "reply-1", CallID: "call-1", ToolID: "demo.echo"}}); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func() (*session.Record, error){
		func() (*session.Record, error) { return session.Open(ctx, store, writer.ID()) },
		func() (*session.Record, error) { return session.View(ctx, store, writer.ID()) },
	} {
		r, err := open()
		if err != nil {
			t.Fatal(err)
		}
		messages := r.Messages()
		if len(messages) != 4 {
			t.Fatalf("messages = %+v, want both calls answered", messages)
		}
		stopped, notRun := messages[2].ToolResult, messages[3].ToolResult
		if stopped == nil || stopped.CallID != "call-1" || !stopped.Executed || stopped.Status != gotato.ToolResultFailed || stopped.SafeError != session.StoppedToolResult {
			t.Fatalf("call-1 answered with %+v", stopped)
		}
		if notRun == nil || notRun.CallID != "call-2" || notRun.Executed || notRun.SafeError != session.NotRunToolResult {
			t.Fatalf("call-2 answered with %+v", notRun)
		}
	}
	// Nothing was written for the answers: the store still holds the facts.
	loaded, err := store.Load(ctx, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range loaded.Facts {
		if entry.Kind == gotato.KindToolResultCommitted && strings.Contains(string(entry.Data), "repair-") {
			t.Fatalf("a repair was written: %s", entry.Data)
		}
	}
}

func TestViewRebuildsACutOffReplyFromItsParts(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("question"))
	for _, text := range []string{"half an ", "answer"} {
		if _, err := writer.Write(ctx, []gotato.Event{gotato.ReplyPartKept{Header: header(), MessageID: "reply-1", Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	// The instance stops mid-stream; another one takes the Session over.
	next, err := session.OpenWriter(ctx, store, writer.ID(), "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, next, gotato.UserMessage("again"))
	view, err := session.View(ctx, store, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	messages := view.Messages()
	if len(messages) != 3 || messages[1].StopReason != gotato.StopCutOff || gotato.TextOf(messages[1]) != "half an answer" || gotato.TextOf(messages[2]) != "again" {
		t.Fatalf("view = %+v, want the cut-off reply between the two prompts", messages)
	}
	facts, err := session.Open(ctx, store, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	if facts.Len() != 2 {
		t.Fatalf("facts = %+v, want the two prompts only: reply parts are log", facts.Messages())
	}
}

// A new Record is busy until its Writer releases it, as one that OpenWriter
// opens is, so another Instance does not take it as idle while its first Run
// is still going. WithIdle, for a Record that nothing runs against, writes the
// release in the same write.
func TestCreateIsBusyUntilReleasedUnlessIdle(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	busy, err := session.Create(ctx, store, session.New(session.WithID("busy")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "busy"); err != nil || !loaded.Summary.Busy || loaded.Head != 2 {
		t.Fatalf("a new Record = %+v, %v; want busy at head 2", loaded.Summary, err)
	}
	if err := busy.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "busy"); err != nil || loaded.Summary.Busy || loaded.Head != 3 {
		t.Fatalf("after Release = %+v, %v; want idle at head 3", loaded.Summary, err)
	}
	if kinds := storedKinds(t, store, "busy", 0); len(kinds) != 3 || kinds[2] != gotato.KindSessionReleased {
		t.Fatalf("entries = %v, want session_created, session_claimed, session_released", kinds)
	}

	var published []gotato.Event
	if _, err := session.Create(ctx, store, session.New(session.WithID("idle")), "instance-a", session.WithIdle(), session.WithPublisher(func(e gotato.Event) { published = append(published, e) })); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(ctx, "idle"); err != nil || loaded.Summary.Busy || loaded.Head != 3 {
		t.Fatalf("an idle Record = %+v, %v; want idle at head 3", loaded.Summary, err)
	}
	if len(published) != 3 || published[2].Kind() != gotato.KindSessionReleased || published[2].Head().Number != 3 {
		t.Fatalf("published = %v, want the three Events of the write, release last", published)
	}
}
