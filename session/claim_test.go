package session_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

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
