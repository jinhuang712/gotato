package session

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// SchemaVersion is the persisted document version written by stores.
const SchemaVersion = 1

// Document is the serializable form of a Record. Snapshot and FromDocument
// convert to and from a Record.
type Document struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	ParentID      string `json:"parent_id,omitempty"`
	// RecordEpoch is the Record's identity as a log (Record.RecordEpoch).
	RecordEpoch string              `json:"record_epoch,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Messages    []gotato.Message    `json:"messages"`
	Runs        []RunInfo           `json:"runs,omitempty"`
	Events      []gotato.Event      `json:"events,omitempty"`
	Usage       gotato.Usage        `json:"usage"`
	Compactions []gotato.Compaction `json:"compactions,omitempty"`
	Metadata    map[string]string   `json:"metadata,omitempty"`
	// LoadedTools are the Tools the model loaded (Record.LoadedTools).
	LoadedTools []string `json:"loaded_tools,omitempty"`
	// Segments are the Segments the Messages start with (Record.Segments);
	// their rendered Messages are the first of Messages.
	Segments []gotato.Segment `json:"segments,omitempty"`
	// Seq is the position of the last Entry the store held when this
	// Document was read; 0 for a Record never stored.
	Seq uint64 `json:"seq,omitempty"`
}

// UnmarshalJSON reads the Events of a document by their kind, and drops the
// ones it cannot read, such as those of an earlier version.
func (d *Document) UnmarshalJSON(data []byte) error {
	type plain Document
	aux := struct {
		*plain
		Events []json.RawMessage `json:"events,omitempty"`
	}{plain: (*plain)(d)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	d.Events = nil
	for _, raw := range aux.Events {
		event, err := gotato.UnmarshalEvent(raw)
		if err != nil {
			continue
		}
		if _, unknown := event.(gotato.UnknownEvent); unknown {
			continue
		}
		d.Events = append(d.Events, event)
	}
	return nil
}

// Snapshot returns a deep-copied Document of the current state.
func (s *Record) Snapshot() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            s.id,
		ParentID:      s.parentID,
		RecordEpoch:   s.epoch,
		CreatedAt:     s.createdAt,
		UpdatedAt:     s.updatedAt,
		Messages:      cloneMessages(s.messages),
		Runs:          slices.Clone(s.runs),
		Events:        slices.Clone(s.retainedEvents()),
		Usage:         s.usage,
		Compactions:   slices.Clone(s.compactions),
		Metadata:      maps.Clone(s.metadata),
		LoadedTools:   slices.Clone(s.loaded),
		Segments:      s.segmentsLocked(),
		Seq:           s.head,
	}
}

// FromDocument rebuilds a Record from a persisted Document.
func FromDocument(doc Document) (*Record, error) {
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, gotato.ErrorOf(gotato.CodeNotSupported, "session: unsupported schema_version")
	}
	if doc.ID == "" {
		return nil, gotato.ErrorOf(gotato.CodeInvalidArgument, "session: document has no id")
	}
	s := &Record{
		id:          doc.ID,
		epoch:       doc.RecordEpoch,
		parentID:    doc.ParentID,
		createdAt:   doc.CreatedAt,
		updatedAt:   doc.UpdatedAt,
		messages:    cloneMessages(doc.Messages),
		runs:        slices.Clone(doc.Runs),
		usage:       doc.Usage,
		compactions: slices.Clone(doc.Compactions),
		metadata:    maps.Clone(doc.Metadata),
		loaded:      slices.Clone(doc.LoadedTools),
		eventLimit:  defaultEventLimit,
		closed:      map[int]gotato.Segment{},
		positions:   map[gotato.MessageID]gotato.MessagePosition{},
		now:         time.Now,
		retainRead:  true,
	}
	for _, run := range s.runs {
		if run.EndedAt.After(s.lastRunEnd) {
			s.lastRunEnd = run.EndedAt
		}
	}
	for _, segment := range doc.Segments {
		s.closed[segment.Number] = segment.Clone()
		s.used = append(s.used, segment.Number)
		s.maxSegment = max(s.maxSegment, segment.Number)
	}
	for _, event := range doc.Events {
		s.retain(event)
	}
	if s.metadata == nil {
		s.metadata = map[string]string{}
	}
	if s.createdAt.IsZero() {
		s.createdAt = s.now().UTC()
	}
	return s, nil
}

func cloneMessages(messages []gotato.Message) []gotato.Message {
	out := make([]gotato.Message, len(messages))
	for i, message := range messages {
		out[i] = message.Clone()
	}
	return out
}
