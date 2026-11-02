package record_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
)

// A Segment is written once as its own Entry; the Checkpoint lists its number
// and keeps the tail, and a load resolves the number to the stored Messages.
func TestCheckpointSegmentsPointsToTheStoredSegment(t *testing.T) {
	for name, st := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("seg-1")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			rec := writer.Record()
			for _, message := range []gotato.Message{idMessage("u1", "user", "one"), idMessage("a1", "assistant", "ok one"), idMessage("u2", "user", "two"), idMessage("a2", "assistant", "ok two")} {
				commit(t, writer, message)
			}
			first, err := rec.CloseWithoutCheckpoint(rendered(0, "SEGMENT-ONE-TEXT", "reply"))
			if err != nil || first.Number != 1 || first.Rendered != 2 {
				t.Fatalf("CloseSegment = %+v, %v; want number 1 rendered by 2 Messages", first, err)
			}
			if len(rec.Segments()) != 0 {
				t.Fatal("a closed Segment is in the Context before a Checkpoint lists it")
			}
			if err := rec.CheckpointSegments(gotato.CompactionWhyPastCeiling, []int{1}, rec.Tail()[2:], &gotato.Compaction{Segment: 1, Recap: "none"}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			commit(t, writer, idMessage("u3", "user", "three"))

			entries, err := st.Log(ctx, "seg-1", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []gotato.EventKind
			for _, entry := range entries {
				kinds = append(kinds, entry.Kind)
				if entry.Kind == gotato.EventKindCompactionEnded && strings.Contains(string(entry.Data), "SEGMENT-ONE-TEXT") {
					t.Fatalf("the Checkpoint copies the Segment's Messages: %s", entry.Data)
				}
			}
			want := []gotato.EventKind{gotato.EventKindSessionCreated, gotato.EventKindSessionClaimed, gotato.EventKindMessageCommitted, gotato.EventKindReplyEnded, gotato.EventKindMessageCommitted, gotato.EventKindReplyEnded, gotato.EventKindSegmentClosed, gotato.EventKindCompactionEnded, gotato.EventKindMessageCommitted}
			if len(kinds) != len(want) {
				t.Fatalf("kinds = %v, want %v", kinds, want)
			}
			for i := range want {
				if kinds[i] != want[i] {
					t.Fatalf("kinds = %v, want %v", kinds, want)
				}
			}
			loaded, err := st.Load(ctx, "seg-1")
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Entries[0].Kind != gotato.EventKindCompactionEnded || len(loaded.Segments) != 1 || loaded.Segments[0].Kind != gotato.EventKindSegmentClosed {
				t.Fatalf("Load = facts from %s, %d segment Entries; want the Checkpoint and one segment_closed Entry before it", loaded.Entries[0].Kind, len(loaded.Segments))
			}

			reopened, err := store.Open(ctx, st, "seg-1")
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
	for name, st := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("seg-2")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			rec := writer.Record()
			commit(t, writer, idMessage("u1", "user", "one"))
			commit(t, writer, idMessage("a1", "assistant", "ok one"))
			if _, err := rec.CloseWithoutCheckpoint(rendered(0, "first", "first reply")); err != nil {
				t.Fatal(err)
			}
			if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, nil, nil); err != nil {
				t.Fatal(err)
			}
			commit(t, writer, idMessage("u2", "user", "two"))
			commit(t, writer, idMessage("a2", "assistant", "ok two"))
			commit(t, writer, idMessage("u3", "user", "three"))
			bytesOf := func(r *record.Record) []byte {
				data, _ := json.Marshal(r.Segments()[0].Messages)
				return data
			}
			before := bytesOf(rec)

			if _, err := rec.CloseWithoutCheckpoint(rendered(0, "second", "second reply")); err != nil {
				t.Fatal(err)
			}
			if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1, 2}, rec.Tail()[2:], nil); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, bytesOf(rec)) {
				t.Fatal("Segment 1's Messages changed when Segment 2 was closed")
			}
			reopened, err := store.Open(ctx, st, "seg-2")
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
			view, err := store.View(ctx, st, "seg-2")
			if err != nil || view.Len() != reopened.Len() || len(view.Segments()) != 2 {
				t.Fatalf("View = %v, %v", view, err)
			}
			events, err := store.StoredEvents(ctx, st, "seg-2", 0, 0)
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
	st := store.NewMemoryStore()
	writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("pos-1")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	rec := writer.Record()
	events := commit(t, writer, idMessage("u1", "user", "one"))
	commit(t, writer, idMessage("a1", "assistant", "ok one"))
	commit(t, writer, idMessage("u2", "user", "two"))
	position, ok := rec.Position("u1")
	if !ok || position.Number != events[0].Head().Number || position.Time.IsZero() {
		t.Fatalf("Position(u1) = %+v, %v; want the Entry number %d and a time", position, ok, events[0].Head().Number)
	}
	if _, ok := rec.Position("nobody"); ok {
		t.Fatal("Position invented a Message")
	}
	kept := rec.Tail()[2:]
	if _, err := rec.CloseWithoutCheckpoint(rendered(0, "first", "reply")); err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, kept, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.Position("u1"); ok {
		t.Fatal("the index holds a Message from before the Checkpoint that it did not keep")
	}
	again, ok := rec.Position("u2")
	if !ok || again.Number == 0 {
		t.Fatalf("Position(u2) = %+v, %v after the Checkpoint", again, ok)
	}
	reopened, err := store.Open(ctx, st, "pos-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded, ok := reopened.Position("u2"); !ok || loaded.Number != again.Number || !loaded.Time.Equal(again.Time) {
		t.Fatalf("a load lost the kept tail's position: %+v, %v, want %+v", loaded, ok, again)
	}
	commit(t, writer, idMessage("u3", "user", "three"))
	reopened, err = store.Open(ctx, st, "pos-1")
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := reopened.Position("u3"); !ok || p.Number == 0 {
		t.Fatalf("a load did not index the Messages after the Checkpoint: %+v, %v", p, ok)
	}
}

