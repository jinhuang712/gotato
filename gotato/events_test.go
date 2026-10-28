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

// TestEventFieldsAreWrittenOneWay pins the JSON keys that name the same thing
// the same way on every Event: a Tool is tool_id, loaded Tools are
// loaded_tools, a duration is a number with its fraction, and a notice's label
// does not take the key the Event's kind has.
func TestEventFieldsAreWrittenOneWay(t *testing.T) {
	events := []struct {
		event Event
		want  map[string]any
		gone  []string
	}{
		{ToolLoaded{ToolID: "crm.get"}, map[string]any{"kind": "tool_loaded", "tool_id": "crm.get"}, []string{"tool"}},
		{ToolUnloaded{ToolID: "crm.get"}, map[string]any{"kind": "tool_unloaded", "tool_id": "crm.get"}, []string{"tool"}},
		{NoticeQueued{NoticeKind: "order_update"}, map[string]any{"kind": "notice_queued", "notice_kind": "order_update"}, []string{"label"}},
		{ToolEnded{}, map[string]any{"tool_id": ""}, nil},
		{ToolResultSpilled{}, map[string]any{"tool_id": ""}, nil},
		{TurnEnded{ElapsedMS: 1.25}, map[string]any{"elapsed_ms": 1.25}, nil},
		{RunEnded{ElapsedMS: 2.5}, map[string]any{"elapsed_ms": 2.5}, nil},
		{ModelRetried{WaitMS: 0.5}, map[string]any{"wait_ms": 0.5}, nil},
		{SessionCreated{LoadedTools: []string{"a.b"}}, map[string]any{"loaded_tools": []any{"a.b"}}, []string{"tools"}},
		{CompactionEnded{LoadedTools: []string{"a.b"}}, map[string]any{"loaded_tools": []any{"a.b"}}, []string{"tools"}},
	}
	for _, c := range events {
		data, err := json.Marshal(c.event)
		if err != nil {
			t.Fatalf("%T: %v", c.event, err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%T: %v", c.event, err)
		}
		for key, want := range c.want {
			if g, ok := got[key]; !ok || !jsonEqual(g, want) {
				t.Errorf("%T: %q = %v (present %v), want %v in %s", c.event, key, g, ok, want, data)
			}
		}
		for _, key := range c.gone {
			if _, ok := got[key]; ok {
				t.Errorf("%T: old key %q still written in %s", c.event, key, data)
			}
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
