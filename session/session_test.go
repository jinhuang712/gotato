package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func runAgent(t *testing.T, s *session.Session, model gotato.Model, prompt string) gotato.RunResult {
	t.Helper()
	agent, err := gotato.NewAgent(
		gotato.WithModel(model),
		gotato.WithTranscript(s),
		gotato.WithExtension(session.Record(s)),
		gotato.WithTool(testkit.DemoEchoTool()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	result, err := agent.Prompt(context.Background(), gotato.UserMessage(prompt))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSessionRecordsRunsEventsAndUsage(t *testing.T) {
	s := session.New()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"v"}`)}),
		append(testkit.Script{{Kind: gotato.ModelUsage, Usage: gotato.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}}, testkit.Text("done")...),
	)
	result := runAgent(t, s, model, "use-tool")
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
	if usage := s.Usage(); usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", usage)
	}
	events := s.Events()
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	if events[0].Kind != gotato.EventAgentStart || events[len(events)-1].Kind != gotato.EventAgentEnd {
		t.Fatalf("events = %d first=%s", len(events), events[0].Kind)
	}
	var built int
	for _, event := range events {
		if event.Kind == gotato.EventContextBuilt {
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
	runAgent(t, s, testkit.EchoModel{}, "first")
	if err := store.Save(ctx, s); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Get(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 || loaded.ParentID() != "" {
		t.Fatalf("loaded = %d messages", loaded.Len())
	}
	if value, _ := loaded.Get("app"); value != "test" {
		t.Fatalf("metadata lost: %v", loaded.Metadata())
	}
	model := testkit.NewFakeModel(testkit.Text("second answer"))
	runAgent(t, loaded, model, "second")
	request, _ := model.LastRequest()
	if len(request.Messages) != 3 || gotato.TextOf(request.Messages[0]) != "first" {
		t.Fatalf("continued run saw %d messages", len(request.Messages))
	}
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].Messages != 4 || list[0].Runs != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := store.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, s.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	if _, err := store.Get(ctx, "../escape"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("path traversal err = %v", err)
	}
}

func TestMemoryStore(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	s := testkit.NewSession("a", "b")
	if err := store.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, s.ID())
	if err != nil || got.Len() != 2 {
		t.Fatalf("get = %v err=%v", got, err)
	}
	// Mutating the loaded copy does not change the stored document.
	_ = got.Append(gotato.UserMessage("c"))
	again, _ := store.Get(ctx, s.ID())
	if again.Len() != 2 {
		t.Fatalf("store aliased live session: %d", again.Len())
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
}

func TestForkIsIndependentState(t *testing.T) {
	parent := testkit.NewSession("q1", "a1")
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

func TestEventLimitBoundsRetention(t *testing.T) {
	s := session.New(session.WithEventLimit(3))
	for i := 0; i < 10; i++ {
		s.RecordEvent(gotato.Event{Kind: gotato.EventTurnStart, Sequence: uint64(i)})
	}
	events := s.Events()
	if len(events) != 3 || events[0].Sequence != 7 {
		t.Fatalf("events = %+v", events)
	}
}

func TestEventLimitZeroKeepsDefault(t *testing.T) {
	s := session.New(session.WithEventLimit(0))
	if got := s.Snapshot().EventLimit; got != session.DefaultEventLimit {
		t.Fatalf("event limit = %d, want default %d", got, session.DefaultEventLimit)
	}
	s.RecordEvent(gotato.Event{Kind: gotato.EventTurnStart})
	if len(s.Events()) != 1 {
		t.Fatalf("zero limit retained %d events, want 1", len(s.Events()))
	}
	disabled := session.New(session.WithEventLimit(-1))
	disabled.RecordEvent(gotato.Event{Kind: gotato.EventTurnStart})
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

func TestEventPayloadIsIsolated(t *testing.T) {
	s := session.New()
	payload := map[string]any{"text": "original"}
	s.RecordEvent(gotato.Event{Kind: gotato.EventTurnStart, Payload: payload})
	payload["text"] = "mutated by caller"

	events := s.Events()
	if got := events[0].Payload["text"]; got != "original" {
		t.Fatalf("caller mutation reached the Event: %v", got)
	}
	events[0].Payload["text"] = "mutated by reader"
	if got := s.Events()[0].Payload["text"]; got != "original" {
		t.Fatalf("reader mutation reached the Event: %v", got)
	}
}

func TestEventPayloadNestedIsIsolated(t *testing.T) {
	s := session.New()
	payload := map[string]any{
		"summary": map[string]any{
			"tool_results": []map[string]any{{"tool_id": "echo", "status": "ok"}},
		},
		"tags": []any{"a", "b"},
	}
	s.RecordEvent(gotato.Event{Kind: gotato.EventTurnEnd, Payload: payload})

	// Mutating the caller's nested values must not reach the stored Event.
	payload["summary"].(map[string]any)["tool_results"].([]map[string]any)[0]["status"] = "caller"
	payload["tags"].([]any)[0] = "caller"

	stored := s.Events()
	summary := stored[0].Payload["summary"].(map[string]any)
	if status := summary["tool_results"].([]map[string]any)[0]["status"]; status != "ok" {
		t.Fatalf("caller nested mutation reached the Event: %v", status)
	}
	if tag := stored[0].Payload["tags"].([]any)[0]; tag != "a" {
		t.Fatalf("caller nested slice mutation reached the Event: %v", tag)
	}

	// Mutating a returned nested value must not reach the stored Event.
	summary["tool_results"].([]map[string]any)[0]["status"] = "reader"
	summary["tool_results"] = nil
	stored[0].Payload["tags"].([]any)[0] = "reader"

	again := s.Events()
	againSummary := again[0].Payload["summary"].(map[string]any)
	if status := againSummary["tool_results"].([]map[string]any)[0]["status"]; status != "ok" {
		t.Fatalf("reader nested mutation reached the Event: %v", status)
	}
	if tag := again[0].Payload["tags"].([]any)[0]; tag != "a" {
		t.Fatalf("reader nested slice mutation reached the Event: %v", tag)
	}
}

func TestRecordEventRingBufferKeepsNewest(t *testing.T) {
	s := session.New(session.WithEventLimit(2))
	for i := 0; i < 5; i++ {
		s.RecordEvent(gotato.Event{
			Kind:     gotato.EventTurnStart,
			Sequence: uint64(i),
			Payload:  map[string]any{"seq": i},
		})
	}
	events := s.Events()
	if len(events) != 2 || events[0].Sequence != 3 || events[1].Sequence != 4 {
		t.Fatalf("events = %+v", events)
	}
	// The payload must stay bound to its own Event across ring overwrites.
	if events[0].Payload["seq"] != 3 || events[1].Payload["seq"] != 4 {
		t.Fatalf("payloads = %+v", events)
	}
}

func TestRecordEventNormalizesOverfullLoadedDocument(t *testing.T) {
	doc := session.Document{
		ID:            "x",
		SchemaVersion: session.SchemaVersion,
		EventLimit:    2,
		Events: []gotato.Event{
			{Sequence: 1}, {Sequence: 2}, {Sequence: 3}, {Sequence: 4},
		},
	}
	s, err := session.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordEvent(gotato.Event{Sequence: 5})
	events := s.Events()
	if len(events) != 2 || events[0].Sequence != 4 || events[1].Sequence != 5 {
		t.Fatalf("events = %+v", events)
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
