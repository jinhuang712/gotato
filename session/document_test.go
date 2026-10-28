package session_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func TestLoadRejectsFutureSchema(t *testing.T) {
	_, err := session.FromDocument(session.Document{ID: "x", SchemaVersion: session.SchemaVersion + 1})
	if !gotato.IsCode(err, gotato.CodeNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadNormalizesAnOverfullDocument(t *testing.T) {
	var events []gotato.Event
	for i := 1; i <= session.DefaultEventLimit+2; i++ {
		events = append(events, gotato.TurnStarted{Header: gotato.Header{Number: uint64(i), Turn: gotato.TurnNumber(i)}})
	}
	doc := session.Document{ID: "x", SchemaVersion: session.SchemaVersion, Events: events}
	s, err := session.FromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(context.Background(), []gotato.Event{gotato.TurnEnded{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	got := s.Events()
	if len(got) != session.DefaultEventLimit || got[0].Head().Turn != 4 || got[len(got)-1].Kind() != gotato.EventKindTurnEnded {
		t.Fatalf("events = %+v", got)
	}
}

// A Document holds typed Events and reads them back by their kind; an Event of
// a kind this version does not know, such as one a later version adds, is
// dropped, not an error.
func TestDocumentRoundTripsTypedEvents(t *testing.T) {
	s := session.New()
	if _, err := s.Write(context.Background(), []gotato.Event{gotato.RunStarted{Header: gotato.NewHeader("", "run-1", 0)}, gotato.TurnStarted{Header: header()}}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var doc session.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(doc.Events); len(kinds) != 2 || kinds[0] != gotato.EventKindRunStarted || kinds[1] != gotato.EventKindTurnStarted {
		t.Fatalf("document events = %v", kinds)
	}
	withFuture := []byte(`{"id":"x","schema_version":1,"events":[{"kind":"from_the_future"},{"kind":"turn_started","time":"2026-10-17T12:00:00Z"}]}`)
	var future session.Document
	if err := json.Unmarshal(withFuture, &future); err != nil {
		t.Fatal(err)
	}
	if kinds := kindsOf(future.Events); len(kinds) != 1 || kinds[0] != gotato.EventKindTurnStarted {
		t.Fatalf("future document events = %v", kinds)
	}
}
