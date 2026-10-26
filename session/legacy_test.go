package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/session"
)

// The Entries below are in the format a Record had before the Event design:
// "checkpoint", "message", "metadata", "claim", "tools_started" facts, and
// "log_snapshot", "run_start", "run_end", "events", "model_call", "reply_part",
// "release" and "busy" log Entries. A store hands them back as they are; the
// Record reads them, and new Events are appended after them.

var legacyAt = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// legacyEntry numbers an Entry of the earlier format.
func legacyEntry(t *testing.T, seq uint64, kind session.EntryKind, payload any) session.Entry {
	t.Helper()
	return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerOf(kind), At: legacyAt.Add(time.Duration(seq) * time.Second), Data: mustJSON(t, payload)}
}

func legacyMessage(id gotato.MessageID, role gotato.Role, text string) gotato.Message {
	return gotato.Message{ID: id, Role: role, Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: text}}}
}

func legacyCheckpoint(messages []gotato.Message, metadata map[string]string, claim session.Claim) map[string]any {
	return map[string]any{
		"id": "legacy-1", "created_at": legacyAt, "messages": messages, "metadata": metadata,
		"event_limit": 500, "claim": claim,
	}
}

// legacyLog is a whole legacy Record of 17 Entries:
//
//	1 checkpoint (q0, a0, agent=old, claim old-inst epoch 3)   2 claim inst-x epoch 4
//	3 log_snapshot (one finished Run, 5 tokens)                4 run_start run-1
//	5 message q1   6 model_call   7 message a1 (calls c1)      8 tools_started c1
//	9 message result c1   10 message a2   11 metadata agent=new
//	12 events   13 run_end run-1 (12 tokens)   14 release
//	15 checkpoint (summary, agent=new, claim inst-x epoch 4)   16 message q2   17 busy
func legacyLog(t *testing.T) []session.Entry {
	t.Helper()
	call := legacyMessage("a1", gotato.RoleAssistant, "")
	call.StopReason = gotato.StopToolCalls
	call.ToolCalls = []gotato.ToolCall{{ID: "c1", ToolID: "demo.echo", Arguments: []byte(`{}`)}}
	result := gotato.Message{ID: "r1", Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultOK}, Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "ok"}}}
	final := legacyMessage("a2", gotato.RoleAssistant, "done")
	final.StopReason = gotato.StopEndTurn
	first := legacyMessage("a0", gotato.RoleAssistant, "a0")
	first.StopReason = gotato.StopEndTurn
	summary := legacyMessage("summary-1", gotato.RoleAssistant, "summary of q0 to a2")
	inst := session.Claim{Instance: "inst-x", Epoch: 4}
	return []session.Entry{
		legacyEntry(t, 1, "checkpoint", legacyCheckpoint([]gotato.Message{legacyMessage("q0", gotato.RoleUser, "q0"), first}, map[string]string{"agent": "old"}, session.Claim{Instance: "old-inst", Epoch: 3})),
		legacyEntry(t, 2, "claim", inst),
		legacyEntry(t, 3, "log_snapshot", map[string]any{"runs": []session.Run{{RunID: "run-0", Status: gotato.RunCompleted, Turns: 1}}, "usage": gotato.Usage{TotalTokens: 5}}),
		legacyEntry(t, 4, "run_start", session.Run{RunID: "run-1", Status: gotato.RunRunning, StartedAt: legacyAt.Add(4 * time.Second)}),
		legacyEntry(t, 5, "message", legacyMessage("q1", gotato.RoleUser, "q1")),
		legacyEntry(t, 6, "model_call", map[string]any{"run_id": "run-1", "turn": 1}),
		legacyEntry(t, 7, "message", call),
		legacyEntry(t, 8, "tools_started", map[string]any{"message_id": "a1", "calls": []string{"c1"}}),
		legacyEntry(t, 9, "message", result),
		legacyEntry(t, 10, "message", final),
		legacyEntry(t, 11, "metadata", map[string]string{"key": "agent", "value": "new"}),
		legacyEntry(t, 12, "events", []any{}),
		legacyEntry(t, 13, "run_end", session.Run{RunID: "run-1", Status: gotato.RunCompleted, EndedAt: legacyAt.Add(13 * time.Second), Turns: 2, ToolCalls: 1, Usage: gotato.Usage{TotalTokens: 12}}),
		legacyEntry(t, 14, "release", inst),
		legacyEntry(t, 15, "checkpoint", legacyCheckpoint([]gotato.Message{summary}, map[string]string{"agent": "new"}, inst)),
		legacyEntry(t, 16, "message", legacyMessage("q2", gotato.RoleUser, "q2")),
		legacyEntry(t, 17, "busy", inst),
	}
}

