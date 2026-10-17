package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

func fixedClock() func() time.Time {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

// sameState compares two Records' documents, which is everything a Record
// holds.
func sameState(t *testing.T, got, want *session.Record) {
	t.Helper()
	g, _ := json.Marshal(got.Snapshot())
	w, _ := json.Marshal(want.Snapshot())
	if string(g) != string(w) {
		t.Fatalf("rebuilt Record differs\n got: %s\nwant: %s", g, w)
	}
}

func layers(entries []session.Entry) map[session.EntryKind]session.Layer {
	out := map[session.EntryKind]session.Layer{}
	for _, entry := range entries {
		out[entry.Kind] = entry.Layer
	}
	return out
}

func TestEntriesRebuildEveryChange(t *testing.T) {
	r := session.New(session.WithClock(fixedClock()), session.WithMessages(gotato.UserMessage("history")))
	session.StartLog(r)
	var entries []session.Entry
	entries = append(entries, session.TakePending(r)...)

	_ = r.Append(gotato.UserMessage("hi"))
	r.Set("agent", "support")
	r.RecordRunStart("run-1", time.Date(2026, 10, 17, 12, 0, 1, 0, time.UTC))
	r.RecordEvent(gotato.Event{Kind: gotato.EventRunStart, Class: gotato.EventProtected, RunID: "run-1"})
	_ = r.Append(gotato.AssistantMessage("hello"))
	r.RecordRunEnd("run-1", gotato.RunCompleted, "", "", 1, 0, gotato.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}, time.Date(2026, 10, 17, 12, 0, 2, 0, time.UTC))
	r.Set("agent", "")
	entries = append(entries, session.TakePending(r)...)

	for i, entry := range entries {
		if entry.Seq != uint64(i+1) {
			t.Fatalf("entry %d has Seq %d", i, entry.Seq)
		}
	}
	got := layers(entries)
	want := map[session.EntryKind]session.Layer{
		session.EntryCheckpoint: session.LayerFact, session.EntryLogSnapshot: session.LayerLog,
		session.EntryMessage: session.LayerFact, session.EntryMetadata: session.LayerFact,
		session.EntryRunStart: session.LayerLog, session.EntryRunEnd: session.LayerLog, session.EntryEvents: session.LayerLog,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entry layers = %v, want %v", got, want)
	}
	rebuilt, err := session.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	sameState(t, rebuilt, r)
	if rebuilt.Head() != uint64(len(entries)) {
		t.Fatalf("Head = %d, want %d", rebuilt.Head(), len(entries))
	}
}

func TestFactsAloneRebuildTheTranscript(t *testing.T) {
	r := session.New(session.WithClock(fixedClock()))
	session.StartLog(r)
	_ = r.Append(gotato.UserMessage("hi"))
	r.RecordRunStart("run-1", time.Now())
	r.Set("k", "v")
	_ = r.Append(gotato.AssistantMessage("hello"))
	entries := session.TakePending(r)
	var facts []session.Entry
	for _, entry := range entries {
		if entry.Layer == session.LayerFact {
			facts = append(facts, entry)
		}
	}
	rebuilt, err := session.FromEntries(facts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuilt.Messages(), r.Messages()) || !reflect.DeepEqual(rebuilt.Metadata(), r.Metadata()) {
		t.Fatalf("facts rebuilt %+v / %v", rebuilt.Messages(), rebuilt.Metadata())
	}
	if len(rebuilt.Runs()) != 0 {
		t.Fatalf("facts alone rebuilt Runs: %+v", rebuilt.Runs())
	}
}

func TestCheckpointStandsInForEverythingBefore(t *testing.T) {
	r := session.New(session.WithClock(fixedClock()))
	session.StartLog(r)
	for _, text := range []string{"one", "two", "three"} {
		_ = r.Append(gotato.UserMessage(text))
	}
	summary := gotato.AssistantMessage("summary of one and two")
	r.Checkpoint([]gotato.Message{summary, gotato.UserMessage("three")}, &session.Compaction{ReplacedMessages: 2, Summarizer: "test"})
	_ = r.Append(gotato.UserMessage("four"))
	entries := session.TakePending(r)

	last := -1
	for i, entry := range entries {
		if entry.Kind == session.EntryCheckpoint {
			last = i
		}
	}
	if last <= 0 {
		t.Fatalf("no second checkpoint in %+v", entries)
	}
	fromCheckpoint, err := session.FromEntries(entries[last:])
	if err != nil {
		t.Fatal(err)
	}
	whole, err := session.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, rebuilt := range []*session.Record{fromCheckpoint, whole} {
		messages := rebuilt.Messages()
		if len(messages) != 3 || gotato.TextOf(messages[0]) != "summary of one and two" || gotato.TextOf(messages[2]) != "four" {
			t.Fatalf("rebuilt Messages = %+v", messages)
		}
		if len(rebuilt.Compactions()) != 1 {
			t.Fatalf("rebuilt Compactions = %+v", rebuilt.Compactions())
		}
	}
}

