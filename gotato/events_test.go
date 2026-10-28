package gotato

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// An Event is one flat JSON object whose kind names its type, and it comes back
// as the type it was.
func TestEventJSONIsFlatAndRoundTrips(t *testing.T) {
	ended := RunEnded{
		Header: Header{Time: time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC), SessionID: "s1", Number: 7, RunID: "r1", Turn: 2},
		Status: RunStatusFailed, Cause: CodeModelFailure, Turns: 2, Usage: Usage{InputTokens: 5},
	}
	data, err := MarshalEvent(ended)
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]any
	if err := json.Unmarshal(data, &flat); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind", "time", "session_id", "number", "run_id", "turn", "status", "cause"} {
		if _, ok := flat[key]; !ok {
			t.Errorf("RunEnded JSON has no %q: %s", key, data)
		}
	}
	if flat["kind"] != "run_ended" {
		t.Fatalf("kind = %v", flat["kind"])
	}
	back, err := UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, Event(ended)) {
		t.Fatalf("round trip = %#v, want %#v", back, ended)
	}
}

// Every kind round-trips through JSON as its own type, and names itself.
func TestEveryKindDecodesToItsType(t *testing.T) {
	if len(eventDecoders) != len(eventClasses) {
		t.Fatalf("%d decoders for %d classes", len(eventDecoders), len(eventClasses))
	}
	for kind, decode := range eventDecoders {
		event, err := decode([]byte(`{"time":"2026-10-18T00:00:00Z"}`))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if event.Kind() != kind {
			t.Errorf("a %s decodes to a type that says %s", kind, event.Kind())
		}
		data, err := json.Marshal(event)
		if err != nil || !strings.Contains(string(data), `"kind":"`+string(kind)+`"`) {
			t.Errorf("%s marshals as %s (%v)", kind, data, err)
		}
		if kind.Class() == "" {
			t.Errorf("%s has no class", kind)
		}
	}
}

// A kind this version does not know is kept, not refused.
func TestUnknownKindIsKept(t *testing.T) {
	data := []byte(`{"kind":"from_the_future","time":"2026-10-18T00:00:00Z","session_id":"s","number":3,"extra":1}`)
	event, err := UnmarshalEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	unknown, ok := event.(UnknownEvent)
	if !ok || unknown.Kind() != "from_the_future" || unknown.Head().Number != 3 {
		t.Fatalf("event = %#v", event)
	}
	again, _ := json.Marshal(unknown)
	if string(again) != string(data) {
		t.Fatalf("an unknown Event is passed on as it was: %s", again)
	}
}

// A reasoning delta from the model is sent as a ReasoningDelta, and never
// stored.
func TestReasoningDeltaIsSentNotStored(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{{
		{Kind: ModelEventKindReasoningDelta, Text: "think"},
		{Kind: ModelEventKindReasoningDelta, Text: " hard"},
		{Kind: ModelEventKindTextDelta, Text: "answer"},
		{Kind: ModelEventKindDone, StopReason: StopReasonEndTurn},
	}}}
	agent, err := NewSession(context.Background(), WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	seen := recordAll(t, agent)
	if _, err := agent.Prompt(context.Background(), UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, event := range seen.events() {
		if e, ok := event.(ReasoningDelta); ok {
			text = append(text, e.Text)
			if e.Kind().Class() != EventClassDelta || e.MessageID == "" {
				t.Fatalf("ReasoningDelta = %+v", e)
			}
		}
	}
	if len(text) != 2 || text[0] != "think" || text[1] != " hard" {
		t.Fatalf("ReasoningDelta texts = %q", text)
	}
}