func TestCheckpointSegmentsRefusesWhatIsNotClosed(t *testing.T) {
	rec := record.New()
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, nil, nil); err == nil {
		t.Fatal("CheckpointSegments accepted a Segment that is not closed")
	}
	if _, err := rec.CloseWithoutCheckpoint(rendered(3, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.CloseWithoutCheckpoint(rendered(3, "a", "b")); err == nil {
		t.Fatal("CloseSegment closed number 3 twice")
	}
	if rec.NextSegment() != 4 {
		t.Fatalf("NextSegment = %d, want 4", rec.NextSegment())
	}
}

// Checkpoint, which replaces the Context with the Messages it is given, leaves
// no Segments in it.
func TestCheckpointWithMessagesLeavesNoSegments(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("seg-3")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	rec := writer.Record()
	commit(t, writer, idMessage("u1", "user", "one"))
	if _, err := rec.CloseWithoutCheckpoint(rendered(0, "first", "reply")); err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rec.Checkpoint(gotato.CompactionWhyOnDemand, []gotato.Message{gotato.UserMessage("summary")}, nil)
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, st, "seg-3")
	if err != nil || len(reopened.Segments()) != 0 || reopened.Len() != 1 || reopened.NextSegment() != 2 {
		t.Fatalf("reopened: %d Segments, %d Messages, next %d, %v", len(reopened.Segments()), reopened.Len(), reopened.NextSegment(), err)
	}
}

