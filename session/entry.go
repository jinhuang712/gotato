package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Layer says what an Entry is for. A fact is part of what the Session is: a
// rebuild reads the facts and nothing else. A log Entry records what
// happened, for people and telemetry, and is not read to rebuild, with one
// exception: while a Run is in flight, a load also returns the log since the
// last Checkpoint (Header.LogFrom), for the reply parts of that Run
// (reply_part_kept), to rebuild the Cut-off reply it had reached if the Run
// never ends.
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
	// facts
	gotato.EventKindSessionCreated:      LayerFact,
	gotato.EventKindCompactionEnded:     LayerFact,
	gotato.EventKindMessageCommitted:    LayerFact,
	gotato.EventKindNoticeCommitted:     LayerFact,
	gotato.EventKindReplyEnded:          LayerFact,
	gotato.EventKindReplyCutOff:         LayerFact,
	gotato.EventKindToolResultCommitted: LayerFact,
	gotato.EventKindToolStarted:         LayerFact,
	gotato.EventKindMetadataChanged:     LayerFact,
	gotato.EventKindToolLoaded:          LayerFact,
	gotato.EventKindToolUnloaded:        LayerFact,
	gotato.EventKindSegmentClosed:       LayerFact,
	gotato.EventKindSessionClaimed:      LayerFact,
	// log entries
	gotato.EventKindRunStarted:        LayerLog,
	gotato.EventKindRunEnded:          LayerLog,
	gotato.EventKindTurnStarted:       LayerLog,
	gotato.EventKindTurnEnded:         LayerLog,
	gotato.EventKindContextBuilt:      LayerLog,
	gotato.EventKindReplyStarted:      LayerLog,
	gotato.EventKindReplyPartKept:     LayerLog,
	gotato.EventKindModelRetried:      LayerLog,
	gotato.EventKindToolEnded:         LayerLog,
	gotato.EventKindToolResultSpilled: LayerLog,
	gotato.EventKindMessageQueued:     LayerLog,
	gotato.EventKindMessageDropped:    LayerLog,
	gotato.EventKindNoticeQueued:      LayerLog,
	gotato.EventKindMessageRejected:   LayerLog,
	gotato.EventKindAbortRequested:    LayerLog,
	gotato.EventKindSessionReleased:   LayerLog,
	gotato.EventKindCompactionStarted: LayerLog,
	gotato.EventKindCompactionFailed:  LayerLog,
	gotato.EventKindExtensionFailed:   LayerLog,
}

// layerOf returns the Layer of Entries of a kind, and an error for a kind that
// storedLayers does not list: a Layer is never guessed.
func layerOf(kind gotato.EventKind) (Layer, error) {
	layer, ok := storedLayers[kind]
	if !ok {
		return "", gotato.ErrorOf(gotato.CodeInternalInvariant, fmt.Sprintf("session: the Event kind %q has no Layer; list it in storedLayers", kind))
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
		return Entry{}, fmt.Errorf("session: encode %s entry: %w", event.Kind(), err)
	}
	layer, err := layerOf(event.Kind())
	if err != nil {
		return Entry{}, err
	}
	head := event.Head()
	return Entry{Seq: head.Number, Kind: event.Kind(), Layer: layer, At: head.Time, Instance: instance, Data: data}, nil
}

// eventOf decodes an Entry back into the Event it holds, with the Entry's
// position as its number and sessionID as its Session. ok is true whenever err
// is nil.
func eventOf(entry Entry, sessionID gotato.SessionID) (event gotato.Event, ok bool, err error) {
	event, err = gotato.UnmarshalEvent(entry.Data)
	if err != nil {
		return nil, false, fmt.Errorf("session: entry %d (%s): %w", entry.Seq, entry.Kind, err)
	}
	event = gotato.WithNumber(event, entry.Seq)
	return gotato.WithSession(event, sessionID), true, nil
}
