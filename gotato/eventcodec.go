package gotato

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MarshalEvent encodes any Event as one flat JSON object whose "kind" names its
// type. An Event type of Gotato writes its own kind; a type of another package,
// which embeds Header and states its Kind and Class, needs no MarshalJSON: the
// kind is written first, followed by its fields. UnmarshalEvent reads the
// kinds of this package back as their types, and any other as an UnknownEvent,
// which MarshalEvent writes as it was read, so the JSON of an Event of another
// package round-trips unchanged.
func MarshalEvent(event Event) ([]byte, error) {
	if event == nil {
		return nil, fmt.Errorf("gotato: encode Event: nil Event")
	}
	if _, writesItsKind := event.(json.Marshaler); writesItsKind {
		return json.Marshal(event)
	}
	return marshalKind(event.Kind(), event)
}

// UnmarshalEvent decodes the JSON that MarshalEvent wrote. A kind this package
// does not know, whether a later version adds it or another package defines it
// (the host package's Instance Events, an Event of a host's own), comes back as an
// UnknownEvent.
func UnmarshalEvent(data []byte) (Event, error) {
	var probe struct {
		Kind EventKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("gotato: decode Event: %w", err)
	}
	decode, ok := eventDecoders[probe.Kind]
	if !ok {
		unknown := UnknownEvent{EventKind: probe.Kind, Raw: append(json.RawMessage(nil), data...)}
		_ = json.Unmarshal(data, &unknown.Header)
		return unknown, nil
	}
	return decode(data)
}

func decodeAs[E Event](data []byte) (Event, error) {
	var event E
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("gotato: decode Event: %w", err)
	}
	return event, nil
}

// marshalKind encodes v, an object, with "kind" as its first key.
func marshalKind(kind EventKind, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(`{"kind":`)
	name, _ := json.Marshal(kind)
	out.Write(name)
	if len(body) > 2 {
		out.WriteByte(',')
		out.Write(body[1:])
	} else {
		out.WriteByte('}')
	}
	return out.Bytes(), nil
}

// UnknownEvent is an Event whose kind this version does not know, kept as its
// JSON so a consumer can pass it on.
type UnknownEvent struct {
	Header
	EventKind EventKind
	Raw       json.RawMessage
}

// Kind implements Event.
func (e UnknownEvent) Kind() EventKind { return e.EventKind }

// Class implements Event. An Event of an unknown kind has no class that can be
// read from its JSON, so it is the empty class: not stored, not a delta, and
// not an Instance Event.
func (UnknownEvent) Class() EventClass { return "" }

// MarshalJSON writes the Event as it was read.
func (e UnknownEvent) MarshalJSON() ([]byte, error) {
	if len(e.Raw) == 0 {
		return marshalKind(e.EventKind, struct{}{})
	}
	return e.Raw, nil
}