// A fork copies the Segments of its parent's Context, each naming the parent's
// Session in its Source. They are stored in the fork's own log, so a load of
// the fork finds them after a later Checkpoint too.
func TestForkCopiesSegments(t *testing.T) {
	for name, st := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			parent := record.New(record.WithID("parent-1"))
			_ = parent.Append(idMessage("u1", "user", "one"))
			if _, err := parent.CloseWithoutCheckpoint(rendered(0, "first", "reply")); err != nil {
				t.Fatal(err)
			}
			if err := parent.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, []gotato.Message{idMessage("u2", "user", "two")}, nil); err != nil {
				t.Fatal(err)
			}
			child := record.Fork(parent, record.WithID("child-1"))
			segments := child.Segments()
			if len(segments) != 1 || segments[0].Number != 1 || segments[0].Source.SessionID != "parent-1" || child.NextSegment() != 2 || child.Len() != 3 {
				t.Fatalf("fork: Segments %+v, next %d, %d Messages", segments, child.NextSegment(), child.Len())
			}
			if parent.Segments()[0].Source.SessionID != "" {
				t.Fatal("forking changed the parent's Segment")
			}
			writer, err := store.CreateWriter(ctx, st, child, "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			commit(t, writer, idMessage("u3", "user", "three"))
			if _, err := writer.Record().CloseWithoutCheckpoint(rendered(0, "second", "reply two")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Record().CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1, 2}, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(ctx, st, "child-1")
			if err != nil {
				t.Fatal(err)
			}
			got := reopened.Segments()
			if len(got) != 2 || got[0].Source.SessionID != "parent-1" || got[1].Number != 2 || got[1].Source.SessionID != "" || reopened.Len() != 4 {
				t.Fatalf("reopened fork: %+v, %d Messages", got, reopened.Len())
			}
			// A load from the fork's first Entry reads the same.
			view, err := store.View(ctx, st, "child-1")
			if err != nil || view.Len() != 4 {
				t.Fatalf("View = %v, %v", view, err)
			}
		})
	}
}

func TestADocumentKeepsSegments(t *testing.T) {
	rec := record.New()
	_ = rec.Append(idMessage("u1", "user", "one"))
	if _, err := rec.CloseWithoutCheckpoint(rendered(0, "first", "reply")); err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, rec.Tail(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rec.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var doc record.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	back, err := record.FromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Segments()) != 1 || back.Len() != rec.Len() || back.NextSegment() != 2 {
		t.Fatalf("loaded: %d Segments, %d Messages", len(back.Segments()), back.Len())
	}
}

// A Checkpoint lists each Segment once, in the order they were closed, and
// only Segments that are closed. A refused call changes nothing.
func TestCheckpointSegmentsRefusesDuplicateOutOfOrderAndUnclosed(t *testing.T) {
	rec := record.New()
	for _, number := range []int{1, 2} {
		if _, err := rec.CloseWithoutCheckpoint(rendered(number, "a", "b")); err != nil {
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
		err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, c.numbers, nil, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Fatalf("%s: CheckpointSegments(%v) = %v, want invalid_argument naming %q", c.name, c.numbers, err, c.want)
		}
		if len(rec.Segments()) != 0 || rec.Len() != 0 {
			t.Fatalf("%s: the refused call changed the Context", c.name)
		}
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1, 2}, nil, nil); err != nil {
		t.Fatalf("a valid list was refused: %v", err)
	}
}

// CloseSegment writes the Segment and the Checkpoint that lists it as
// neighbouring Entries of one batch, so nothing stored holds the one without
// the other.
func TestCloseSegmentWritesBothTogether(t *testing.T) {
	for name, st := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("seg-both")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			rec := writer.Record()
			for _, message := range []gotato.Message{idMessage("u1", "user", "one"), idMessage("a1", "assistant", "ok one"), idMessage("u2", "user", "two")} {
				commit(t, writer, message)
			}
			closed, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, rendered(0, "one", "ok one"), rec.Tail()[2:], nil)
			if err != nil || closed.Number != 1 || closed.Rendered != 2 {
				t.Fatalf("CloseSegment = %+v, %v", closed, err)
			}
			if got := rec.Segments(); len(got) != 1 || got[0].Number != 1 || rec.Len() != 3 {
				t.Fatalf("Context = %d Segments, %d Messages; want 1 and 3", len(got), rec.Len())
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			entries, err := st.Log(ctx, "seg-both", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for i, entry := range entries {
				if entry.Kind != gotato.EventKindSegmentClosed {
					continue
				}
				if i+1 >= len(entries) || entries[i+1].Kind != gotato.EventKindCompactionEnded || entries[i+1].Seq != entry.Seq+1 {
					t.Fatalf("segment_closed is not followed by the Checkpoint that lists it")
				}
				return
			}
			t.Fatal("no segment_closed Entry was written")
		})
	}
}