// legacyStore keeps the first n Entries of legacyLog in a MemoryStore, in two
// writes as a store would have received them, with the Summary the Record
// had.
func legacyStore(t *testing.T, n int) *session.MemoryStore {
	t.Helper()
	entries := legacyLog(t)[:n]
	record, err := session.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	summary := session.SummaryOf(record)
	summary.Seq = uint64(n)
	store := session.NewMemoryStore()
	ctx := context.Background()
	if err := store.Create(ctx, "legacy-1", entries[:2], summary); err != nil {
		t.Fatal(err)
	}
	if n > 2 {
		if err := store.Append(ctx, "legacy-1", 2, entries[2:], summary); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestLegacyLayersAndCheckpoints(t *testing.T) {
	facts := map[session.EntryKind]bool{"checkpoint": true, "message": true, "metadata": true, "claim": true, "tools_started": true}
	for _, entry := range legacyLog(t) {
		want := session.LayerLog
		if facts[entry.Kind] {
			want = session.LayerFact
		}
		if entry.Layer != want {
			t.Fatalf("legacy %s is layer %s, want %s", entry.Kind, entry.Layer, want)
		}
		if entry.IsCheckpoint() != (entry.Kind == "checkpoint") {
			t.Fatalf("legacy %s: IsCheckpoint = %v", entry.Kind, entry.IsCheckpoint())
		}
	}
}

func TestLegacyLoadStartsAtTheLastCheckpoint(t *testing.T) {
	ctx := context.Background()
	// A checkpoint that comes after other Entries stands in for all of them.
	loaded, err := legacyStore(t, 17).Load(ctx, "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Head != 17 || loaded.Facts[0].Seq != 15 || loaded.Facts[0].Kind != "checkpoint" {
		t.Fatalf("Load = head %d, first fact %d %s; want head 17 starting at checkpoint 15", loaded.Head, loaded.Facts[0].Seq, loaded.Facts[0].Kind)
	}
	for _, fact := range loaded.Facts {
		if fact.Layer != session.LayerFact {
			t.Fatalf("Load returned a %s Entry: %s", fact.Layer, fact.Kind)
		}
	}
	record, err := session.Open(ctx, legacyStore(t, 17), "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	messages := record.Messages()
	if len(messages) != 2 || gotato.TextOf(messages[0]) != "summary of q0 to a2" || gotato.TextOf(messages[1]) != "q2" {
		t.Fatalf("Messages = %+v, want the second checkpoint's summary and q2", messages)
	}
	if record.ID() != "legacy-1" || record.Claim() != (session.Claim{Instance: "inst-x", Epoch: 4}) {
		t.Fatalf("id %q claim %+v", record.ID(), record.Claim())
	}
	// With the checkpoint at Entry 1 only, a load reads everything from there.
	first, err := legacyStore(t, 14).Load(ctx, "legacy-1")
	if err != nil || first.Facts[0].Seq != 1 || first.Head != 14 {
		t.Fatalf("Load = %+v, %v", first, err)
	}
}

func TestLegacyViewRebuildsRunsAndUsage(t *testing.T) {
	ctx := context.Background()
	view, err := session.View(ctx, legacyStore(t, 17), "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	runs := view.Runs()
	if len(runs) != 2 || runs[0].RunID != "run-0" || runs[1].RunID != "run-1" || runs[1].Status != gotato.RunCompleted || runs[1].Turns != 2 || runs[1].ToolCalls != 1 {
		t.Fatalf("Runs = %+v", runs)
	}
	if usage := view.Usage(); usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v, want the snapshot's 5 plus the Run's 12", usage)
	}
	if view.Head() != 17 {
		t.Fatalf("Head = %d", view.Head())
	}
	// FromEntries over the whole log gives the same Record.
	whole, err := session.FromEntries(legacyLog(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Runs()) != 2 || whole.Usage().TotalTokens != 17 || whole.Len() != 2 {
		t.Fatalf("FromEntries: %d Runs, usage %+v, %d Messages", len(whole.Runs()), whole.Usage(), whole.Len())
	}
	// Before the second checkpoint the whole conversation is there.
	early, err := session.FromEntries(legacyLog(t)[:14])
	if err != nil {
		t.Fatal(err)
	}
	if early.Len() != 6 || early.Metadata()["agent"] != "new" || early.Claim() != (session.Claim{Instance: "inst-x", Epoch: 4}) {
		t.Fatalf("early: %d Messages, metadata %v, claim %+v", early.Len(), early.Metadata(), early.Claim())
	}
}

func TestLegacyStoredEventsMapOntoEvents(t *testing.T) {
	events, err := session.StoredEvents(context.Background(), legacyStore(t, 17), "legacy-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		number uint64
		kind   gotato.EventKind
	}{
		{1, gotato.KindSessionCreated}, {2, gotato.KindSessionClaimed},
		// 3 is a log_snapshot: it has no Event.
		{4, gotato.KindRunStarted}, {5, gotato.KindMessageCommitted}, {6, gotato.KindReplyStarted},
		{7, gotato.KindReplyEnded}, {8, gotato.KindToolStarted}, {9, gotato.KindToolResultCommitted},
		{10, gotato.KindReplyEnded}, {11, gotato.KindMetadataChanged},
		// 12 is an "events" copy of Events: it has none.
		{13, gotato.KindRunEnded}, {14, gotato.KindSessionReleased}, {15, gotato.KindCompactionEnded},
		{16, gotato.KindMessageCommitted}, {17, gotato.KindSessionClaimed},
	}
	if len(events) != len(want) {
		t.Fatalf("kinds = %v, want %d Events", kindsOf(events), len(want))
	}
	for i, event := range events {
		if event.Kind() != want[i].kind || event.Head().Number != want[i].number || event.Head().SessionID != "legacy-1" {
			t.Fatalf("event %d = %s numbered %d for %q, want %s numbered %d", i, event.Kind(), event.Head().Number, event.Head().SessionID, want[i].kind, want[i].number)
		}
	}
	if m := events[3].(gotato.MessageCommitted); m.Source != gotato.SourcePrompt || gotato.TextOf(m.Message) != "q1" {
		t.Fatalf("message user = %+v", m)
	}
	if r := events[5].(gotato.ReplyEnded); r.ToolCalls != 1 || r.Message.ID != "a1" {
		t.Fatalf("message assistant = %+v", r)
	}
	if tool := events[6].(gotato.ToolStarted); tool.CallID != "c1" || tool.MessageID != "a1" {
		t.Fatalf("tools_started = %+v", tool)
	}
	if c := events[1].(gotato.SessionClaimed); c.Instance != "inst-x" || c.Epoch != 4 {
		t.Fatalf("claim = %+v", c)
	}
	if run := events[2].(gotato.RunStarted); run.RunID != "run-1" {
		t.Fatalf("run_start = %+v", run)
	}
	if end := events[10].(gotato.RunEnded); end.RunID != "run-1" || end.Status != gotato.RunCompleted || end.Usage.TotalTokens != 12 {
		t.Fatalf("run_end = %+v", end)
	}
	if created := events[0].(gotato.SessionCreated); len(created.Messages) != 2 || created.Metadata["agent"] != "old" {
		t.Fatalf("checkpoint at 1 = %+v", created)
	}
	if ended := events[12].(gotato.CompactionEnded); len(ended.Messages) != 1 {
		t.Fatalf("checkpoint at 15 = %+v", ended)
	}
	// Reading after a number reads only what follows it.
	after, err := session.StoredEvents(context.Background(), legacyStore(t, 17), "legacy-1", 13)
	if err != nil || len(after) != 4 || after[0].Head().Number != 14 {
		t.Fatalf("StoredEvents after 13 = %v, %v", kindsOf(after), err)
	}
}

func TestLegacyWriterContinuesTheRecord(t *testing.T) {
	ctx := context.Background()
	store := legacyStore(t, 14)
	writer, err := session.OpenWriter(ctx, store, "legacy-1", "inst-y")
	if err != nil {
		t.Fatal(err)
	}
	record := writer.Record()
	if record.Len() != 6 || record.Metadata()["agent"] != "new" {
		t.Fatalf("opened %d Messages, metadata %v", record.Len(), record.Metadata())
	}
	// The Claim continues from the legacy Entries: inst-x held epoch 4.
	if claim := record.Claim(); claim != (session.Claim{Instance: "inst-y", Epoch: 5}) {
		t.Fatalf("claim = %+v, want inst-y at epoch 5", claim)
	}
	commit(t, writer, gotato.UserMessage("after the upgrade"))
	record.Set("agent", "latest")
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Log(ctx, "legacy-1", 14)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []gotato.EventKind{gotato.KindSessionClaimed, gotato.KindMessageCommitted, gotato.KindMetadataChanged}
	if len(entries) != len(wantKinds) {
		t.Fatalf("written after the legacy Entries = %+v", entries)
	}
	for i, entry := range entries {
		if entry.Kind != wantKinds[i] || entry.Seq != uint64(15+i) {
			t.Fatalf("entry %d = %s at %d, want %s at %d", i, entry.Kind, entry.Seq, wantKinds[i], 15+i)
		}
	}
	claimed, _, err := session.EventOf(entries[0], "legacy-1")
	if err != nil || claimed.(gotato.SessionClaimed).Epoch != 5 || claimed.(gotato.SessionClaimed).From != "inst-x" || claimed.(gotato.SessionClaimed).Why != gotato.ClaimIdle {
		t.Fatalf("claim entry = %+v, %v", claimed, err)
	}
	// A load reads the legacy checkpoint and the new Events together.
	reopened, err := session.Open(ctx, store, "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Len() != 7 || reopened.Metadata()["agent"] != "latest" || reopened.Claim().Epoch != 5 {
		t.Fatalf("reopened: %d Messages, metadata %v, claim %+v", reopened.Len(), reopened.Metadata(), reopened.Claim())
	}
	view, err := session.View(ctx, store, "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Head() != 17 || len(view.Runs()) != 2 || view.Usage().TotalTokens != 17 {
		t.Fatalf("view: head %d, %d Runs, usage %+v", view.Head(), len(view.Runs()), view.Usage())
	}
	events, err := session.StoredEvents(ctx, store, "legacy-1", 0)
	if err != nil || events[len(events)-1].Head().Number != 17 {
		t.Fatalf("StoredEvents = %v, %v", kindsOf(events), err)
	}
}

func TestLegacyWriterOfTheSameInstanceKeepsItsEpoch(t *testing.T) {
	ctx := context.Background()
	store := legacyStore(t, 14)
	writer, err := session.OpenWriter(ctx, store, "legacy-1", "inst-x")
	if err != nil {
		t.Fatal(err)
	}
	if claim := writer.Record().Claim(); claim != (session.Claim{Instance: "inst-x", Epoch: 4}) {
		t.Fatalf("claim = %+v, want inst-x still at epoch 4", claim)
	}
	if entries, err := store.Log(ctx, "legacy-1", 14); err != nil || len(entries) != 1 || entries[0].Kind != gotato.KindSessionClaimed || entries[0].Seq != 15 {
		t.Fatalf("written = %+v, %v", entries, err)
	}
}

func TestLegacyTakeoverEndsAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	store := legacyStore(t, 4) // the Run started, and its writer never ended it
	loaded, err := store.Load(ctx, "legacy-1")
	if err != nil || loaded.Summary.Running != "run-1" {
		t.Fatalf("summary = %+v, %v; want run-1 running", loaded.Summary, err)
	}
	if _, err := session.OpenWriter(ctx, store, "legacy-1", "inst-y"); err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, "legacy-1")
	if err != nil {
		t.Fatal(err)
	}
	runs := view.Runs()
	if len(runs) != 2 || runs[1].Status != gotato.RunFailed || runs[1].Error != "interrupted" {
		t.Fatalf("Runs = %+v, want run-1 ended as interrupted", runs)
	}
	events, err := session.StoredEvents(ctx, store, "legacy-1", 4)
	if err != nil || len(events) != 2 {
		t.Fatalf("StoredEvents after 4 = %v, %v", kindsOf(events), err)
	}
	claimed := events[0].(gotato.SessionClaimed)
	if claimed.Why != gotato.ClaimWriterGone || claimed.Epoch != 5 {
		t.Fatalf("claim = %+v, want writer_gone at epoch 5", claimed)
	}
	if ended := events[1].(gotato.RunEnded); ended.RunID != "run-1" || ended.Cause != "interrupted" {
		t.Fatalf("run_ended = %+v", ended)
	}
}

func TestLegacyReplyPartsAndToolsStartedRebuild(t *testing.T) {
	// A reply that was streaming when its writer stopped comes back as a
	// cut-off reply, and a Tool call that started is answered as stopped.
	ctx := context.Background()
	call := legacyMessage("a1", gotato.RoleAssistant, "")
	call.StopReason = gotato.StopToolCalls
	call.ToolCalls = []gotato.ToolCall{{ID: "c1", ToolID: "demo.echo", Arguments: []byte(`{}`)}}
	entries := []session.Entry{
		legacyEntry(t, 1, "checkpoint", legacyCheckpoint(nil, nil, session.Claim{Instance: "inst-x", Epoch: 1})),
		legacyEntry(t, 2, "message", legacyMessage("q1", gotato.RoleUser, "q1")),
		legacyEntry(t, 3, "reply_part", map[string]any{"run_id": "run-1", "turn": 1, "message_id": "a1", "text": "half an "}),
		legacyEntry(t, 4, "reply_part", map[string]any{"run_id": "run-1", "turn": 1, "message_id": "a1", "text": "answer"}),
		legacyEntry(t, 5, "message", legacyMessage("q2", gotato.RoleUser, "q2")),
		legacyEntry(t, 6, "message", call),
		legacyEntry(t, 7, "tools_started", map[string]any{"message_id": "a1", "calls": []string{"c1"}}),
	}
	record, err := session.FromEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	messages := record.Messages()
	if len(messages) != 5 || messages[1].StopReason != gotato.StopCutOff || gotato.TextOf(messages[1]) != "half an answer" {
		t.Fatalf("Messages = %+v", messages)
	}
	if stopped := messages[4].ToolResult; stopped == nil || stopped.CallID != "c1" || !stopped.Executed || stopped.SafeError != session.StoppedToolResult {
		t.Fatalf("c1 answered with %+v", messages[4])
	}
	store := session.NewMemoryStore()
	if err := store.Create(ctx, "legacy-1", entries[:1], session.SummaryOf(record)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, "legacy-1", 1, entries[1:], session.SummaryOf(record)); err != nil {
		t.Fatal(err)
	}
	events, err := session.StoredEvents(ctx, store, "legacy-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsOf(events); len(got) != 5 || got[0] != gotato.KindReplyPartKept || got[3] != gotato.KindReplyEnded {
		t.Fatalf("kinds = %v", got)
	}
	if part := events[0].(gotato.ReplyPartKept); part.MessageID != "a1" || part.Text != "half an " || part.RunID != "run-1" || part.Turn != 1 {
		t.Fatalf("reply_part = %+v", part)
	}
}

// A Record of an earlier layout of the file store, a document <id>.json or a
// directory <id>/ that holds batch files, is refused with ErrOldLayout by
// every call that would read or write it, and is never misread, nor made to
// look like a Record that does not exist.
func TestFileStoreRefusesARecordOfAnEarlierLayout(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.json"), mustJSON(t, session.Document{SchemaVersion: 1, ID: "doc"}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dirid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirid", "00000000000000000001.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry := session.Entry{Seq: 1, Kind: gotato.KindSessionCreated, Layer: session.LayerFact, Data: []byte(`{}`)}
	summary := session.Summary{RecordEpoch: "e", Seq: 1}
	for _, id := range []string{"doc", "dirid"} {
		if _, err := store.Load(ctx, id); !errors.Is(err, session.ErrOldLayout) {
			t.Errorf("Load(%s) = %v, want ErrOldLayout", id, err)
		}
		if _, err := store.Log(ctx, id, 0); !errors.Is(err, session.ErrOldLayout) {
			t.Errorf("Log(%s) = %v, want ErrOldLayout", id, err)
		}
		if err := store.Create(ctx, id, []session.Entry{entry}, summary); !errors.Is(err, session.ErrOldLayout) {
			t.Errorf("Create(%s) = %v, want ErrOldLayout", id, err)
		}
		if _, err := store.List(ctx); err == nil || !strings.Contains(err.Error(), id) {
			t.Errorf("List = %v, want an error that names %s", err, id)
		}
	}
	if err := store.Delete(ctx, "doc"); err != nil {
		t.Fatalf("Delete of a document of the earlier layout = %v, want nil", err)
	}
	if err := store.Delete(ctx, "dirid"); err != nil {
		t.Fatalf("Delete of a directory of the earlier layout = %v, want nil", err)
	}
}
