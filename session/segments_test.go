package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

func idMessage(id, role, text string) gotato.Message {
	message := gotato.UserMessage(text)
	if role == "assistant" {
		message = gotato.AssistantMessage(text)
	}
	message.ID = gotato.MessageID(id)
	return message
}

func rendered(number int, texts ...string) gotato.Segment {
	segment := gotato.Segment{Number: number, From: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)}
	for i, text := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		segment.Messages = append(segment.Messages, idMessage("seg"+string(rune('0'+number))+"-"+string(rune('a'+i)), role, text))
	}
	segment.Recap = "## What happened\n- " + texts[0]
	return segment
}

func segmentStores(t *testing.T) map[string]session.Store {
	t.Helper()
	files, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]session.Store{"memory": session.NewMemoryStore(), "file": files}
}

// A Segment is written once as its own Entry; the Checkpoint lists its number
// and keeps the tail, and a load resolves the number to the stored Messages.
func TestCheckpointSegmentsPointsToTheStoredSegment(t *testing.T) {
	for name, store := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := session.Create(ctx, store, session.New(session.WithID("seg-1")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			record := writer.Record()
			for _, message := range []gotato.Message{idMessage("u1", "user", "one"), idMessage("a1", "assistant", "ok one"), idMessage("u2", "user", "two"), idMessage("a2", "assistant", "ok two")} {
				commit(t, writer, message)
			}
			first, err := record.CloseSegment(rendered(0, "SEGMENT-ONE-TEXT", "reply"))
			if err != nil || first.Number != 1 || first.Rendered != 2 {
				t.Fatalf("CloseSegment = %+v, %v; want number 1 rendered by 2 Messages", first, err)
			}
			if len(record.Segments()) != 0 {
				t.Fatal("a closed Segment is in the Context before a Checkpoint lists it")
			}
			if err := record.CheckpointSegments(gotato.CompactionPastCeiling, []int{1}, record.Tail()[2:], &session.Compaction{Segment: 1, Summarizer: "none"}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			commit(t, writer, idMessage("u3", "user", "three"))

			entries, err := store.Log(ctx, "seg-1", 0)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []gotato.EventKind
			for _, entry := range entries {
				kinds = append(kinds, entry.Kind)
				if entry.Kind == gotato.KindCompactionEnded && strings.Contains(string(entry.Data), "SEGMENT-ONE-TEXT") {
					t.Fatalf("the Checkpoint copies the Segment's Messages: %s", entry.Data)
				}
			}
			want := []gotato.EventKind{gotato.KindSessionCreated, gotato.KindSessionClaimed, gotato.KindMessageCommitted, gotato.KindReplyEnded, gotato.KindMessageCommitted, gotato.KindReplyEnded, gotato.KindSegmentClosed, gotato.KindCompactionEnded, gotato.KindMessageCommitted}
			if len(kinds) != len(want) {
				t.Fatalf("kinds = %v, want %v", kinds, want)
			}
			for i := range want {
				if kinds[i] != want[i] {
					t.Fatalf("kinds = %v, want %v", kinds, want)
				}
			}
			loaded, err := store.Load(ctx, "seg-1")
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Facts[0].Kind != gotato.KindCompactionEnded || len(loaded.Segments) != 1 || loaded.Segments[0].Kind != gotato.KindSegmentClosed {
				t.Fatalf("Load = facts from %s, %d segment Entries; want the Checkpoint and one segment_closed Entry before it", loaded.Facts[0].Kind, len(loaded.Segments))
			}

			reopened, err := session.Open(ctx, store, "seg-1")
			if err != nil {
				t.Fatal(err)
			}
			var texts []string
			for _, message := range reopened.Messages() {
				texts = append(texts, gotato.TextOf(message))
			}
			if got := strings.Join(texts, "|"); got != "SEGMENT-ONE-TEXT|reply|two|ok two|three" {
				t.Fatalf("Context = %s", got)
			}
			segments := reopened.Segments()
			if len(segments) != 1 || segments[0].Number != 1 || segments[0].Recap == "" || len(reopened.Tail()) != 3 || reopened.NextSegment() != 2 {
				t.Fatalf("reopened: %d Segments, %d tail Messages, next %d", len(segments), len(reopened.Tail()), reopened.NextSegment())
			}
			if c := reopened.Compactions(); len(c) != 1 || c[0].Segment != 1 {
				t.Fatalf("Compactions = %+v", c)
			}
		})
	}
}

