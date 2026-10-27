package gotato_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

func TestSegmentClosedRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 2, 0, 0, time.UTC)
	segment := gotato.Segment{
		Number: 2, From: at, To: at.Add(time.Hour),
		Source:   gotato.SegmentSource{SessionID: "parent", FromNumber: 3, ToNumber: 40},
		Messages: []gotato.Message{{ID: "m1", Role: gotato.RoleUser, Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "hi"}}}},
		Rendered: 1,
		ToolCalls: []gotato.ToolCallLine{
			{At: at, ToolID: "logs.query", Args: `{"a":1}`, Status: gotato.ToolResultStatusOK, Count: 2},
		},
		Recap: "## What happened\n- x", LoadedTools: []string{"logs.tail"},
	}
	event := gotato.SegmentClosed{Header: gotato.Header{Time: at, SessionID: "s1", Number: 9}, Segment: segment}
	data, err := gotato.MarshalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gotato.UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind() != gotato.EventKindSegmentClosed || got.Kind().Class() != gotato.EventClassStored {
		t.Fatalf("kind %q class %v, want a stored segment_closed", got.Kind(), got.Kind().Class())
	}
	if !reflect.DeepEqual(got.(gotato.SegmentClosed).Segment, segment) {
		t.Fatalf("round trip = %+v, want %+v", got.(gotato.SegmentClosed).Segment, segment)
	}
}

func TestSegmentCloneSharesNothing(t *testing.T) {
	segment := gotato.Segment{Messages: []gotato.Message{gotato.UserMessage("a")}, LoadedTools: []string{"x"}, ToolCalls: []gotato.ToolCallLine{{ToolID: "t"}}}
	clone := segment.Clone()
	clone.Messages[0].Parts[0].Text = "b"
	clone.LoadedTools[0], clone.ToolCalls[0].ToolID = "y", "u"
	if gotato.TextOf(segment.Messages[0]) != "a" || segment.LoadedTools[0] != "x" || segment.ToolCalls[0].ToolID != "t" {
		t.Fatal("a clone shares memory with its Segment")
	}
}

// A merged Segment names the Segments it stands for, round-trips with them,
// and shares nothing with its clone.
func TestMergedSegmentNamesWhatItStandsFor(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 2, 0, 0, time.UTC)
	merged := gotato.Segment{Number: 7, Merged: []gotato.MergedSegment{
		{Number: 1, From: at, To: at.Add(time.Hour), Source: gotato.SegmentSource{FromNumber: 3, ToNumber: 40}},
		{Number: 2, Source: gotato.SegmentSource{SessionID: "parent", FromNumber: 41, ToNumber: 60}},
	}}
	if got := merged.Numbers(); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("Numbers = %v, want [1 2]", got)
	}
	if got := (gotato.Segment{Number: 3}).Numbers(); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("Numbers of an ordinary Segment = %v, want [3]", got)
	}
	data, err := gotato.MarshalEvent(gotato.SegmentClosed{Header: gotato.Header{SessionID: "s1", Number: 9}, Segment: merged})
	if err != nil {
		t.Fatal(err)
	}
	got, err := gotato.UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.(gotato.SegmentClosed).Segment, merged) {
		t.Fatalf("round trip = %+v, want %+v", got.(gotato.SegmentClosed).Segment, merged)
	}
	clone := merged.Clone()
	clone.Merged[0].Number = 99
	if merged.Merged[0].Number != 1 {
		t.Fatal("a clone shares its merged list with the Segment")
	}
}