func TestCompactionAppendsOneCheckpoint(t *testing.T) {
	r := session.New()
	for _, text := range []string{"one", "two", "three", "four"} {
		_ = r.Append(gotato.UserMessage(text))
		_ = r.Append(gotato.AssistantMessage("ok " + text))
	}
	session.StartLog(r)
	session.TakePending(r)
	result, err := modelctx.Compact(context.Background(), r, modelctx.CompactOptions{Keep: 2})
	if err != nil || !result.Replaced {
		t.Fatalf("Compact = %+v, %v", result, err)
	}
	checkpoints := 0
	for _, entry := range session.TakePending(r) {
		if entry.Kind == session.EntryCheckpoint {
			checkpoints++
		}
		if entry.Kind == session.EntryMessage {
			t.Fatalf("compaction rewrote Messages one by one: %+v", entry)
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoints = %d, want 1", checkpoints)
	}
}

func TestUnknownEntryKindIsRefused(t *testing.T) {
	r := session.New()
	session.StartLog(r)
	entries := session.TakePending(r)
	entries = append(entries, session.Entry{Seq: uint64(len(entries) + 1), Kind: "from_the_future", Layer: session.LayerFact, Data: json.RawMessage(`{}`)})
	_, err := session.FromEntries(entries)
	var runtimeErr *gotato.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != gotato.ErrNotSupported {
		t.Fatalf("FromEntries = %v, want not_supported", err)
	}
}

func TestALogMustStartAtACheckpointInOrder(t *testing.T) {
	r := session.New()
	session.StartLog(r)
	_ = r.Append(gotato.UserMessage("hi"))
	entries := session.TakePending(r)
	if _, err := session.FromEntries(entries[1:]); err == nil {
		t.Fatal("FromEntries accepted a log that does not start at a checkpoint")
	}
	swapped := append([]session.Entry{entries[0]}, entries[2], entries[1])
	if _, err := session.FromEntries(swapped); err == nil {
		t.Fatal("FromEntries accepted a log out of order")
	}
}

func TestUnstoredRecordKeepsNoPending(t *testing.T) {
	r := session.New()
	for range 100 {
		_ = r.Append(gotato.UserMessage("hi"))
		r.RecordEvent(gotato.Event{Kind: gotato.EventRunStart, Class: gotato.EventProtected})
	}
	if entries := session.TakePending(r); len(entries) != 0 {
		t.Fatalf("a Record never stored queued %d Entries", len(entries))
	}
}

func TestSetSameValueAppendsNothing(t *testing.T) {
	r := session.New(session.WithMetadata(map[string]string{"agent": "support"}))
	session.StartLog(r)
	session.TakePending(r)
	r.Set("agent", "support")
	r.Set("absent", "")
	if entries := session.TakePending(r); len(entries) != 0 {
		t.Fatalf("Set without a change queued %+v", entries)
	}
	r.Set("agent", "sales")
	if entries := session.TakePending(r); len(entries) != 1 || entries[0].Kind != session.EntryMetadata {
		t.Fatalf("Set with a change queued %+v", entries)
	}
}

func TestRecorderKeepsNoCoalescableEvents(t *testing.T) {
	r := session.New()
	recorder := session.NewRecorder(r)
	_ = recorder.Observe(context.Background(), gotato.Event{Kind: gotato.EventMessageUpdate, Class: gotato.EventCoalescable, Payload: map[string]any{"text": "he"}})
	_ = recorder.Observe(context.Background(), gotato.Event{Kind: gotato.EventMessageEnd, Class: gotato.EventProtected})
	events := r.Events()
	if len(events) != 1 || events[0].Kind != gotato.EventMessageEnd {
		t.Fatalf("retained Events = %+v, want message_end only", events)
	}
}

func TestForkRecordsWhereItBranched(t *testing.T) {
	parent := session.New()
	session.StartLog(parent)
	_ = parent.Append(gotato.UserMessage("hi"))
	head := session.TakePending(parent)
	child := session.Fork(parent)
	session.StartLog(child)
	entries := session.TakePending(child)
	var checkpoint session.Checkpoint
	if err := json.Unmarshal(entries[0].Data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.ParentID != parent.ID() || checkpoint.ParentSeq != head[len(head)-1].Seq || len(checkpoint.Messages) != 1 {
		t.Fatalf("fork checkpoint = %+v", checkpoint)
	}
}