// A Record that holds a Segment no Checkpoint lists keeps its number reserved
// while it lives; a Load reads the largest number from the Segments the
// Document lists.
func TestLoadReadsTheLargestListedSegmentNumber(t *testing.T) {
	rec := record.New(record.WithID("seg-max"))
	if _, err := rec.CloseWithoutCheckpoint(rendered(0, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.CloseWithoutCheckpoint(rendered(0, "c", "d")); err != nil { // closed alone: number 2, listed by nothing
		t.Fatal(err)
	}
	if rec.NextSegment() != 3 {
		t.Fatalf("NextSegment = %d, want 3", rec.NextSegment())
	}
	doc := rec.Snapshot()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var back record.Document
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	loaded, err := record.FromDocument(back)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NextSegment() != 2 {
		t.Fatalf("NextSegment after Load = %d, want 2, one more than the largest listed Segment", loaded.NextSegment())
	}
}

// closeAndList closes a Segment of two Messages and lists it after the earlier
// ones, the way a compaction does.
func closeAndList(t *testing.T, rec *record.Record, texts ...string) gotato.Segment {
	t.Helper()
	closed, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, rendered(0, texts...), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return closed
}

func mergedOf(numbers ...int) gotato.Segment {
	segment := gotato.Segment{}
	for _, n := range numbers {
		segment.Merged = append(segment.Merged, gotato.MergedSegment{Number: n})
	}
	segment.Messages = []gotato.Message{idMessage("merged-1", "user", "<segment>merged</segment>")}
	segment.Recap = "## What happened\n- merged"
	return segment
}

// A merge replaces the oldest Segments with one merged Segment that opens the
// Context; the Segments after keep their bytes, and a load reads the same.
func TestMergeSegmentsPutsOneMergedSegmentFirst(t *testing.T) {
	for name, st := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := store.CreateWriter(ctx, st, record.New(record.WithID("merge-1")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			rec := writer.Record()
			closeAndList(t, rec, "one", "r1")
			closeAndList(t, rec, "two", "r2")
			closeAndList(t, rec, "three", "r3")
			commit(t, writer, idMessage("tail-1", "user", "the tail"))
			keptBefore, _ := json.Marshal(rec.Segments()[2].Messages)

			merged, err := rec.MergeSegments(gotato.CompactionWhyPastBudget, mergedOf(1, 2), []int{1, 2}, rec.Tail(), &gotato.Compaction{Segment: 4, Merged: []int{1, 2}})
			if err != nil || merged.Number != 4 || merged.Rendered != 1 {
				t.Fatalf("MergeSegments = %+v, %v; want segment 4 rendered by 1 Message", merged, err)
			}
			numbers := func(r *record.Record) (out []int) {
				for _, segment := range r.Segments() {
					out = append(out, segment.Number)
				}
				return out
			}
			if got := numbers(rec); !reflect.DeepEqual(got, []int{4, 3}) {
				t.Fatalf("Segments = %v, want the merged 4 first, then 3", got)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(ctx, st, "merge-1")
			if err != nil {
				t.Fatal(err)
			}
			if got := numbers(reopened); !reflect.DeepEqual(got, []int{4, 3}) {
				t.Fatalf("reopened Segments = %v", got)
			}
			if kept, _ := json.Marshal(reopened.Segments()[1].Messages); !bytes.Equal(kept, keptBefore) {
				t.Fatal("the Segment a merge left alone changed its bytes")
			}
			var texts []string
			for _, message := range reopened.Messages() {
				texts = append(texts, gotato.TextOf(message))
			}
			if got := strings.Join(texts, "|"); got != "<segment>merged</segment>|three|r3|the tail" {
				t.Fatalf("Context = %s", got)
			}
			if got := reopened.Segments()[0].Numbers(); !reflect.DeepEqual(got, []int{1, 2}) {
				t.Fatalf("the merged Segment stands for %v", got)
			}
			if reopened.NextSegment() != 5 {
				t.Fatalf("next Segment = %d, want 5", reopened.NextSegment())
			}
			if c := reopened.Compactions(); len(c) != 1 || !reflect.DeepEqual(c[0].Merged, []int{1, 2}) {
				t.Fatalf("Compactions = %+v", c)
			}

			// The next merge takes the merged Segment with the oldest after it.
			if _, err := reopened.MergeSegments(gotato.CompactionWhyPastBudget, mergedOf(1, 2, 3), []int{4, 3}, reopened.Tail(), nil); err != nil {
				t.Fatal(err)
			}
			if got := numbers(reopened); !reflect.DeepEqual(got, []int{5}) {
				t.Fatalf("Segments after the second merge = %v", got)
			}
		})
	}
}

// A merge replaces the Context's first Segments, names the Segments it stands
// for, and leaves the merged Segment first: anything else is refused, and
// nothing is written.
func TestMergeSegmentsRefusesWhatIsNotAMerge(t *testing.T) {
	rec := record.New(record.WithID("merge-2"))
	closeAndList(t, rec, "one", "r1")
	closeAndList(t, rec, "two", "r2")
	closeAndList(t, rec, "three", "r3")
	events := len(rec.Events())
	for name, tc := range map[string]struct {
		merged   gotato.Segment
		replaces []int
	}{
		"no Segments named":        {gotato.Segment{Messages: mergedOf(1).Messages}, []int{1}},
		"nothing replaced":         {mergedOf(1), nil},
		"not the first Segments":   {mergedOf(2, 3), []int{2, 3}},
		"more than the Context":    {mergedOf(1, 2, 3, 4), []int{1, 2, 3, 4}},
		"a number already closed":  {func() gotato.Segment { s := mergedOf(1, 2); s.Number = 3; return s }(), []int{1, 2}},
		"out of order replacement": {mergedOf(1, 2), []int{2, 1}},
	} {
		if _, err := rec.MergeSegments(gotato.CompactionWhyPastBudget, tc.merged, tc.replaces, rec.Tail(), nil); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want invalid_argument", name, err)
		}
	}
	if len(rec.Events()) != events || len(rec.Segments()) != 3 {
		t.Fatalf("a refused merge changed the Record: %d Events, %d Segments", len(rec.Events()), len(rec.Segments()))
	}
	// A merged Segment is never closed as an ordinary one, and never listed
	// after another Segment.
	if _, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, mergedOf(1), nil, nil); err == nil {
		t.Error("CloseSegment accepted a merged Segment")
	}
	merged, err := rec.CloseWithoutCheckpoint(mergedOf(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.CheckpointSegments(gotato.CompactionWhyOnDemand, []int{1, merged.Number}, nil, nil); err == nil {
		t.Error("a merged Segment was listed after another Segment")
	}
}

// A fork inherits the merged Segment with where each Segment it stands for can
// be read: the parent's Record.
func TestAForkKeepsWhereTheMergedSegmentsCanBeRead(t *testing.T) {
	rec := record.New(record.WithID("merge-parent"))
	closeAndList(t, rec, "one", "r1")
	closeAndList(t, rec, "two", "r2")
	merged := mergedOf(1, 2)
	merged.Merged[0].Source = gotato.SegmentSource{FromNumber: 3, ToNumber: 6}
	if _, err := rec.MergeSegments(gotato.CompactionWhyPastBudget, merged, []int{1, 2}, rec.Tail(), nil); err != nil {
		t.Fatal(err)
	}
	child := record.Fork(rec, record.WithID("merge-child"))
	got := child.Segments()
	if len(got) != 1 || len(got[0].Merged) != 2 || got[0].Merged[0].Source.SessionID != "merge-parent" || got[0].Merged[0].Source.FromNumber != 3 {
		t.Fatalf("the fork's merged Segment = %+v", got)
	}
}
