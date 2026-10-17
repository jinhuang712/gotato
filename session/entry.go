package session

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// Layer says what an Entry is for. A fact is part of what the Session is: a
// rebuild reads the facts and nothing else. A log Entry records what
// happened, for people and telemetry, and is never read to rebuild.
type Layer string

const (
	LayerFact Layer = "fact"
	LayerLog  Layer = "log"
)

// EntryKind names what an Entry's Data holds.
type EntryKind string

const (
	// EntryCheckpoint is a fact holding a Checkpoint: the whole state of the
	// Session's facts, standing in for every Entry before it.
	EntryCheckpoint EntryKind = "checkpoint"
	// EntryMessage is a fact holding one committed gotato.Message.
	EntryMessage EntryKind = "message"
	// EntryMetadata is a fact holding one MetadataChange.
	EntryMetadata EntryKind = "metadata"

	// EntryLogSnapshot is a log Entry holding a LogSnapshot, written right
	// after each Checkpoint, so the log can be read from there too.
	EntryLogSnapshot EntryKind = "log_snapshot"
	// EntryRunStart is a log Entry holding a Run as it started.
	EntryRunStart EntryKind = "run_start"
	// EntryRunEnd is a log Entry holding a Run as it ended.
	EntryRunEnd EntryKind = "run_end"
	// EntryEvents is a log Entry holding the Events retained since the
	// previous write.
	EntryEvents EntryKind = "events"
)

// Entry is one line of a Record's log. Seq numbers a Record's Entries from 1,
// with no gaps. A Store keeps an Entry as it is and reads nothing in it but
// Seq, Kind and Layer.
type Entry struct {
	Seq      uint64          `json:"seq"`
	Kind     EntryKind       `json:"kind"`
	Layer    Layer           `json:"layer"`
	At       time.Time       `json:"at"`
	Instance string          `json:"instance,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// Checkpoint is the whole state of a Session's facts at one point of its log.
// A load starts from the last Checkpoint, so nothing before it is read again.
// Every Record's log starts with one, a Compaction appends one, and a Fork
// starts its own log with one of its parent.
type Checkpoint struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id,omitempty"`
	ParentSeq   uint64            `json:"parent_seq,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Messages    []gotato.Message  `json:"messages"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Compactions []Compaction      `json:"compactions,omitempty"`
	EventLimit  int               `json:"event_limit,omitempty"`
}

// LogSnapshot is the state of a Session's log at a Checkpoint: its Runs, its
// usage and its retained Events.
type LogSnapshot struct {
	Runs   []Run          `json:"runs,omitempty"`
	Usage  gotato.Usage   `json:"usage"`
	Events []gotato.Event `json:"events,omitempty"`
}

// MetadataChange sets one metadata key. An empty Value deletes the key.
type MetadataChange struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// pendingEntry is an Entry the store does not hold yet, kept as a value until
// it is written.
type pendingEntry struct {
	kind  EntryKind
	layer Layer
	at    time.Time
	data  any
}

// startLog turns the Record's log on, with a Checkpoint of its current state
// as the first Entry. A Record keeps no pending Entries until then, so one
// that is never stored does not grow.
func (s *Record) startLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logging = true
	s.queueCheckpoint()
}

// queueCheckpoint queues a Checkpoint of the current state and the log
// snapshot that goes with it. The caller holds the lock.
func (s *Record) queueCheckpoint() {
	if !s.logging {
		return
	}
	at := s.now().UTC()
	s.pending = append(s.pending,
		pendingEntry{kind: EntryCheckpoint, layer: LayerFact, at: at, data: s.checkpointLocked()},
		pendingEntry{kind: EntryLogSnapshot, layer: LayerLog, at: at, data: LogSnapshot{
			Runs: slices.Clone(s.runs), Usage: s.usage, Events: cloneEvents(s.retainedEvents()),
		}},
	)
	// The snapshot holds the retained Events, so none is pending any more.
	s.pendingEvents = nil
}

func (s *Record) checkpointLocked() Checkpoint {
	return Checkpoint{
		ID:          s.id,
		ParentID:    s.parentID,
		ParentSeq:   s.parentSeq,
		CreatedAt:   s.createdAt,
		Messages:    cloneMessages(s.messages),
		Metadata:    maps.Clone(s.metadata),
		Compactions: slices.Clone(s.compactions),
		EventLimit:  s.eventLimit,
	}
}

// queue adds one pending Entry once the log is on. The caller holds the lock.
func (s *Record) queue(kind EntryKind, layer Layer, data any) {
	if !s.logging {
		return
	}
	s.pending = append(s.pending, pendingEntry{kind: kind, layer: layer, at: s.now().UTC(), data: data})
}

