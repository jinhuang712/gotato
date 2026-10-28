package session_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestLoadRejectsFutureSchema(t *testing.T) {
	_, err := session.FromDocument(session.Document{ID: "x", SchemaVersion: session.SchemaVersion + 1})
	if !gotato.IsCode(err, gotato.CodeNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadNormalizesAnOverfullDocument(t *testing.T) {
	var events []gotato.Event
	for i := 1; i <= session.DefaultEventLimit+2; i++ {
		events = append(events, gotato.TurnStarted{Header: gotato.Header{Number: uint64(i), Turn: gotato.TurnNumber(i)}})
	}
	doc := session.Document{ID: "x", SchemaVersion: session.SchemaVersion, Events: events}
	s, err := session.FromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(context.Background(), []gotato.Event{gotato.TurnEnded{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	got := s.Events()
	if len(got) != session.DefaultEventLimit || got[0].Head().Turn != 4 || got[len(got)-1].Kind() != gotato.EventKindTurnEnded {
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
	if kinds := kindsOf(doc.Events); len(kinds) != 2 || kinds[0] != gotato.EventKindRunStarted || kinds[1] != gotato.EventKindTurnStarted {
		t.Fatalf("document events = %v", kinds)
	}
	withFuture := []byte(`{"id":"x","schema_version":1,"events":[{"kind":"from_the_future"},{"kind":"turn_started","time":"2026-10-17T12:00:00Z"}]}`)
	var future session.Document
	if err := json.Unmarshal(withFuture, &future); err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(future.Events); len(kinds) != 1 || kinds[0] != gotato.EventKindTurnStarted {
		t.Fatalf("future document events = %v", kinds)
	}
}

func runMessage(id, role, text string, runID gotato.RunID) gotato.Message {
	message := idMessage(id, role, text)
	message.RunID = runID
	return message
}

func runIDs(messages []gotato.Message) []gotato.RunID {
	out := make([]gotato.RunID, len(messages))
	for i, message := range messages {
		out[i] = message.RunID
	}
	return out
}

func sameRunIDs(t *testing.T, label string, got []gotato.Message, want ...gotato.RunID) {
	t.Helper()
	have := runIDs(got)
	if len(have) != len(want) {
		t.Fatalf("%s: RunIDs %v, want %v", label, have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("%s: RunIDs %v, want %v", label, have, want)
		}
	}
}

// A RunID survives the log (both stores), a Checkpoint's tail, a Segment's
// rendered Messages, a reload, a Fork and a Document round trip; a Message
// with none comes back with none.
func TestRunIDSurvivesTheRecord(t *testing.T) {
	for name, store := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := session.CreateWriter(ctx, store, session.New(session.WithID("run-1")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			record := writer.Record()
			commit(t, writer, runMessage("u1", "user", "one", "run-a"))
			commit(t, writer, runMessage("a1", "assistant", "ok one", "run-a"))
			commit(t, writer, runMessage("u2", "user", "two", "run-b"))
			commit(t, writer, idMessage("a2", "assistant", "ok two")) // outside a Run
			reloaded, err := session.Open(ctx, store, "run-1")
			if err != nil {
				t.Fatal(err)
			}
			sameRunIDs(t, "reload", reloaded.Messages(), "run-a", "run-a", "run-b", "")

			rendered := rendered(0, "first", "reply")
			rendered.Messages[0].RunID, rendered.Messages[1].RunID = "run-a", "run-a"
			if _, err := record.CloseSegment(rendered); err != nil {
				t.Fatal(err)
			}
			if err := record.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, record.Tail()[2:], nil); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			sameRunIDs(t, "checkpointed", record.Messages(), "run-a", "run-a", "run-b", "")
			reopened, err := session.Open(ctx, store, "run-1")
			if err != nil {
				t.Fatal(err)
			}
			sameRunIDs(t, "reopened", reopened.Messages(), "run-a", "run-a", "run-b", "")
			sameRunIDs(t, "reopened Segment", reopened.Segments()[0].Messages, "run-a", "run-a")

			child := session.Fork(reopened, session.WithID("run-child"))
			sameRunIDs(t, "fork", child.Messages(), "run-a", "run-a", "run-b", "")
			sameRunIDs(t, "fork Segment", child.Segments()[0].Messages, "run-a", "run-a")
			childWriter, err := session.CreateWriter(ctx, store, child, "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			_ = childWriter
			stored, err := session.Open(ctx, store, "run-child")
			if err != nil {
				t.Fatal(err)
			}
			sameRunIDs(t, "stored fork", stored.Messages(), "run-a", "run-a", "run-b", "")

			data, err := json.Marshal(reopened.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			var doc session.Document
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			back, err := session.FromDocument(doc)
			if err != nil {
				t.Fatal(err)
			}
			sameRunIDs(t, "document", back.Messages(), "run-a", "run-a", "run-b", "")
		})
	}
}

// A Message written before RunID existed has none, and loads without error.
func TestMessageWithoutRunIDLoadsEmpty(t *testing.T) {
	var message gotato.Message
	if err := json.Unmarshal([]byte(`{"id":"m1","role":"user","parts":[{"kind":"text","text":"old"}]}`), &message); err != nil {
		t.Fatal(err)
	}
	if message.RunID != "" {
		t.Fatalf("RunID = %q, want empty", message.RunID)
	}
	if data, _ := json.Marshal(message); strings.Contains(string(data), "run_id") {
		t.Fatalf("an empty RunID is written: %s", data)
	}
}

// The reply a crash cut off is rebuilt from its kept parts, with the Run
// that wrote them.
func TestCutOffReplyRebuiltFromPartsKeepsItsRun(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(ctx, store, session.New(session.WithID("run-cut")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	header := gotato.NewHeader("run-cut", "run-x", 1)
	if _, err := writer.Write(ctx, []gotato.Event{gotato.ReplyPartKept{Header: header, MessageID: "partial", Text: "half a reply"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := session.View(ctx, store, "run-cut")
	if err != nil {
		t.Fatal(err)
	}
	messages := view.Messages()
	if len(messages) != 1 || messages[0].StopReason != gotato.StopReasonCutOff || messages[0].RunID != "run-x" {
		t.Fatalf("rebuilt Messages = %+v, want one cut-off reply of run-x", messages)
	}
}
