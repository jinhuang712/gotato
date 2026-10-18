package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func runSession(t *testing.T, s *session.Record, model gotato.Model, prompt string) gotato.RunResult {
	t.Helper()
	agent, err := gotato.NewSession(
		gotato.WithModel(model),
		gotato.WithTranscript(s),
		gotato.WithTool(testkit.DemoEchoTool()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), gotato.UserMessage(prompt))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A live Session over a Record takes the Record's ID, and every Event it
// records carries it. Without a Record it generates its own.
func TestLiveSessionTakesItsRecordID(t *testing.T) {
	r := session.New(session.WithID("order-42"))
	runSession(t, r, testkit.EchoModel{}, "hi")
	events := r.Events()
	if len(events) == 0 {
		t.Fatal("no Events recorded")
	}
	for _, event := range events {
		if event.Head().SessionID != "order-42" {
			t.Fatalf("%s carries session_id %q, want order-42", event.Kind(), event.Head().SessionID)
		}
	}
	live, err := gotato.NewSession(gotato.WithModel(testkit.EchoModel{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close(context.Background()) }()
	if live.ID() == "" {
		t.Fatal("a Session without a Record has no ID")
	}
}

func TestSessionRecordsRunsEventsAndUsage(t *testing.T) {
	s := session.New()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"v"}`)}),
		append(testkit.Script{{Kind: gotato.ModelUsage, Usage: gotato.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CacheReadTokens: 6, CacheWriteTokens: 2}}}, testkit.Text("done")...),
	)
	result := runSession(t, s, model, "use-tool")
	if result.Status != gotato.RunCompleted {
		t.Fatalf("status = %s", result.Status)
	}
	if s.Len() != 4 {
		t.Fatalf("messages = %d, want user, assistant(tool call), tool_result, assistant", s.Len())
	}
	runs := s.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunCompleted || runs[0].Turns != 2 || runs[0].ToolCalls != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	if usage := s.Usage(); usage.TotalTokens != 15 || usage.CacheReadTokens != 6 || usage.CacheWriteTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	if final := s.Messages()[3]; final.Role != gotato.RoleAssistant || final.Usage.TotalTokens != 15 || final.Usage.CacheReadTokens != 6 {
		t.Fatalf("final assistant usage = %+v", final.Usage)
	}
	events := s.Events()
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	kinds := kindsOf(events)
	if kinds[0] != gotato.KindRunStarted || kinds[len(kinds)-1] != gotato.KindRunEnded {
		t.Fatalf("events = %v", kinds)
	}
	var built int
	for _, event := range events {
		if event.Kind() == gotato.KindContextBuilt {
			built++
		}
	}
	if built != 2 {
		t.Fatalf("context_built events = %d, want one per Turn", built)
	}
}

func TestFileStoreRoundTripContinuesHistory(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := session.New(session.WithMetadata(map[string]string{"app": "test"}))
	writer, err := session.Create(ctx, store, s, "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s, testkit.EchoModel{}, "first")
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, err := session.OpenWriter(ctx, store, s.ID(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	loaded := reopened.Record()
	if loaded.Len() != 2 || loaded.ParentID() != "" {
		t.Fatalf("loaded = %d messages", loaded.Len())
	}
	if value, _ := loaded.Get("app"); value != "test" {
		t.Fatalf("metadata lost: %v", loaded.Metadata())
	}
	model := testkit.NewFakeModel(testkit.Text("second answer"))
	runSession(t, loaded, model, "second")
	request, _ := model.LastRequest()
	if len(request.Messages) != 3 || gotato.TextOf(request.Messages[0]) != "first" {
		t.Fatalf("continued run saw %d messages", len(request.Messages))
	}
	if err := reopened.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].Messages != 4 || list[0].Runs != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := store.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Open(ctx, store, s.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	if _, err := session.Open(ctx, store, "../escape"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("path traversal err = %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	s := testkit.NewRecord("a", "b")
	if _, err := session.Create(ctx, store, s, "instance-a"); err != nil {
		t.Fatal(err)
	}
	got, err := session.Open(ctx, store, s.ID())
	if err != nil || got.Len() != 2 {
		t.Fatalf("get = %v err=%v", got, err)
	}
	// Mutating a copy opened for reading does not change the stored Record.
	_ = got.Append(gotato.UserMessage("c"))
	again, _ := session.Open(ctx, store, s.ID())
	if again.Len() != 2 {
		t.Fatalf("store aliased live session: %d", again.Len())
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
}

func TestForkIsIndependentState(t *testing.T) {
	parent := testkit.NewRecord("q1", "a1")
	parent.Set("owner", "parent")
	child := session.Fork(parent)
	if child.ID() == parent.ID() || child.ParentID() != parent.ID() {
		t.Fatalf("fork identity: child=%s parent=%s childParent=%s", child.ID(), parent.ID(), child.ParentID())
	}
	if child.Len() != 2 {
		t.Fatalf("child messages = %d", child.Len())
	}
	if value, _ := child.Get("owner"); value != "parent" {
		t.Fatalf("metadata not copied: %v", child.Metadata())
	}
	_ = child.Append(gotato.UserMessage("q2"))
	child.Set("owner", "child")
	if parent.Len() != 2 {
		t.Fatalf("parent mutated by child append: %d", parent.Len())
	}
	if value, _ := parent.Get("owner"); value != "parent" {
		t.Fatalf("parent metadata mutated: %v", parent.Metadata())
	}
	if len(child.Runs()) != 0 || len(child.Events()) != 0 {
		t.Fatalf("fork should start with empty run/event records")
	}
}

func turnStarted() gotato.Event { return gotato.TurnStarted{Header: header()} }

func TestEventLimitBoundsRetention(t *testing.T) {
	s := session.New(session.WithEventLimit(3))
	for range 10 {
		if _, err := s.Write(context.Background(), []gotato.Event{turnStarted()}); err != nil {
			t.Fatal(err)
		}
	}
	events := s.Events()
	if len(events) != 3 || events[0].Head().Number != 8 || events[2].Head().Number != 10 {
		t.Fatalf("events = %+v, want numbers 8 to 10", events)
	}
}

func TestEventLimitZeroKeepsDefault(t *testing.T) {
	s := session.New(session.WithEventLimit(0))
	if got := s.Snapshot().EventLimit; got != session.DefaultEventLimit {
		t.Fatalf("event limit = %d, want default %d", got, session.DefaultEventLimit)
	}
	if _, err := s.Write(context.Background(), []gotato.Event{turnStarted()}); err != nil {
		t.Fatal(err)
	}
	if len(s.Events()) != 1 {
		t.Fatalf("zero limit retained %d events, want 1", len(s.Events()))
	}
	disabled := session.New(session.WithEventLimit(-1))
	if _, err := disabled.Write(context.Background(), []gotato.Event{turnStarted()}); err != nil {
		t.Fatal(err)
	}
	if len(disabled.Events()) != 0 {
		t.Fatalf("negative limit retained %d events", len(disabled.Events()))
	}
}

func TestLoadRejectsFutureSchema(t *testing.T) {
	_, err := session.Load(session.Document{ID: "x", SchemaVersion: session.SchemaVersion + 1})
	if !gotato.IsCode(err, gotato.ErrNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestFileStoreListSurfacesUnreadableSession(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("List hid an unreadable session file")
	}
}

// The Events a Record returns are a copy of its window: a caller that edits
// the slice does not change what the Record keeps.
func TestEventsReturnsACopyOfTheWindow(t *testing.T) {
	s := session.New()
	if _, err := s.Write(context.Background(), []gotato.Event{turnStarted(), gotato.TurnEnded{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	events := s.Events()
	events[0] = gotato.RunEnded{Header: header()}
	if kinds := kindsOf(s.Events()); len(kinds) != 2 || kinds[0] != gotato.KindTurnStarted || kinds[1] != gotato.KindTurnEnded {
		t.Fatalf("a caller's edit reached the Record's window: %v", kinds)
	}
}

// Events keep their own typed fields: a RunEnded read back holds what was
// written, whatever the caller does to its own copy of the value afterwards.
func TestEventsKeepTheirFields(t *testing.T) {
	s := session.New()
	ended := gotato.RunEnded{Header: gotato.NewHeader("", "run-1", 0), Status: gotato.RunCompleted, Turns: 2, Usage: gotato.Usage{TotalTokens: 9}}
	if _, err := s.Write(context.Background(), []gotato.Event{ended}); err != nil {
		t.Fatal(err)
	}
	ended.Turns = 99
	got, ok := s.Events()[0].(gotato.RunEnded)
	if !ok || got.Turns != 2 || got.Usage.TotalTokens != 9 || got.RunID != "run-1" {
		t.Fatalf("event = %+v", s.Events()[0])
	}
}

func TestRingBufferKeepsNewestInOrder(t *testing.T) {
	s := session.New(session.WithEventLimit(2))
	for i := range 5 {
		hd := header()
		hd.Turn = gotato.TurnNumber(i)
		if _, err := s.Write(context.Background(), []gotato.Event{gotato.TurnStarted{Header: hd}}); err != nil {
			t.Fatal(err)
		}
	}
	events := s.Events()
	if len(events) != 2 || events[0].Head().Number != 4 || events[1].Head().Number != 5 || events[0].Head().Turn != 3 || events[1].Head().Turn != 4 {
		t.Fatalf("events = %+v", events)
	}
}

func TestLoadNormalizesAnOverfullDocument(t *testing.T) {
	var events []gotato.Event
	for i := 1; i <= 4; i++ {
		events = append(events, gotato.TurnStarted{Header: gotato.Header{Number: uint64(i), Turn: gotato.TurnNumber(i)}})
	}
	doc := session.Document{ID: "x", SchemaVersion: session.SchemaVersion, EventLimit: 2, Events: events}
	s, err := session.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(context.Background(), []gotato.Event{gotato.TurnEnded{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	got := s.Events()
	if len(got) != 2 || got[0].Head().Turn != 4 || got[1].Kind() != gotato.KindTurnEnded {
		t.Fatalf("events = %+v", got)
	}
}

// A Document holds typed Events and reads them back by their kind; an Event of
// a kind this version does not know, such as one a later version adds, is
// dropped, not an error.
func TestDocumentRoundTripsTypedEvents(t *testing.T) {
	s := session.New()
	if _, err := s.Write(context.Background(), []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}, gotato.TurnStarted{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var doc session.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(doc.Events); len(kinds) != 2 || kinds[0] != gotato.KindRunStarted || kinds[1] != gotato.KindTurnStarted {
		t.Fatalf("document events = %v", kinds)
	}
	withFuture := []byte(`{"id":"x","schema_version":1,"events":[{"kind":"from_the_future"},{"kind":"turn_started","time":"2026-10-17T12:00:00Z"}]}`)
	var future session.Document
	if err := json.Unmarshal(withFuture, &future); err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(future.Events); len(kinds) != 1 || kinds[0] != gotato.KindTurnStarted {
		t.Fatalf("future document events = %v", kinds)
	}
}

func TestSummaryMetadataIsIsolated(t *testing.T) {
	s := session.New(session.WithMetadata(map[string]string{"app": "test"}))
	summary := session.SummaryOf(s)
	summary.Metadata["app"] = "mutated"
	if value, _ := s.Get("app"); value != "test" {
		t.Fatalf("summary metadata aliased the Session: %v", s.Metadata())
	}
}
