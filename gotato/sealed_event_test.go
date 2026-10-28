package gotato_test

import (
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

// appEvent is an Event defined outside the core: embedding Header is enough.
type appEvent struct{ gotato.Header }

func (appEvent) Kind() gotato.EventKind { return "app_event" }

func TestAnEventTypeOutsideTheCoreEmbedsHeader(t *testing.T) {
	var event gotato.Event = appEvent{}
	if event.Kind() != "app_event" {
		t.Fatal("kind")
	}
}