// A second compaction appends a new Segment and leaves the first as it was:
// the bytes of its Messages do not change, and the original Entries stay.
func TestASegmentIsNeverRewrittenByTheNextCompaction(t *testing.T) {
	for name, store := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := session.Create(ctx, store, session.New(session.WithID("seg-2")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			record := writer.Record()
			commit(t, writer, idMessage("u1", "user", "one"))
			commit(t, writer, idMessage("a1", "assistant", "ok one"))
			if _, err := record.CloseSegment(rendered(0, "first", "first reply")); err != nil {
				t.Fatal(err)
			}
			if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, nil, nil); err != nil {
				t.Fatal(err)
			}
			commit(t, writer, idMessage("u2", "user", "two"))
			commit(t, writer, idMessage("a2", "assistant", "ok two"))
			commit(t, writer, idMessage("u3", "user", "three"))
			bytesOf := func(r *session.Record) []byte {
				data, _ := json.Marshal(r.Segments()[0].Messages)
				return data
			}
			before := bytesOf(record)

			if _, err := record.CloseSegment(rendered(0, "second", "second reply")); err != nil {
				t.Fatal(err)
			}
			if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1, 2}, record.Tail()[2:], nil); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, bytesOf(record)) {
				t.Fatal("Segment 1's Messages changed when Segment 2 was closed")
			}
			reopened, err := session.Open(ctx, store, "seg-2")
			if err != nil {
				t.Fatal(err)
			}
			if got := reopened.Segments(); len(got) != 2 || got[1].Number != 2 || !bytes.Equal(before, bytesOf(reopened)) {
				t.Fatalf("reopened Segments = %+v", got)
			}
			if gotato.TextOf(reopened.Messages()[len(reopened.Messages())-1]) != "three" || reopened.Len() != 5 {
				t.Fatalf("Context = %d Messages", reopened.Len())
			}
			// The original Entries stay, and the whole log reads to the same Context.
			view, err := session.View(ctx, store, "seg-2")
			if err != nil || view.Len() != reopened.Len() || len(view.Segments()) != 2 {
				t.Fatalf("View = %v, %v", view, err)
			}
			events, err := session.StoredEvents(ctx, store, "seg-2", 0)
			if err != nil {
				t.Fatal(err)
			}
			commits := 0
			for _, event := range events {
				if _, ok := event.(gotato.MessageCommitted); ok {
					commits++
				}
			}
			if commits != 3 {
				t.Fatalf("the log holds %d user commits, want the 3 original ones", commits)
			}
		})
	}
}

