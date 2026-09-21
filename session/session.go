// Package session is the Gotato Session primitive: the record of what
// happened in an interaction across Turns and Runs.
//
// A Session holds identity, the committed Messages, per-Run records, usage,
// a bounded window of runtime Events, compaction history, and free-form
// application metadata. It implements gotato.Transcript, so an Agent commits
// to it directly:
//
//	s := session.New()
//	agent, _ := gotato.NewAgent(gotato.WithModel(model), gotato.WithTranscript(s), gotato.WithExtension(session.Record(s)))
//	agent.Prompt(ctx, gotato.UserMessage("hi"))
//	store.Save(ctx, s)
//
// A Session knows nothing about tasks, projects, roles, or agent hierarchy.
// Application semantics belong in Metadata.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"slices"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// SchemaVersion is the persisted document version written by stores.
const SchemaVersion = 1

// DefaultEventLimit bounds the Events retained in a Session. Older Events are
// dropped first; Runs, Messages, and Usage are never dropped.
const DefaultEventLimit = 1000

// Run is the per-Run record kept in a Session.
type Run struct {
	RunID      gotato.RunID     `json:"run_id"`
	AgentID    gotato.AgentID   `json:"agent_id,omitempty"`
	Status     gotato.RunStatus `json:"status"`
	Error      string           `json:"error,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	EndedAt    time.Time        `json:"ended_at,omitempty"`
	Turns      uint32           `json:"turns"`
	ToolCalls  uint32           `json:"tool_calls"`
	Usage      gotato.Usage     `json:"usage"`
	StopReason string           `json:"stop_reason,omitempty"`
}

// Compaction records one replacement of a Message prefix by a summary, so an
// application can always tell what was compacted and what replaced it.
type Compaction struct {
	At               time.Time        `json:"at"`
	ReplacedMessages int              `json:"replaced_messages"`
	FromMessageID    gotato.MessageID `json:"from_message_id,omitempty"`
	ToMessageID      gotato.MessageID `json:"to_message_id,omitempty"`
	SummaryMessageID gotato.MessageID `json:"summary_message_id"`
	Summarizer       string           `json:"summarizer"`
	BytesBefore      int              `json:"bytes_before"`
	BytesAfter       int              `json:"bytes_after"`
}

// Document is the serializable form of a Session. Stores persist it; Snapshot
// and Load convert to and from a live Session.
type Document struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	ParentID      string            `json:"parent_id,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Messages      []gotato.Message  `json:"messages"`
	Runs          []Run             `json:"runs,omitempty"`
	Events        []gotato.Event    `json:"events,omitempty"`
	Usage         gotato.Usage      `json:"usage"`
	Compactions   []Compaction      `json:"compactions,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	EventLimit    int               `json:"event_limit,omitempty"`
}

// Session is what happened. It is safe for concurrent use; the Agent that
// runs against it must still be the only writer of Messages during a Run.
type Session struct {
	mu        sync.RWMutex
	id        string
	parentID  string
	createdAt time.Time
	updatedAt time.Time
	messages  []gotato.Message
	runs      []Run
	events    []gotato.Event
	// eventHead is the ring-buffer position of the oldest retained Event once
	// events is full. It is always 0 until the window reaches eventLimit.
	eventHead   int
	usage       gotato.Usage
	compactions []Compaction
	metadata    map[string]string
	eventLimit  int
	now         func() time.Time
}

// Option configures New.
type Option func(*Session)

// WithID sets the Session ID. Default: a random 16-byte hex ID.
func WithID(id string) Option { return func(s *Session) { s.id = id } }

// WithEventLimit bounds retained Events. Zero keeps DefaultEventLimit; a
// negative value disables Event retention.
func WithEventLimit(limit int) Option {
	return func(s *Session) {
		if limit == 0 {
			limit = DefaultEventLimit
		}
		s.eventLimit = limit
	}
}

// WithMetadata sets initial application metadata.
func WithMetadata(metadata map[string]string) Option {
	return func(s *Session) { s.metadata = maps.Clone(metadata) }
}

// WithClock overrides the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Session) { s.now = now } }

// New creates an empty Session.
func New(options ...Option) *Session {
	s := &Session{eventLimit: DefaultEventLimit, now: time.Now}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	if s.id == "" {
		s.id = NewID()
	}
	if s.metadata == nil {
		s.metadata = map[string]string{}
	}
	s.createdAt = s.now().UTC()
	s.updatedAt = s.createdAt
	return s
}

// NewID returns a random, restart-safe identifier.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

// Load rebuilds a live Session from a persisted Document.
func Load(doc Document) (*Session, error) {
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, gotato.ErrorOf(gotato.ErrNotSupported, "session: unsupported schema_version")
	}
	if doc.ID == "" {
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "session: document has no id")
	}
	s := &Session{
		id:          doc.ID,
		parentID:    doc.ParentID,
		createdAt:   doc.CreatedAt,
		updatedAt:   doc.UpdatedAt,
		messages:    cloneMessages(doc.Messages),
		runs:        slices.Clone(doc.Runs),
		events:      cloneEvents(doc.Events),
		usage:       doc.Usage,
		compactions: slices.Clone(doc.Compactions),
		metadata:    maps.Clone(doc.Metadata),
		eventLimit:  doc.EventLimit,
		now:         time.Now,
	}
	if s.eventLimit == 0 {
		s.eventLimit = DefaultEventLimit
	}
	if s.metadata == nil {
		s.metadata = map[string]string{}
	}
	if s.createdAt.IsZero() {
		s.createdAt = s.now().UTC()
	}
	return s, nil
}

// Snapshot returns a deep-copied Document of the current state.
func (s *Session) Snapshot() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            s.id,
		ParentID:      s.parentID,
		CreatedAt:     s.createdAt,
		UpdatedAt:     s.updatedAt,
		Messages:      cloneMessages(s.messages),
		Runs:          slices.Clone(s.runs),
		Events:        cloneEvents(s.retainedEvents()),
		Usage:         s.usage,
		Compactions:   slices.Clone(s.compactions),
		Metadata:      maps.Clone(s.metadata),
		EventLimit:    s.eventLimit,
	}
}

// ID returns the Session identity.
func (s *Session) ID() string { return s.id }

// ParentID returns the Session this one was forked from, or "".
func (s *Session) ParentID() string { return s.parentID }

// CreatedAt returns the creation time.
func (s *Session) CreatedAt() time.Time { return s.createdAt }

// UpdatedAt returns the time of the last mutation.
func (s *Session) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// Messages implements gotato.Transcript. The returned slice is a copy.
func (s *Session) Messages() []gotato.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.messages)
}

// Len returns the number of committed Messages without copying them.
func (s *Session) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// Append implements gotato.Transcript.
func (s *Session) Append(message gotato.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message.Clone())
	s.touch()
	return nil
}

// ReplaceMessages swaps the whole committed history. It exists for state
// operations such as compaction and must not run while an Agent Run is using
// this Session.
func (s *Session) ReplaceMessages(messages []gotato.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneMessages(messages)
	s.touch()
}

// Runs returns the Run records.
func (s *Session) Runs() []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.runs)
}

// RecordRunStart adds a running Run record.
func (s *Session) RecordRunStart(runID gotato.RunID, agentID gotato.AgentID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, Run{RunID: runID, AgentID: agentID, Status: gotato.RunRunning, StartedAt: at.UTC()})
	s.touch()
}

// RecordRunEnd finalizes the matching Run record and aggregates usage.
func (s *Session) RecordRunEnd(runID gotato.RunID, status gotato.RunStatus, errText, stopReason string, turns, toolCalls uint32, usage gotato.Usage, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.runs) - 1; i >= 0; i-- {
		if s.runs[i].RunID != runID {
			continue
		}
		s.runs[i].Status = status
		s.runs[i].Error = errText
		s.runs[i].StopReason = stopReason
		s.runs[i].EndedAt = at.UTC()
		s.runs[i].Turns = turns
		s.runs[i].ToolCalls = toolCalls
		s.runs[i].Usage = usage
		break
	}
	s.usage.InputTokens += usage.InputTokens
	s.usage.OutputTokens += usage.OutputTokens
	s.usage.TotalTokens += usage.TotalTokens
	s.touch()
}

// Usage returns the aggregated usage across recorded Runs.
func (s *Session) Usage() gotato.Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usage
}

// Events returns the retained Events in order, with copied payloads.
func (s *Session) Events() []gotato.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneEvents(s.retainedEvents())
}

// RecordEvent retains one Event, dropping the oldest beyond the limit. Once the
// window is full the backing slice is reused as a ring buffer so a streaming
// producer does not reallocate the whole window on every Event.
func (s *Session) RecordEvent(event gotato.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventLimit < 0 {
		return
	}
	event = cloneEvent(event)
	if len(s.events) < s.eventLimit {
		s.events = append(s.events, event)
	} else {
		if len(s.events) > s.eventLimit {
			// A loaded document held more Events than the limit; normalize.
			s.events = slices.Clone(s.events[len(s.events)-s.eventLimit:])
			s.eventHead = 0
		}
		s.events[s.eventHead] = event
		s.eventHead = (s.eventHead + 1) % s.eventLimit
	}
	s.touch()
}

// retainedEvents returns the retained Events in insertion order. When the ring
// buffer has wrapped it returns a fresh slice; otherwise it aliases s.events
// and must only be read under the Session lock.
func (s *Session) retainedEvents() []gotato.Event {
	if s.eventHead == 0 || s.eventHead >= len(s.events) {
		return s.events
	}
	out := make([]gotato.Event, 0, len(s.events))
	out = append(out, s.events[s.eventHead:]...)
	out = append(out, s.events[:s.eventHead]...)
	return out
}

// Compactions returns the compaction history.
func (s *Session) Compactions() []Compaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.compactions)
}

// RecordCompaction appends a compaction record.
func (s *Session) RecordCompaction(record Compaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactions = append(s.compactions, record)
	s.touch()
}

// Metadata returns a copy of the application metadata.
func (s *Session) Metadata() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.metadata)
}

// Get returns one metadata value.
func (s *Session) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.metadata[key]
	return value, ok
}

// Set writes one metadata value. An empty value deletes the key.
func (s *Session) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		delete(s.metadata, key)
	} else {
		s.metadata[key] = value
	}
	s.touch()
}

func (s *Session) touch() { s.updatedAt = s.now().UTC() }

// Fork creates a new Session from the current state of parent. The fork has
// its own ID, records the parent ID, copies Messages, Usage, Compactions, and
// Metadata, and starts with empty Run and Event records. It is a state
// operation: no relationship between Agents is implied.
func Fork(parent *Session, options ...Option) *Session {
	doc := parent.Snapshot()
	child := New(options...)
	child.mu.Lock()
	defer child.mu.Unlock()
	child.parentID = doc.ID
	child.messages = doc.Messages
	child.usage = doc.Usage
	child.compactions = doc.Compactions
	if len(child.metadata) == 0 {
		child.metadata = doc.Metadata
		if child.metadata == nil {
			child.metadata = map[string]string{}
		}
	}
	child.updatedAt = child.now().UTC()
	return child
}

func cloneMessages(messages []gotato.Message) []gotato.Message {
	out := make([]gotato.Message, len(messages))
	for i, message := range messages {
		out[i] = message.Clone()
	}
	return out
}

// cloneEvents copies the Event list and each Payload map (deep), so a caller
// cannot mutate committed or persisted state through a returned Event.
func cloneEvents(events []gotato.Event) []gotato.Event {
	out := make([]gotato.Event, len(events))
	for i, event := range events {
		out[i] = cloneEvent(event)
	}
	return out
}

func cloneEvent(event gotato.Event) gotato.Event {
	event.Payload = clonePayload(event.Payload)
	return event
}

// clonePayload deep-copies an Event payload, including nested maps and
// slices, so a caller cannot mutate committed or persisted state through a
// returned Event. Only the map/slice shapes an Event payload actually uses are
// cloned; other values are passed through.
func clonePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = clonePayloadValue(value)
	}
	return out
}

func clonePayloadValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return clonePayload(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = clonePayloadValue(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i, item := range typed {
			out[i] = clonePayload(item)
		}
		return out
	case map[string]string:
		return maps.Clone(typed)
	case []string:
		return slices.Clone(typed)
	case []byte:
		return slices.Clone(typed)
	default:
		return value
	}
}
