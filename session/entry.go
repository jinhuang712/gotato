package session

import (
	"encoding/json"
	"fmt"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// Layer says what an Entry is for. A fact is part of what the Session is: a
// rebuild reads the facts and nothing else. A log Entry records what
// happened, for people and telemetry, and is not read to rebuild, with one
// exception: when a Run never ended, a load also reads the log since the last
// Checkpoint for the reply parts of that Run (reply_part_kept), to rebuild the
// Cut-off reply it had reached.
type Layer string

const (
	LayerFact Layer = "fact"
	LayerLog  Layer = "log"
)

// EntryKind names what an Entry holds. Every Entry is a stored Event, so its
// kind is the Event's kind, such as "message_committed". A Record written
// before the Event design also holds Entries of the kinds it used then, which
// are still read (see legacy.go).
type EntryKind = gotato.EventKind

// factKinds are the Events a rebuild reads.
var factKinds = map[gotato.EventKind]bool{
	gotato.KindSessionCreated:      true,
	gotato.KindCompactionEnded:     true,
	gotato.KindMessageCommitted:    true,
	gotato.KindReplyEnded:          true,
	gotato.KindReplyCutOff:         true,
	gotato.KindToolResultCommitted: true,
	gotato.KindToolStarted:         true,
	gotato.KindMetadataChanged:     true,
	gotato.KindSessionClaimed:      true,
}

// LayerOf returns the Layer of Entries of a kind: the Events a rebuild reads
// are facts, and every other Event is a log Entry.
func LayerOf(kind EntryKind) Layer {
	if factKinds[kind] || legacyFacts[kind] {
		return LayerFact
	}
	return LayerLog
}

// Entry is one line of a Record's log: one stored Event. Seq numbers a
// Record's Entries from 1, with no gaps, and is the Event's number. A Store
// keeps an Entry as it is and reads nothing in it but Seq, Kind and Layer, and
// IsCheckpoint to find where a load starts.
type Entry struct {
	Seq      uint64          `json:"seq"`
	Kind     EntryKind       `json:"kind"`
	Layer    Layer           `json:"layer"`
	At       time.Time       `json:"at"`
	Instance string          `json:"instance,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// IsCheckpoint reports whether the Entry holds the whole state of the
// Session's facts at one point, so that it stands in for every Entry before
// it: session_created, which starts every Record, and compaction_ended. A load
// starts at the last one.
func (e Entry) IsCheckpoint() bool {
	switch e.Kind {
	case gotato.KindSessionCreated, gotato.KindCompactionEnded, legacyKindCheckpoint:
		return true
	}
	return false
}

// Claim names the Instance that writes a Record, and its Epoch, which grows
// by one with every new Claim. A Claim is itself an Entry, at the position
// after the head its Instance read. So a writer that lost its Claim always
// writes to a position that is already taken, and the store refuses it in
// the same write.
type Claim struct {
	Instance string `json:"instance"`
	Epoch    uint64 `json:"epoch"`
}

// Compaction records one replacement of a Message prefix by a summary.
type Compaction = gotato.Compaction

// entryOf encodes a numbered stored Event as the Entry at its number.
func entryOf(event gotato.Event, instance string) (Entry, error) {
	data, err := gotato.MarshalEvent(event)
	if err != nil {
		return Entry{}, fmt.Errorf("session: encode %s entry: %w", event.Kind(), err)
	}
	head := event.Head()
	return Entry{Seq: head.Number, Kind: event.Kind(), Layer: LayerOf(event.Kind()), At: head.Time, Instance: instance, Data: data}, nil
}

// EventOf decodes an Entry back into the Event it holds, with the Entry's
// position as its number and sessionID as its Session. Entries that the Event
// design replaced are mapped onto their Events where one fits; ok is false for
// an Entry that has no Event, such as a legacy log snapshot.
func EventOf(entry Entry, sessionID gotato.SessionID) (event gotato.Event, ok bool, err error) {
	if isLegacy(entry.Kind) {
		return legacyEventOf(entry, sessionID)
	}
	event, err = gotato.UnmarshalEvent(entry.Data)
	if err != nil {
		return nil, false, fmt.Errorf("session: entry %d (%s): %w", entry.Seq, entry.Kind, err)
	}
	event = gotato.WithNumber(event, entry.Seq)
	return gotato.WithSession(event, sessionID), true, nil
}
