package session_test

import (
	"context"
	"errors"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

var (
	_ gotato.Transcript = (*session.Record)(nil)
	_ gotato.Journal    = (*session.Record)(nil)
	_ gotato.Journal    = (*session.Writer)(nil)
)

// A Record is a Journal in memory: a live Session over session.New() writes
// its Events to it, and the Record folds them into Runs and usage and keeps
// them in its window, with no store.
func TestRecordIsAJournalInMemory(t *testing.T) {
	ctx := context.Background()
	r := session.New(session.WithID("mem-1"))
	if r.Head() != 0 || len(r.Events()) != 0 || len(r.Runs()) != 0 {
		t.Fatal("a new Record is not empty")
	}
	model := testkit.NewFakeModel(
		append(testkit.Script{{Kind: gotato.ModelUsage, Usage: gotato.Usage{InputTokens: 4, OutputTokens: 3, TotalTokens: 7}}}, testkit.Text("one")...),
		append(testkit.Script{{Kind: gotato.ModelUsage, Usage: gotato.Usage{InputTokens: 5, OutputTokens: 4, TotalTokens: 9}}}, testkit.Text("two")...),
	)
	live, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(r))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close(ctx) }()

	first, err := live.Prompt(ctx, gotato.UserMessage("q1"))
	if err != nil || first.Status != gotato.RunCompleted {
		t.Fatalf("Prompt = %+v, %v", first, err)
	}
	runs := r.Runs()
	if len(runs) != 1 || runs[0].RunID != first.RunID || runs[0].Status != gotato.RunCompleted || runs[0].Turns != 1 || runs[0].StartedAt.IsZero() || runs[0].EndedAt.IsZero() {
		t.Fatalf("Runs = %+v", runs)
	}
	if usage := r.Usage(); usage.TotalTokens != 7 || usage.InputTokens != 4 {
		t.Fatalf("usage = %+v", usage)
	}
	events := r.Events()
	var started, ended int
	for i, event := range events {
		if event.Head().Number != uint64(i+1) || event.Head().SessionID != "mem-1" {
			t.Fatalf("event %d = %s numbered %d for %q", i, event.Kind(), event.Head().Number, event.Head().SessionID)
		}
		switch e := event.(type) {
		case gotato.RunStarted:
			started++
			if e.RunID != first.RunID {
				t.Fatalf("run_started for %s, want %s", e.RunID, first.RunID)
			}
		case gotato.RunEnded:
			ended++
			if e.RunID != first.RunID || e.Status != gotato.RunCompleted || e.Usage.TotalTokens != 7 {
				t.Fatalf("run_ended = %+v", e)
			}
		}
		if event.Kind().Class() != gotato.ClassStored {
			t.Fatalf("the Record keeps a %s Event: %s", event.Kind().Class(), event.Kind())
		}
	}
	if started != 1 || ended != 1 {
		t.Fatalf("run_started=%d run_ended=%d in %v", started, ended, kindsOf(events))
	}

	// A second Run adds to the Runs and the usage, and the numbers go on.
	last := events[len(events)-1].Head().Number
	if _, err := live.Prompt(ctx, gotato.UserMessage("q2")); err != nil {
		t.Fatal(err)
	}
	if runs := r.Runs(); len(runs) != 2 || r.Usage().TotalTokens != 16 {
		t.Fatalf("after two Runs: %+v usage %+v", runs, r.Usage())
	}
	if all := r.Events(); all[len(all)-1].Head().Number <= last || all[len(all)-1].Kind() != gotato.KindRunEnded {
		t.Fatalf("after two Runs the last Event is %s numbered %d (was %d)", all[len(all)-1].Kind(), all[len(all)-1].Head().Number, last)
	}
	if r.Len() != 4 {
		t.Fatalf("Messages = %d, want two prompts and two replies", r.Len())
	}
	// Nothing is stored: there is no log to read.
	if r.Head() != 0 || len(session.TakePending(r)) != 0 {
		t.Fatalf("a Record without a store has Head %d", r.Head())
	}
}