func TestRecordKeepsWhereMessagesWereCommitted(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(session.WithID("pos-1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	record := writer.Record()
	events := commit(t, writer, idMessage("u1", "user", "one"))
	commit(t, writer, idMessage("a1", "assistant", "ok one"))
	commit(t, writer, idMessage("u2", "user", "two"))
	position, ok := record.Position("u1")
	if !ok || position.Number != events[0].Head().Number || position.Time.IsZero() {
		t.Fatalf("Position(u1) = %+v, %v; want the Entry number %d and a time", position, ok, events[0].Head().Number)
	}
	if _, ok := record.Position("nobody"); ok {
		t.Fatal("Position invented a Message")
	}
	kept := record.Tail()[2:]
	if _, err := record.CloseSegment(rendered(0, "first", "reply")); err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, kept, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := record.Position("u1"); ok {
		t.Fatal("the index holds a Message from before the Checkpoint that it did not keep")
	}
	again, ok := record.Position("u2")
	if !ok || again.Number == 0 {
		t.Fatalf("Position(u2) = %+v, %v after the Checkpoint", again, ok)
	}
	reopened, err := session.Open(ctx, store, "pos-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded, ok := reopened.Position("u2"); !ok || loaded.Number != again.Number || !loaded.Time.Equal(again.Time) {
		t.Fatalf("a load lost the kept tail's position: %+v, %v, want %+v", loaded, ok, again)
	}
	commit(t, writer, idMessage("u3", "user", "three"))
	reopened, err = session.Open(ctx, store, "pos-1")
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := reopened.Position("u3"); !ok || p.Number == 0 {
		t.Fatalf("a load did not index the Messages after the Checkpoint: %+v, %v", p, ok)
	}
}

func TestCheckpointSegmentsRefusesWhatIsNotClosed(t *testing.T) {
	record := session.New()
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, nil, nil); err == nil {
		t.Fatal("CheckpointSegments accepted a Segment that is not closed")
	}
	if _, err := record.CloseSegment(rendered(3, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := record.CloseSegment(rendered(3, "a", "b")); err == nil {
		t.Fatal("CloseSegment closed number 3 twice")
	}
	if record.NextSegment() != 4 {
		t.Fatalf("NextSegment = %d, want 4", record.NextSegment())
	}
}

// Checkpoint, which replaces the Context with the Messages it is given, leaves
// no Segments in it.
func TestCheckpointWithMessagesLeavesNoSegments(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	writer, err := session.Create(ctx, store, session.New(session.WithID("seg-3")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	record := writer.Record()
	commit(t, writer, idMessage("u1", "user", "one"))
	if _, err := record.CloseSegment(rendered(0, "first", "reply")); err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	record.Checkpoint(gotato.CompactionOnDemand, []gotato.Message{gotato.UserMessage("summary")}, nil)
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.Open(ctx, store, "seg-3")
	if err != nil || len(reopened.Segments()) != 0 || reopened.Len() != 1 || reopened.NextSegment() != 2 {
		t.Fatalf("reopened: %d Segments, %d Messages, next %d, %v", len(reopened.Segments()), reopened.Len(), reopened.NextSegment(), err)
	}
}

// A fork copies the Segments of its parent's Context, each naming the parent's
// Session in its Source. They are stored in the fork's own log, so a load of
// the fork finds them after a later Checkpoint too.
func TestForkCopiesSegments(t *testing.T) {
	for name, store := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			parent := session.New(session.WithID("parent-1"))
			_ = parent.Append(idMessage("u1", "user", "one"))
			if _, err := parent.CloseSegment(rendered(0, "first", "reply")); err != nil {
				t.Fatal(err)
			}
			if err := parent.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, []gotato.Message{idMessage("u2", "user", "two")}, nil); err != nil {
				t.Fatal(err)
			}
			child := session.Fork(parent, session.WithID("child-1"))
			segments := child.Segments()
			if len(segments) != 1 || segments[0].Number != 1 || segments[0].Source.SessionID != "parent-1" || child.NextSegment() != 2 || child.Len() != 3 {
				t.Fatalf("fork: Segments %+v, next %d, %d Messages", segments, child.NextSegment(), child.Len())
			}
			if parent.Segments()[0].Source.SessionID != "" {
				t.Fatal("forking changed the parent's Segment")
			}
			writer, err := session.Create(ctx, store, child, "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			commit(t, writer, idMessage("u3", "user", "three"))
			if _, err := writer.Record().CloseSegment(rendered(0, "second", "reply two")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Record().CheckpointSegments(gotato.CompactionOnDemand, []int{1, 2}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			reopened, err := session.Open(ctx, store, "child-1")
			if err != nil {
				t.Fatal(err)
			}
			got := reopened.Segments()
			if len(got) != 2 || got[0].Source.SessionID != "parent-1" || got[1].Number != 2 || got[1].Source.SessionID != "" || reopened.Len() != 4 {
				t.Fatalf("reopened fork: %+v, %d Messages", got, reopened.Len())
			}
			// A load from the fork's first Entry reads the same.
			view, err := session.View(ctx, store, "child-1")
			if err != nil || view.Len() != 4 {
				t.Fatalf("View = %v, %v", view, err)
			}
		})
	}
}

func TestADocumentKeepsSegments(t *testing.T) {
	record := session.New()
	_ = record.Append(idMessage("u1", "user", "one"))
	if _, err := record.CloseSegment(rendered(0, "first", "reply")); err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, record.Tail(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var doc session.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	back, err := session.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Segments()) != 1 || back.Len() != record.Len() || back.NextSegment() != 2 {
		t.Fatalf("loaded: %d Segments, %d Messages", len(back.Segments()), back.Len())
	}
}
