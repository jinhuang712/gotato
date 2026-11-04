package record

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Layer says what an Entry is for. A fact is part of what the Session is: a
// rebuild reads the facts and nothing else, so every Event whose loss would
// change the rebuilt Record is a fact, the parts of a reply that was streaming
// included. A log Entry records what happened, for people and telemetry, and
// is never read to rebuild.
type Layer string

// The two layers of an Entry.
const (
	LayerFact Layer = "fact"
	LayerLog  Layer = "log"
)

// storedLayers is the Layer of every Event kind that is stored, and the only
// place that says it: the Events a rebuild reads are facts, and every other
// stored Event is a log Entry. A kind that is not listed has no Layer, so a new
// stored kind that is forgotten here fails its first write (layerOf) and the
// test that ranges over the stored kinds, and is never quietly a log Entry that
// a load would leave out.
var storedLayers = map[gotato.EventKind]Layer{
	// facts: the Messages and the Context
	gotato.EventKindSessionCreated:      LayerFact,
	gotato.EventKindCompactionEnded:     LayerFact,
	gotato.EventKindSegmentClosed:       LayerFact,
	gotato.EventKindMessageCommitted:    LayerFact,
	gotato.EventKindNoticeCommitted:     LayerFact,
	gotato.EventKindReplyEnded:          LayerFact,
	gotato.EventKindReplyCutOff:         LayerFact,
	gotato.EventKindReplyPartKept:       LayerFact,
	gotato.EventKindToolResultCommitted: LayerFact,
	gotato.EventKindToolStarted:         LayerFact,
	gotato.EventKindMetadataChanged:     LayerFact,
	gotato.EventKindToolLoaded:          LayerFact,
	gotato.EventKindToolUnloaded:        LayerFact,
	// facts: the Runs, the queue and the writer
	gotato.EventKindRunStarted:      LayerFact,
	gotato.EventKindRunEnded:        LayerFact,
	gotato.EventKindMessageQueued:   LayerFact,
	gotato.EventKindMessageDropped:  LayerFact,
	gotato.EventKindNoticeQueued:    LayerFact,
	gotato.EventKindSessionClaimed:  LayerFact,
	gotato.EventKindSessionReleased: LayerFact,
	// log entries
	gotato.EventKindTurnStarted:       LayerLog,
	gotato.EventKindTurnEnded:         LayerLog,
	gotato.EventKindContextBuilt:      LayerLog,
	gotato.EventKindReplyStarted:      LayerLog,
	gotato.EventKindModelRetried:      LayerLog,
	gotato.EventKindToolEnded:         LayerLog,
	gotato.EventKindToolResultSpilled: LayerLog,
	gotato.EventKindMessageRejected:   LayerLog,
	gotato.EventKindAbortRequested:    LayerLog,
	gotato.EventKindCompactionStarted: LayerLog,
	gotato.EventKindCompactionFailed:  LayerLog,
	gotato.EventKindExtensionFailed:   LayerLog,
}

// layerOf returns the Layer of Entries of a kind, and an error for a kind that
// storedLayers does not list: a Layer is never guessed.
func layerOf(kind gotato.EventKind) (Layer, error) {
	layer, ok := storedLayers[kind]
	if !ok {
		return "", gotato.ErrorOf(gotato.CodeInternalInvariant, fmt.Sprintf("record: the Event kind %q has no Layer; list it in storedLayers", kind))
	}
	return layer, nil
}

// Entry is one line of a Record's log: one stored Event. Seq numbers a
// Record's Entries from 1, with no gaps, and is the Event's number. A Store
// keeps an Entry as it is and reads nothing in it but Seq, Kind and Layer, and
// IsCheckpoint to find where a load starts.
type Entry struct {
	Seq      uint64           `json:"seq"`
	Kind     gotato.EventKind `json:"kind"`
	Layer    Layer            `json:"layer"`
	At       time.Time        `json:"at"`
	Instance string           `json:"instance,omitempty"`
	Data     json.RawMessage  `json:"data,omitempty"`
}

// IsCheckpoint reports whether the Entry holds the whole state of the
// Session's facts at one point, so that it stands in for every Entry before
// it: session_created, which starts every Record, and compaction_ended. A load
// starts at the last one.
func (e Entry) IsCheckpoint() bool {
	switch e.Kind {
	case gotato.EventKindSessionCreated, gotato.EventKindCompactionEnded:
		return true
	}
	return false
}

// entryOf encodes a numbered stored Event as the Entry at its number.
func entryOf(event gotato.Event, instance string) (Entry, error) {
	data, err := gotato.MarshalEvent(event)
	if err != nil {
		return Entry{}, fmt.Errorf("record: encode %s entry: %w", event.Kind(), err)
	}
	layer, err := layerOf(event.Kind())
	if err != nil {
		return Entry{}, err
	}
	head := event.Head()
	return Entry{Seq: head.Number, Kind: event.Kind(), Layer: layer, At: head.Time, Instance: instance, Data: data}, nil
}

// Event decodes the Entry back into the Event it holds, with the Entry's
// position as its number and sessionID as its Session.
func (e Entry) Event(sessionID gotato.SessionID) (gotato.Event, error) {
	event, err := gotato.UnmarshalEvent(e.Data)
	if err != nil {
		return nil, fmt.Errorf("record: entry %d (%s): %w", e.Seq, e.Kind, err)
	}
	event = gotato.WithNumber(event, e.Seq)
	return gotato.WithSession(event, sessionID), nil
}
