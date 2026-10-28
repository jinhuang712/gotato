package gotato

import (
	"reflect"
	"time"
)

// Header is what every Event carries: when it happened and the Session it
// concerns, if any. A stored Event also has its Number, and an Event of a Run
// its Run and its Turn.
type Header struct {
	Time      time.Time `json:"time"`
	SessionID SessionID `json:"session_id,omitempty"`
	// Number is a stored Event's position in the Session's Record. A delta
	// carries the Number of the stored Event it follows. An Instance Event
	// has none.
	Number uint64     `json:"number,omitempty"`
	RunID  RunID      `json:"run_id,omitempty"`
	Turn   TurnNumber `json:"turn,omitempty"`
}

// Head returns the Header; every Event type gets it by embedding Header.
func (h Header) Head() Header { return h }

// NewHeader returns a Header stamped with the current time.
func NewHeader(session SessionID, run RunID, turn TurnNumber) Header {
	return Header{Time: time.Now(), SessionID: session, RunID: run, Turn: turn}
}

// WithNumber returns a copy of event whose Number is number.
func WithNumber(event Event, number uint64) Event {
	return withHeader(event, func(h *Header) { h.Number = number })
}

// WithSession returns a copy of event whose SessionID is id, when it has none.
func WithSession(event Event, id SessionID) Event {
	if event.Head().SessionID != "" || id == "" {
		return event
	}
	return withHeader(event, func(h *Header) { h.SessionID = id })
}

// withHeader copies an Event and edits the copy's Header. Event types are
// plain structs that embed Header, so one reflective copy serves them all.
func withHeader(event Event, edit func(*Header)) Event {
	value := reflect.ValueOf(event)
	if value.Kind() != reflect.Struct {
		return event
	}
	field := value.FieldByName("Header")
	if !field.IsValid() {
		return event
	}
	copied := reflect.New(value.Type()).Elem()
	copied.Set(value)
	header := field.Interface().(Header)
	edit(&header)
	copied.FieldByName("Header").Set(reflect.ValueOf(header))
	return copied.Interface().(Event)
}
