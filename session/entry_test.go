package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

func fixedClock() func() time.Time {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

// sameState compares what two Records hold: Messages, metadata, Runs, usage,
// compaction history and the numbers and kinds of the Events they keep.
func sameState(t *testing.T, got, want *session.Record) {
	t.Helper()
	view := func(r *session.Record) string {
		type eventView struct {
			Kind   gotato.EventKind
			Number uint64
		}
		var events []eventView
		for _, event := range r.Events() {
			events = append(events, eventView{event.Kind(), event.Head().Number})
		}
		data, _ := json.Marshal(map[string]any{
			"id": r.ID(), "messages": r.Messages(), "metadata": r.Metadata(), "runs": r.Runs(),
			"usage": r.Usage(), "compactions": r.Compactions(), "events": events,
		})
		return string(data)
	}
	if g, w := view(got), view(want); g != w {
		t.Fatalf("rebuilt Record differs\n got: %s\nwant: %s", g, w)
	}
}

func layers(entries []session.Entry) map[gotato.EventKind]session.Layer {
	out := map[gotato.EventKind]session.Layer{}
	for _, entry := range entries {
		out[entry.Kind] = entry.Layer
	}
	return out
}

func runEvent(kind string, runID gotato.RunID, at time.Time) gotato.Event {
	hd := gotato.Header{Time: at, RunID: runID}
	if kind == "start" {
		return gotato.RunStarted{Header: hd}
	}
	return gotato.RunEnded{Header: hd, Status: gotato.RunCompleted, Turns: 1, Usage: gotato.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
}

func TestEntriesRebuildEveryChange(t *testing.T) {
	ctx := context.Background()
	r := session.New(session.WithClock(fixedClock()), session.WithMessages(gotato.UserMessage("history")))
	session.StartLog(r)
	var entries []session.Entry
	entries = append(entries, session.TakePending(r)...)

	commit(t, r, gotato.UserMessage("hi"))
	r.Set("agent", "support")
	if _, err := r.Write(ctx, []gotato.Event{runEvent("start", "run-1", time.Date(2026, 10, 17, 12, 0, 1, 0, time.UTC))}); err != nil {
		t.Fatal(err)
	}
	commit(t, r, gotato.AssistantMessage("hello"))
	if _, err := r.Write(ctx, []gotato.Event{runEvent("end", "run-1", time.Date(2026, 10, 17, 12, 0, 2, 0, time.UTC))}); err != nil {
		t.Fatal(err)
	}
	r.Set("agent", "")
	entries = append(entries, session.TakePending(r)...)

	for i, entry := range entries {
		if entry.Seq != uint64(i+1) {
			t.Fatalf("entry %d has Seq %d", i, entry.Seq)
		}
	}
	got := layers(entries)
	want := map[gotato.EventKind]session.Layer{
		gotato.KindSessionCreated: session.LayerFact, gotato.KindMessageCommitted: session.LayerFact,
		gotato.KindMetadataChanged: session.LayerFact, gotato.KindReplyEnded: session.LayerFact,
		gotato.KindRunStarted: session.LayerLog, gotato.KindRunEnded: session.LayerLog,
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
	if runs := rebuilt.Runs(); len(runs) != 1 || runs[0].Status != gotato.RunCompleted || rebuilt.Usage().TotalTokens != 12 {
		t.Fatalf("rebuilt Runs = %+v usage = %+v", runs, rebuilt.Usage())
	}
	// The window of Events a rebuilt Record keeps is typed and numbered by position.
	for i, event := range rebuilt.Events() {
		if event.Head().Number != uint64(i+1) || event.Head().SessionID != gotato.SessionID(r.ID()) {
			t.Fatalf("event %d = %s numbered %d for session %q", i, event.Kind(), event.Head().Number, event.Head().SessionID)
		}
	}
}

func TestFactsAloneRebuildTheTranscript(t *testing.T) {
	r := session.New(session.WithClock(fixedClock()))
	session.StartLog(r)
	commit(t, r, gotato.UserMessage("hi"))
	if _, err := r.Write(context.Background(), []gotato.Event{runEvent("start", "run-1", time.Now())}); err != nil {
		t.Fatal(err)
	}
	r.Set("k", "v")
	commit(t, r, gotato.AssistantMessage("hello"))
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
		commit(t, r, gotato.UserMessage(text))
	}
	summary := gotato.AssistantMessage("summary of one and two")
	r.Checkpoint(gotato.CompactionOnDemand, []gotato.Message{summary, gotato.UserMessage("three")}, &session.Compaction{ReplacedMessages: 2, Recap: "test"})
	commit(t, r, gotato.UserMessage("four"))
	entries := session.TakePending(r)

	last := -1
	for i, entry := range entries {
		if entry.Kind == gotato.KindCompactionEnded {
			last = i
		}
		if entry.IsCheckpoint() != (entry.Kind == gotato.KindSessionCreated || entry.Kind == gotato.KindCompactionEnded) {
			t.Fatalf("%s: IsCheckpoint = %v", entry.Kind, entry.IsCheckpoint())
		}
	}
	if last <= 0 {
		t.Fatalf("no compaction_ended in %+v", entries)
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
		if rebuilt.ID() != r.ID() {
			t.Fatalf("rebuilt ID = %q, want %q", rebuilt.ID(), r.ID())
		}
	}
}

func TestCompactionAppendsStartedThenOneCheckpoint(t *testing.T) {
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
	var kinds []gotato.EventKind
	for _, entry := range session.TakePending(r) {
		kinds = append(kinds, entry.Kind)
		if entry.Kind == gotato.KindMessageCommitted || entry.Kind == gotato.KindReplyEnded {
			t.Fatalf("compaction rewrote Messages one by one: %+v", entry)
		}
	}
	if want := []gotato.EventKind{gotato.KindCompactionStarted, gotato.KindSegmentClosed, gotato.KindCompactionEnded}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
}

func TestUnknownEntryKindIsRefused(t *testing.T) {
	r := session.New()
	session.StartLog(r)
	entries := session.TakePending(r)
	entries = append(entries, session.Entry{Seq: uint64(len(entries) + 1), Kind: "from_the_future", Layer: session.LayerFact, Data: json.RawMessage(`{"kind":"from_the_future"}`)})
	_, err := session.FromEntries(entries)
	var runtimeErr *gotato.RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.Code != gotato.ErrNotSupported {
		t.Fatalf("FromEntries = %v, want not_supported", err)
	}
}

func TestALogMustStartAtACheckpointInOrder(t *testing.T) {
	r := session.New()
	session.StartLog(r)
	commit(t, r, gotato.UserMessage("hi"))
	commit(t, r, gotato.UserMessage("again"))
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
		commit(t, r, gotato.UserMessage("hi"))
		if _, err := r.Write(context.Background(), []gotato.Event{runEvent("start", "run", time.Now())}); err != nil {
			t.Fatal(err)
		}
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
	if entries := session.TakePending(r); len(entries) != 1 || entries[0].Kind != gotato.KindMetadataChanged {
		t.Fatalf("Set with a change queued %+v", entries)
	}
}

// An Entry is a stored Event: its Seq is the Event's number, its Kind the
// Event's kind, and EventOf decodes it back into the typed Event.
func TestEntryIsTheStoredEvent(t *testing.T) {
	r := session.New()
	session.StartLog(r)
	commit(t, r, gotato.UserMessage("hi"))
	for _, entry := range session.TakePending(r) {
		event, ok, err := session.EventOf(entry, gotato.SessionID(r.ID()))
		if err != nil || !ok {
			t.Fatalf("EventOf(%s) = %v, %v", entry.Kind, ok, err)
		}
		if event.Kind() != entry.Kind || event.Head().Number != entry.Seq || event.Head().SessionID != gotato.SessionID(r.ID()) {
			t.Fatalf("EventOf(%+v) = %s numbered %d", entry, event.Kind(), event.Head().Number)
		}
		if session.LayerOf(entry.Kind) != entry.Layer {
			t.Fatalf("%s: layer %s, LayerOf says %s", entry.Kind, entry.Layer, session.LayerOf(entry.Kind))
		}
	}
}

func TestForkRecordsWhereItBranched(t *testing.T) {
	parent := session.New()
	session.StartLog(parent)
	commit(t, parent, gotato.UserMessage("hi"))
	head := session.TakePending(parent)
	child := session.Fork(parent)
	session.StartLog(child)
	entries := session.TakePending(child)
	event, ok, err := session.EventOf(entries[0], gotato.SessionID(child.ID()))
	created, isCreated := event.(gotato.SessionCreated)
	if err != nil || !ok || !isCreated {
		t.Fatalf("first entry = %v %v %v", event, ok, err)
	}
	if created.ForkedFrom == nil || created.ForkedFrom.SessionID != gotato.SessionID(parent.ID()) || created.ForkedFrom.Number != head[len(head)-1].Seq || len(created.Messages) != 1 {
		t.Fatalf("fork session_created = %+v", created)
	}
	rebuilt, err := session.FromEntries(entries)
	if err != nil || rebuilt.ParentID() != parent.ID() {
		t.Fatalf("rebuilt fork = %v, %v", rebuilt, err)
	}
}

// An Entry of a kind the Event design replaced is refused as an unknown kind,
// never misread.
func TestEntryOfAnOldKindIsRefused(t *testing.T) {
	old := session.Entry{Seq: 1, Kind: "checkpoint", Layer: session.LayerFact, At: time.Unix(1, 0).UTC(), Data: []byte(`{"id":"x","messages":[]}`)}
	if _, err := session.FromEntries([]session.Entry{old}); err == nil {
		t.Fatal("FromEntries accepted an Entry of the kind checkpoint")
	}
	if got := session.LayerOf("checkpoint"); got != session.LayerLog {
		t.Fatalf("LayerOf(checkpoint) = %s, want a log Entry", got)
	}
	if old.IsCheckpoint() {
		t.Fatal("an Entry of the kind checkpoint starts a load")
	}
}