// takePending returns the pending Entries, numbered from head+1, with the
// Events retained since the previous take in one EntryEvents at the end, and
// clears them. The Record's head moves only when stored confirms the write.
func (s *Record) takePending() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pending
	if len(s.pendingEvents) > 0 {
		pending = append(pending, pendingEntry{kind: EntryEvents, layer: LayerLog, at: s.now().UTC(), data: s.pendingEvents})
	}
	if len(pending) == 0 {
		return nil, nil
	}
	entries := make([]Entry, len(pending))
	for i, p := range pending {
		data, err := json.Marshal(p.data)
		if err != nil {
			return nil, fmt.Errorf("session: encode %s entry: %w", p.kind, err)
		}
		entries[i] = Entry{Seq: s.head + uint64(i) + 1, Kind: p.kind, Layer: p.layer, At: p.at, Instance: s.instance, Data: data}
	}
	s.pending = nil
	s.pendingEvents = nil
	return entries, nil
}

// stored records that the store now holds every Entry up to seq.
func (s *Record) stored(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = max(s.head, seq)
}

// Head returns the Seq of the last Entry the store holds for this Record, or
// 0 when the Record has never been stored.
func (s *Record) Head() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.head
}

// Checkpoint replaces the committed Messages with messages, as a Compaction
// does, records compaction when it is not nil, and appends a Checkpoint to
// the log. Nothing written before is rewritten: the Messages it replaces stay
// in the store, and a load starts after them. It must not run while a Run is
// using the Record.
func (s *Record) Checkpoint(messages []gotato.Message, compaction *Compaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneMessages(messages)
	if compaction != nil {
		s.compactions = append(s.compactions, *compaction)
	}
	s.touch()
	s.queueCheckpoint()
}

// FromEntries rebuilds a Record from its log, starting at a Checkpoint: the
// facts a Store loads, or the whole log. The Entries must be in Seq order.
// Log Entries are applied when present: the facts alone rebuild everything a
// Run needs, and the log adds Runs, usage and Events. An Entry of an unknown
// kind is refused rather than misread.
func FromEntries(entries []Entry) (*Record, error) {
	if len(entries) == 0 || entries[0].Kind != EntryCheckpoint {
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "session: a log must start at a checkpoint")
	}
	s := &Record{eventLimit: DefaultEventLimit, now: time.Now, metadata: map[string]string{}}
	for i, entry := range entries {
		if i > 0 && entry.Seq <= entries[i-1].Seq {
			return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: entry %d follows entry %d", entry.Seq, entries[i-1].Seq))
		}
		if err := s.apply(entry); err != nil {
			return nil, err
		}
		s.updatedAt = entry.At
	}
	if s.id == "" {
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "session: checkpoint has no id")
	}
	s.head = entries[len(entries)-1].Seq
	s.logging = true
	return s, nil
}

func (s *Record) apply(entry Entry) error {
	decode := func(v any) error {
		if err := json.Unmarshal(entry.Data, v); err != nil {
			return gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: entry %d (%s): %v", entry.Seq, entry.Kind, err))
		}
		return nil
	}
	switch entry.Kind {
	case EntryCheckpoint:
		var checkpoint Checkpoint
		if err := decode(&checkpoint); err != nil {
			return err
		}
		s.id = checkpoint.ID
		s.parentID = checkpoint.ParentID
		s.parentSeq = checkpoint.ParentSeq
		s.createdAt = checkpoint.CreatedAt
		s.messages = checkpoint.Messages
		s.metadata = checkpoint.Metadata
		if s.metadata == nil {
			s.metadata = map[string]string{}
		}
		s.compactions = checkpoint.Compactions
		s.eventLimit = checkpoint.EventLimit
		if s.eventLimit == 0 {
			s.eventLimit = DefaultEventLimit
		}
	case EntryMessage:
		var message gotato.Message
		if err := decode(&message); err != nil {
			return err
		}
		s.messages = append(s.messages, message)
	case EntryMetadata:
		var change MetadataChange
		if err := decode(&change); err != nil {
			return err
		}
		if change.Value == "" {
			delete(s.metadata, change.Key)
		} else {
			s.metadata[change.Key] = change.Value
		}
	case EntryLogSnapshot:
		var snapshot LogSnapshot
		if err := decode(&snapshot); err != nil {
			return err
		}
		s.runs = snapshot.Runs
		s.usage = snapshot.Usage
		s.events, s.eventHead = nil, 0
		for _, event := range snapshot.Events {
			s.retain(event)
		}
	case EntryRunStart:
		var run Run
		if err := decode(&run); err != nil {
			return err
		}
		s.runs = append(s.runs, run)
	case EntryRunEnd:
		var run Run
		if err := decode(&run); err != nil {
			return err
		}
		s.endRun(run)
	case EntryEvents:
		var events []gotato.Event
		if err := decode(&events); err != nil {
			return err
		}
		for _, event := range events {
			s.retain(event)
		}
	default:
		return gotato.ErrorOf(gotato.ErrNotSupported, fmt.Sprintf("session: entry %d has unknown kind %q", entry.Seq, entry.Kind))
	}
	return nil
}
