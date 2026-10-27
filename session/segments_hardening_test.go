package session_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A Checkpoint lists each Segment once, in the order they were closed, and
// only Segments that are closed. A refused call changes nothing.
func TestCheckpointSegmentsRefusesDuplicateOutOfOrderAndUnclosed(t *testing.T) {
	record := session.New()
	for _, number := range []int{1, 2} {
		if _, err := record.CloseSegment(rendered(number, "a", "b")); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		numbers []int
		want    string
	}{
		{"duplicate", []int{1, 1}, "twice"},
		{"out of order", []int{2, 1}, "ascending"},
		{"never closed", []int{1, 2, 3}, "not closed"},
	}
	for _, c := range cases {
		err := record.CheckpointSegments(gotato.CompactionOnDemand, c.numbers, nil, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Fatalf("%s: CheckpointSegments(%v) = %v, want invalid_argument naming %q", c.name, c.numbers, err, c.want)
		}
		if len(record.Segments()) != 0 || record.Len() != 0 {
			t.Fatalf("%s: the refused call changed the Context", c.name)
		}
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1, 2}, nil, nil); err != nil {
		t.Fatalf("a valid list was refused: %v", err)
	}
}

// CloseSegmentAndCheckpoint writes the Segment and the Checkpoint that lists
// it as neighbouring Entries of one batch, so nothing stored holds the one
// without the other.
func TestCloseSegmentAndCheckpointWritesBothTogether(t *testing.T) {
	for name, store := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := session.Create(ctx, store, session.New(session.WithID("seg-both")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			record := writer.Record()
			for _, message := range []gotato.Message{idMessage("u1", "user", "one"), idMessage("a1", "assistant", "ok one"), idMessage("u2", "user", "two")} {
				commit(t, writer, message)
			}
			if _, err := record.CloseSegmentAndCheckpoint(gotato.CompactionOnDemand, rendered(0, "one", "ok one"), []int{9}, record.Tail()[2:], nil); err == nil {
				t.Fatal("an earlier Segment that is not closed was accepted")
			}
			if record.NextSegment() != 1 || len(record.Segments()) != 0 {
				t.Fatal("a refused call closed or listed a Segment")
			}
			closed, err := record.CloseSegmentAndCheckpoint(gotato.CompactionOnDemand, rendered(0, "one", "ok one"), nil, record.Tail()[2:], nil)
			if err != nil || closed.Number != 1 || closed.Rendered != 2 {
				t.Fatalf("CloseSegmentAndCheckpoint = %+v, %v", closed, err)
			}
			if got := record.Segments(); len(got) != 1 || got[0].Number != 1 || record.Len() != 3 {
				t.Fatalf("Context = %d Segments, %d Messages; want 1 and 3", len(got), record.Len())
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			entries, err := store.Log(ctx, "seg-both", 0)
			if err != nil {
				t.Fatal(err)
			}
			for i, entry := range entries {
				if entry.Kind != gotato.KindSegmentClosed {
					continue
				}
				if i+1 >= len(entries) || entries[i+1].Kind != gotato.KindCompactionEnded || entries[i+1].Seq != entry.Seq+1 {
					t.Fatalf("segment_closed is not followed by the Checkpoint that lists it")
				}
				return
			}
			t.Fatal("no segment_closed Entry was written")
		})
	}
}

// A Document carries the largest Segment number, so a Segment the store holds
// and no Checkpoint lists keeps its number reserved across Snapshot and Load.
func TestLoadKeepsTheLargestSegmentNumber(t *testing.T) {
	record := session.New(session.WithID("seg-max"))
	if _, err := record.CloseSegment(rendered(0, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := record.CloseSegment(rendered(0, "c", "d")); err != nil { // closed alone: number 2, listed by nothing
		t.Fatal(err)
	}
	doc := record.Snapshot()
	if doc.MaxSegment != 2 {
		t.Fatalf("Document.MaxSegment = %d, want 2", doc.MaxSegment)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var back session.Document
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Load(back)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NextSegment() != 3 {
		t.Fatalf("NextSegment after Load = %d, want 3", loaded.NextSegment())
	}
	// A Document written before the field existed still loads from its Segments.
	old := record.Snapshot()
	old.MaxSegment = 0
	if again, err := session.Load(old); err != nil || again.NextSegment() != 2 {
		t.Fatalf("a Document with no max_segment: next %d, %v; want 2", again.NextSegment(), err)
	}
}
