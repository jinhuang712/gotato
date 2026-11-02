package record_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
)

func checkpointOf(t *testing.T, rec *record.Record) gotato.CompactionEnded {
	t.Helper()
	for _, event := range slices.Backward(rec.Events()) {
		if ended, ok := event.(gotato.CompactionEnded); ok {
			return ended
		}
	}
	t.Fatal("no Checkpoint")
	return gotato.CompactionEnded{}
}

// CloseSegment lists the Segments of the Context itself: the caller cannot
// drop or move one, since it never names them.
func TestCloseSegmentListsTheContextsSegmentsItself(t *testing.T) {
	rec := record.New(record.WithID("lists"))
	record.StartLog(rec)
	for i, texts := range [][]string{{"one", "r1"}, {"two", "r2"}, {"three", "r3"}} {
		closed, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, rendered(0, texts...), nil, nil)
		if err != nil || closed.Number != i+1 {
			t.Fatalf("CloseSegment %d = %+v, %v", i+1, closed, err)
		}
		var numbers []int
		for _, segment := range rec.Segments() {
			numbers = append(numbers, segment.Number)
		}
		want := []int{1, 2, 3}[:i+1]
		if !reflect.DeepEqual(numbers, want) || !reflect.DeepEqual(checkpointOf(t, rec).Segments, want) {
			t.Fatalf("after Segment %d the Context lists %v and the Checkpoint %v, want %v", i+1, numbers, checkpointOf(t, rec).Segments, want)
		}
	}
}

// The loaded Tools a compaction keeps are set by the same step that writes the
// Checkpoint, so the Checkpoint holds them; a compaction with none recorded
// leaves them as they are, and a refused call changes nothing.
func TestCloseSegmentSetsTheLoadedToolsFromTheCompaction(t *testing.T) {
	rec := record.New(record.WithID("loaded"))
	record.StartLog(rec)
	rec.SetLoadedTools([]string{"files.read", "files.write", "crm.find"})

	if _, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, rendered(0, "one", "r1"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := rec.LoadedTools(); !reflect.DeepEqual(got, []string{"files.read", "files.write", "crm.find"}) {
		t.Fatalf("a close with no compaction changed the loaded Tools: %v", got)
	}

	kept := []string{"crm.find"}
	if _, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, rendered(0, "two", "r2"), nil, &gotato.Compaction{LoadedTools: kept}); err != nil {
		t.Fatal(err)
	}
	if got := rec.LoadedTools(); !reflect.DeepEqual(got, kept) {
		t.Fatalf("loaded Tools = %v, want %v", got, kept)
	}
	if got := checkpointOf(t, rec).LoadedTools; !reflect.DeepEqual(got, kept) {
		t.Fatalf("the Checkpoint holds %v, want %v", got, kept)
	}

	// A refused call leaves the loaded Tools alone.
	rec.SetLoadedTools([]string{"a", "b"})
	taken := rendered(1, "again", "r")
	if _, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, taken, nil, &gotato.Compaction{LoadedTools: nil}); err == nil {
		t.Fatal("a Segment number already closed was accepted")
	}
	if _, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, mergedOf(1, 2), nil, &gotato.Compaction{LoadedTools: nil}); err == nil {
		t.Fatal("a merged Segment was accepted")
	}
	if got := rec.LoadedTools(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("a refused close changed the loaded Tools: %v", got)
	}

	// A compaction that keeps none unloads them all.
	if _, err := rec.CloseSegment(gotato.CompactionWhyOnDemand, rendered(0, "three", "r3"), nil, &gotato.Compaction{}); err != nil {
		t.Fatal(err)
	}
	if got := rec.LoadedTools(); len(got) != 0 {
		t.Fatalf("loaded Tools = %v, want none", got)
	}
}
