package service_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
)

// The nine Instance Events moved from the core to the service with their kinds,
// their fields and their JSON unchanged: MarshalEvent writes each as one flat
// object whose kind comes first, and the core reads it back as an UnknownEvent
// that writes the same bytes.
func TestInstanceEventsKeepTheirKindsAndJSON(t *testing.T) {
	h := gotato.NewHeader("s-1", "", 0)
	cases := []struct {
		event gotato.Event
		kind  string
		keys  []string
	}{
		{service.RunRequested{Header: h, Agent: "echo"}, "run_requested", []string{"agent"}},
		{service.RunWaiting{Header: h, Why: "busy"}, "run_waiting", []string{"why"}},
		{service.RunRejected{Header: h, Why: "at_capacity"}, "run_rejected", []string{"why"}},
		{service.SessionFenced{Header: h, Instance: "i-1", Position: 4}, "session_fenced", []string{"instance", "position"}},
		{service.LivenessLapsed{Header: h, Instance: "i-1"}, "liveness_lapsed", []string{"instance"}},
		{service.SessionDeleted{Header: h}, "session_deleted", nil},
		{service.SessionLoaded{Header: h, Messages: 3, ElapsedMS: 1.5, Failed: true}, "session_loaded", []string{"messages", "elapsed_ms", "failed"}},
		{service.SessionSaved{Header: h, Messages: 3, ElapsedMS: 2.5, Writes: 2}, "session_saved", []string{"messages", "elapsed_ms", "writes"}},
		{service.StoreWriteFailed{Header: h, Code: gotato.CodeStoreFailure}, "store_write_failed", []string{"code"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if string(tc.event.Kind()) != tc.kind || tc.event.Class() != gotato.EventClassInstance {
				t.Fatalf("kind %q class %q, want %q and an Instance Event", tc.event.Kind(), tc.event.Class(), tc.kind)
			}
			data, err := gotato.MarshalEvent(tc.event)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), `{"kind":"`+tc.kind+`",`) {
				t.Fatalf("the kind is not the first key: %s", data)
			}
			var flat map[string]any
			if err := json.Unmarshal(data, &flat); err != nil {
				t.Fatal(err)
			}
			for _, key := range append([]string{"kind", "time", "session_id"}, tc.keys...) {
				if _, ok := flat[key]; !ok {
					t.Errorf("%s has no %q: %s", tc.kind, key, data)
				}
			}
			back, err := gotato.UnmarshalEvent(data)
			if err != nil {
				t.Fatal(err)
			}
			unknown, ok := back.(gotato.UnknownEvent)
			if !ok || unknown.Kind() != tc.event.Kind() || unknown.Head().SessionID != "s-1" {
				t.Fatalf("the core read %#v, want an UnknownEvent of kind %s", back, tc.kind)
			}
			again, err := gotato.MarshalEvent(back)
			if err != nil || string(again) != string(data) {
				t.Fatalf("round trip = %s (%v), want %s", again, err, data)
			}
		})
	}
}
