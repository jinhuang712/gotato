package gotato

import (
	"encoding/json"
	"testing"
)

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
