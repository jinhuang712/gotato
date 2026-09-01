package orchestration

import (
	"context"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
)

func TestConversationSurvivesAProcessRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSnapshotStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := testOrchestrator(t, WithSnapshotStore(store))
	request := Request{AgentName: "default", ConversationKey: "durable"}
	if _, _, err := first.Dispatch(context.Background(), request, gotato.UserMessage("remember this")); err != nil {
		t.Fatal(err)
	}
	_, record, err := first.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Retire(context.Background(), record.ID, Retain); err != nil {
		t.Fatal(err)
	}

	// A second Orchestrator stands in for the restarted process: it shares
	// nothing with the first except the directory on disk.
	reopened, err := NewFileSnapshotStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	second := testOrchestrator(t, WithSnapshotStore(reopened))
	restored, err := second.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatalf("restored Conversations = %d", restored)
	}
	dormant, ok := second.Get(record.ID)
	if !ok || dormant.Status != ConversationDormant || dormant.LiveAgentID != "" {
		t.Fatalf("restored record = %+v", dormant)
	}

	agent, revived, err := second.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if revived.ID != record.ID {
		t.Fatalf("ConversationID changed across the restart: %s then %s", record.ID, revived.ID)
	}
	if revived.Generation != record.Generation+1 {
		t.Fatalf("generation after restart = %d, want %d", revived.Generation, record.Generation+1)
	}
	snapshot, err := agent.(gotato.Snapshotter).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range snapshot.Messages {
		if strings.Contains(gotato.TextOf(message), "remember this") {
			found = true
		}
	}
	if !found {
		t.Fatalf("transcript did not survive the restart: %+v", snapshot.Messages)
	}
}

func TestFileStoreRejectsAStaleWrite(t *testing.T) {
	store, err := NewFileSnapshotStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := StoredState{Record: Record{ID: "conv-1", AgentName: "default", StateVersion: 2}}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	stale := state
	stale.Record.StateVersion = 1
	if err := store.Save(context.Background(), stale); !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("stale write = %v", err)
	}
	same := state
	if err := store.Save(context.Background(), same); !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("rewrite at the same version = %v", err)
	}
	newer := state
	newer.Record.StateVersion = 3
	if err := store.Save(context.Background(), newer); err != nil {
		t.Fatalf("advancing write = %v", err)
	}
	loaded, ok, err := store.Load(context.Background(), "conv-1")
	if err != nil || !ok || loaded.Record.StateVersion != 3 {
		t.Fatalf("loaded = %+v ok=%v err=%v", loaded, ok, err)
	}
}

func TestMemoryStoreRejectsAStaleWrite(t *testing.T) {
	store := NewMemorySnapshotStore()
	state := StoredState{Record: Record{ID: "conv-1", StateVersion: 2}}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	stale := state
	stale.Record.StateVersion = 1
	if err := store.Save(context.Background(), stale); !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("stale write = %v", err)
	}
}

func TestFileStoreBoundsOneState(t *testing.T) {
	store, err := NewFileSnapshotStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.SetMaxBytes(64)
	state := StoredState{
		Record:   Record{ID: "conv-big", StateVersion: 1},
		Snapshot: gotato.CoreSnapshot{Messages: []gotato.Message{gotato.UserMessage(strings.Repeat("x", 4096))}},
	}
	if err := store.Save(context.Background(), state); !gotato.IsCode(err, gotato.ErrLimitExceeded) {
		t.Fatalf("oversized state = %v", err)
	}
	if ids, err := store.List(context.Background()); err != nil || len(ids) != 0 {
		t.Fatalf("rejected state left a file: %v %v", ids, err)
	}
}

func TestRestoreReportsAMissingDefinition(t *testing.T) {
	store := NewMemorySnapshotStore()
	if err := store.Save(context.Background(), StoredState{Record: Record{ID: "conv-x", AgentName: "gone", StateVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	o := testOrchestrator(t, WithSnapshotStore(store))
	restored, err := o.Restore(context.Background())
	if !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("restore with an unknown definition = %v", err)
	}
	if restored != 0 {
		t.Fatalf("restored = %d", restored)
	}
	if _, ok := o.Get("conv-x"); ok {
		t.Fatal("an unservable Conversation was installed anyway")
	}
}