// Checkpoint appends compaction_ended: the Messages the Session continues
// with, and a later load starts there, not at the first Entry.
func TestCheckpointWritesCompactionEndedAndALoadStartsThere(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(session.WithID("cp-1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three"} {
		commit(t, writer, gotato.UserMessage(text))
	}
	writer.Record().Checkpoint(gotato.CompactionBeforeUnload,
		[]gotato.Message{gotato.AssistantMessage("summary"), gotato.UserMessage("three")},
		&session.Compaction{ReplacedMessages: 2, Recap: "test"})
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("four"))

	entries, err := store.Log(ctx, "cp-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint session.Entry
	for _, entry := range entries {
		if entry.Kind == gotato.KindCompactionEnded {
			checkpoint = entry
		}
	}
	if checkpoint.Seq != 6 || !checkpoint.IsCheckpoint() || checkpoint.Layer != session.LayerFact {
		t.Fatalf("compaction_ended entry = %+v, want a fact checkpoint at 6", checkpoint)
	}
	event, _, err := session.EventOf(checkpoint, "cp-1")
	if err != nil {
		t.Fatal(err)
	}
	ended := event.(gotato.CompactionEnded)
	if ended.Why != gotato.CompactionBeforeUnload || ended.Compaction == nil || ended.Compaction.ReplacedMessages != 2 || len(ended.Messages) != 2 || len(ended.History) != 1 {
		t.Fatalf("compaction_ended = %+v", ended)
	}
	loaded, err := store.Load(ctx, "cp-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Facts[0].Seq != checkpoint.Seq || loaded.Head != 7 {
		t.Fatalf("Load starts at %d with head %d, want a load from the checkpoint at %d", loaded.Facts[0].Seq, loaded.Head, checkpoint.Seq)
	}
	reopened, err := session.Open(ctx, store, "cp-1")
	if err != nil {
		t.Fatal(err)
	}
	messages := reopened.Messages()
	if reopened.ID() != "cp-1" || len(messages) != 3 || gotato.TextOf(messages[0]) != "summary" || gotato.TextOf(messages[2]) != "four" || len(reopened.Compactions()) != 1 {
		t.Fatalf("reopened %q: %+v, %d compactions", reopened.ID(), messages, len(reopened.Compactions()))
	}
	if reopened.CreatedAt().IsZero() {
		t.Fatal("a Record loaded from a checkpoint lost its creation time")
	}
	// Everything before the checkpoint stays in the store, for View.
	view, err := session.View(ctx, store, "cp-1")
	if err != nil || view.Len() != 3 || view.Head() != 7 {
		t.Fatalf("View = %v, %v", view, err)
	}
	if kinds := storedKinds(t, store, "cp-1", 0); len(kinds) != 7 || kinds[2] != gotato.KindMessageCommitted {
		t.Fatalf("stored kinds = %v", kinds)
	}
}

type failingRecap struct{}

func (failingRecap) Name() string { return "failing" }
func (failingRecap) WriteRecap(context.Context, []gotato.Message) (gotato.Message, error) {
	return gotato.Message{}, errors.New("recap writer down")
}

// modelctx.Compact writes compaction_started first and then compaction_ended,
// or compaction_failed when its summarizer fails, which leaves the Messages as
// they were.
func TestCompactWritesItsEventsToTheStoredRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(session.WithID("cmp-1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three"} {
		commit(t, writer, gotato.UserMessage(text))
		commit(t, writer, gotato.AssistantMessage("ok "+text))
	}
	before := storedKinds(t, store, "cmp-1", 0)
	if _, err := modelctx.Compact(ctx, writer.Record(), modelctx.CompactOptions{Keep: 2, Recap: failingRecap{}}); err == nil {
		t.Fatal("Compact hid the recap writer's failure")
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if writer.Record().Len() != 6 {
		t.Fatalf("a failed compaction changed the Messages: %d", writer.Record().Len())
	}
	got := storedKinds(t, store, "cmp-1", uint64(len(before)))
	if len(got) != 2 || got[0] != gotato.KindCompactionStarted || got[1] != gotato.KindCompactionFailed {
		t.Fatalf("after a failed compaction: %v", got)
	}
	result, err := modelctx.Compact(ctx, writer.Record(), modelctx.CompactOptions{Keep: 2, Why: gotato.CompactionPastCeiling})
	if err != nil || !result.Replaced {
		t.Fatalf("Compact = %+v, %v", result, err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got = storedKinds(t, store, "cmp-1", uint64(len(before)+2))
	if len(got) != 2 || got[0] != gotato.KindCompactionStarted || got[1] != gotato.KindCompactionEnded {
		t.Fatalf("after a compaction: %v", got)
	}
	events, err := session.StoredEvents(ctx, store, "cmp-1", uint64(len(before)+2))
	if err != nil {
		t.Fatal(err)
	}
	if events[0].(gotato.CompactionStarted).Why != gotato.CompactionPastCeiling || events[1].(gotato.CompactionEnded).Why != gotato.CompactionPastCeiling {
		t.Fatalf("events = %+v", events)
	}
	loaded, err := store.Load(ctx, "cmp-1")
	if err != nil || loaded.Facts[0].Kind != gotato.KindCompactionEnded {
		t.Fatalf("a load starts at %v, %v; want the compaction_ended Checkpoint", loaded.Facts[0].Kind, err)
	}
}
