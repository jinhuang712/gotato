package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// closeAndList closes a Segment of two Messages and lists it after the earlier
// ones, the way a compaction does.
func closeAndList(t *testing.T, record *session.Record, earlier []int, texts ...string) gotato.Segment {
	t.Helper()
	closed, err := record.CloseSegmentAndCheckpoint(gotato.CompactionOnDemand, rendered(0, texts...), earlier, nil, nil)
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
	for name, store := range segmentStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writer, err := session.Create(ctx, store, session.New(session.WithID("merge-1")), "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			record := writer.Record()
			closeAndList(t, record, nil, "one", "r1")
			closeAndList(t, record, []int{1}, "two", "r2")
			closeAndList(t, record, []int{1, 2}, "three", "r3")
			commit(t, writer, idMessage("tail-1", "user", "the tail"))
			keptBefore, _ := json.Marshal(record.Segments()[2].Messages)

			merged, err := record.MergeSegments(gotato.CompactionPastBudget, mergedOf(1, 2), []int{1, 2}, record.Tail(), &session.Compaction{Segment: 4, Merged: []int{1, 2}})
			if err != nil || merged.Number != 4 || merged.Rendered != 1 {
				t.Fatalf("MergeSegments = %+v, %v; want segment 4 rendered by 1 Message", merged, err)
			}
			numbers := func(r *session.Record) (out []int) {
				for _, segment := range r.Segments() {
					out = append(out, segment.Number)
				}
				return out
			}
			if got := numbers(record); !reflect.DeepEqual(got, []int{4, 3}) {
				t.Fatalf("Segments = %v, want the merged 4 first, then 3", got)
			}
			if err := writer.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			reopened, err := session.Open(ctx, store, "merge-1")
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
			if _, err := reopened.MergeSegments(gotato.CompactionPastBudget, mergedOf(1, 2, 3), []int{4, 3}, reopened.Tail(), nil); err != nil {
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
	record := session.New(session.WithID("merge-2"))
	closeAndList(t, record, nil, "one", "r1")
	closeAndList(t, record, []int{1}, "two", "r2")
	closeAndList(t, record, []int{1, 2}, "three", "r3")
	events := len(record.Events())
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
		if _, err := record.MergeSegments(gotato.CompactionPastBudget, tc.merged, tc.replaces, record.Tail(), nil); !gotato.IsCode(err, gotato.ErrInvalidArgument) {
			t.Errorf("%s: error = %v, want invalid_argument", name, err)
		}
	}
	if len(record.Events()) != events || len(record.Segments()) != 3 {
		t.Fatalf("a refused merge changed the Record: %d Events, %d Segments", len(record.Events()), len(record.Segments()))
	}
	// A merged Segment is never closed as an ordinary one, and never listed
	// after another Segment.
	if _, err := record.CloseSegmentAndCheckpoint(gotato.CompactionOnDemand, mergedOf(1), []int{1, 2, 3}, nil, nil); err == nil {
		t.Error("CloseSegmentAndCheckpoint accepted a merged Segment")
	}
	merged, err := record.CloseSegment(mergedOf(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err := record.CheckpointSegments(gotato.CompactionOnDemand, []int{1, merged.Number}, nil, nil); err == nil {
		t.Error("a merged Segment was listed after another Segment")
	}
}

// A fork inherits the merged Segment with where each Segment it stands for can
// be read: the parent's Record.
func TestAForkKeepsWhereTheMergedSegmentsCanBeRead(t *testing.T) {
	record := session.New(session.WithID("merge-parent"))
	closeAndList(t, record, nil, "one", "r1")
	closeAndList(t, record, []int{1}, "two", "r2")
	merged := mergedOf(1, 2)
	merged.Merged[0].Source = gotato.SegmentSource{FromNumber: 3, ToNumber: 6}
	if _, err := record.MergeSegments(gotato.CompactionPastBudget, merged, []int{1, 2}, record.Tail(), nil); err != nil {
		t.Fatal(err)
	}
	child := session.Fork(record, session.WithID("merge-child"))
	got := child.Segments()
	if len(got) != 1 || len(got[0].Merged) != 2 || got[0].Merged[0].Source.SessionID != "merge-parent" || got[0].Merged[0].Source.FromNumber != 3 {
		t.Fatalf("the fork's merged Segment = %+v", got)
	}
}
