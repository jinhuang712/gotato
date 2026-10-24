package gotato_test

import (
	"reflect"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

func TestSegmentClosedRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 2, 0, 0, time.UTC)
	segment := gotato.Segment{
		Number: 2, From: at, To: at.Add(time.Hour),
		Source:   gotato.SegmentSource{SessionID: "parent", FromNumber: 3, ToNumber: 40},
		Messages: []gotato.Message{{ID: "m1", Role: gotato.RoleUser, Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "hi"}}}},
		Rendered: 1,
		ToolCalls: []gotato.ToolCallLine{
			{At: at, ToolID: "logs.query", Args: `{"a":1}`, Status: gotato.ToolResultOK, Count: 2},
		},
		Recap: "## What happened\n- x", ToolSets: []string{"logs"},
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
	if got.Kind() != gotato.KindSegmentClosed || got.Kind().Class() != gotato.ClassStored {
		t.Fatalf("kind %q class %v, want a stored segment_closed", got.Kind(), got.Kind().Class())
	}
	if !reflect.DeepEqual(got.(gotato.SegmentClosed).Segment, segment) {
		t.Fatalf("round trip = %+v, want %+v", got.(gotato.SegmentClosed).Segment, segment)
	}
}

func TestSegmentCloneSharesNothing(t *testing.T) {
	segment := gotato.Segment{Messages: []gotato.Message{gotato.UserMessage("a")}, ToolSets: []string{"x"}, ToolCalls: []gotato.ToolCallLine{{ToolID: "t"}}}
	clone := segment.Clone()
	clone.Messages[0].Parts[0].Text = "b"
	clone.ToolSets[0], clone.ToolCalls[0].ToolID = "y", "u"
	if gotato.TextOf(segment.Messages[0]) != "a" || segment.ToolSets[0] != "x" || segment.ToolCalls[0].ToolID != "t" {
		t.Fatal("a clone shares memory with its Segment")
	}
}
