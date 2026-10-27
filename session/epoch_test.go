package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Record's epoch is its identity as a log: it is chosen when the Record is
// created, it is the same for every Instance and every load, also one that
// starts at a Checkpoint after a compaction, and it changes only when the
// Record is deleted and created again under the same ID. A stream resumes from
// a position only when its client's epoch is the Record's.
func TestRecordEpochIsStableAcrossLoadsAndChangesWithANewRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	created, err := session.CreateWriter(ctx, store, session.New(session.WithID("same")), "a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	epoch := created.Record().RecordEpoch()
	if epoch == "" {
		t.Fatal("a new Record has no epoch")
	}

	// It is in the first Entry, session_created, and in the RecordInfo.
	entries, err := store.Log(ctx, "same", 0)
	if err != nil {
		t.Fatal(err)
	}
	first, err := gotato.UnmarshalEvent(entries[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.(gotato.SessionCreated).RecordEpoch; got != epoch {
		t.Fatalf("session_created holds epoch %q, the Record %q", got, epoch)
	}
	if !strings.Contains(string(entries[0].Data), `"record_epoch":"`+epoch+`"`) {
		t.Fatalf("session_created JSON = %s", entries[0].Data)
	}
	loaded, err := store.Load(ctx, "same")
	if err != nil || loaded.Info.RecordEpoch != epoch {
		t.Fatalf("RecordInfo epoch = %q, %v; want %q", loaded.Info.RecordEpoch, err, epoch)
	}

	// Another Instance opens it, and writes to it: same epoch.
	other, err := session.OpenWriter(ctx, store, "same", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got := other.Record().RecordEpoch(); got != epoch {
		t.Fatalf("a second Instance reads epoch %q, want %q", got, epoch)
	}

	// A compaction makes the load start at a Checkpoint that is not
	// session_created: the epoch still comes with it.
	other.Record().Checkpoint(gotato.CompactionWhyOnDemand, []gotato.Message{gotato.UserMessage("summary")}, nil)
	if err := other.Release(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(ctx, store, "same")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.RecordEpoch(); got != epoch {
		t.Fatalf("after a compaction the epoch is %q, want %q", got, epoch)
	}

	// Deleted and created again under the same ID: a different Record.
	if err := store.Delete(ctx, "same"); err != nil {
		t.Fatal(err)
	}
	again, err := session.CreateWriter(ctx, store, session.New(session.WithID("same")), "a", session.WithIdle())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Record().RecordEpoch(); got == "" || got == epoch {
		t.Fatalf("a new Record under the same ID has epoch %q, the old one %q", got, epoch)
	}

	// A fork is a Record of its own.
	fork := session.Fork(again.Record())
	if fork.RecordEpoch() == again.Record().RecordEpoch() {
		t.Fatal("a fork shares its parent's epoch")
	}
}
