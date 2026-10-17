// Package session holds a Session's Record, what the Store keeps of it, and
// the Stores that keep it.
//
// A Record holds the Session's identity, the committed Messages, per-Run
// records, usage, a bounded window of runtime Events, compaction history, and
// free-form application metadata. Once it is stored, every change to it is
// also an Entry of its log: facts, which rebuild the Session, and log
// Entries, which record what happened (see Entry). It implements
// gotato.Transcript, so a live gotato.Session commits to it directly and
// takes its ID:
//
//	r := session.New()
//	s, _ := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(r), gotato.WithExtension(session.NewRecorder(r)))
//	s.Prompt(ctx, gotato.UserMessage("hi"))
//	store.Save(ctx, r)
//
// Tasks, projects, roles, and agent hierarchy are application semantics and
// belong in Metadata.
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

// Run is the per-Run record kept in a Record.
type Run struct {
	RunID      gotato.RunID     `json:"run_id"`
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

// Document is the serializable form of a Record. Stores persist it; Snapshot
// and Load convert to and from a Record.
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
	// Seq is the position of the last Entry the store held when this
	// Document was read; 0 for a Record never stored.
	Seq uint64 `json:"seq,omitempty"`
}

// Record is what the Store keeps of a Session: what happened in it. It is safe
// for concurrent use; the live gotato.Session that runs against it must still
// be the only writer of Messages during a Run.
type Record struct {
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

	// parentSeq is the parent's head when this Record was forked from it.
	parentSeq uint64
	// head is the Seq of the last Entry the store holds. logging turns on
	// once the Record is stored; from then on every change queues a pending
	// Entry, and the Events it retains wait in pendingEvents.
	head          uint64
	logging       bool
	pending       []pendingEntry
	pendingEvents []gotato.Event
	// instance names the Instance whose writes this Record's Entries are,
	// and claim the Record's writer as its facts last named it.
	instance string
	claim    Claim
	// running is the Run that started and has not ended. baseRuns counts
	// the Runs of a Record loaded from its facts alone, whose Run records
	// are in the log and were not read.
	running  gotato.RunID
	baseRuns int
}

// Option configures New.
type Option func(*Record)

// WithID sets the Session ID. Default: a random 16-byte hex ID.
func WithID(id string) Option { return func(s *Record) { s.id = id } }

// WithEventLimit bounds retained Events. Zero keeps DefaultEventLimit; a
// negative value disables Event retention.
func WithEventLimit(limit int) Option {
	return func(s *Record) {
		if limit == 0 {
			limit = DefaultEventLimit
		}
		s.eventLimit = limit
	}
}

// WithMetadata sets initial application metadata.
func WithMetadata(metadata map[string]string) Option {
	return func(s *Record) { s.metadata = maps.Clone(metadata) }
}

// WithMessages sets the Messages a new Record starts with, as history that
// its first Checkpoint holds rather than Messages committed one by one.
func WithMessages(messages ...gotato.Message) Option {
	return func(s *Record) { s.messages = cloneMessages(messages) }
}

// WithClock overrides the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Record) { s.now = now } }

// New creates an empty Record.
func New(options ...Option) *Record {
	s := &Record{eventLimit: DefaultEventLimit, now: time.Now}
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

// Load rebuilds a Record from a persisted Document.
func Load(doc Document) (*Record, error) {
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, gotato.ErrorOf(gotato.ErrNotSupported, "session: unsupported schema_version")
	}
	if doc.ID == "" {
		return nil, gotato.ErrorOf(gotato.ErrInvalidArgument, "session: document has no id")
	}
	s := &Record{
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
func (s *Record) Snapshot() Document {
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
		Seq:           s.head,
	}
}

// ID returns the Session identity.
func (s *Record) ID() string { return s.id }

// ParentID returns the Session this one was forked from, or "".
func (s *Record) ParentID() string { return s.parentID }

// CreatedAt returns the creation time.
func (s *Record) CreatedAt() time.Time { return s.createdAt }

// UpdatedAt returns the time of the last mutation.
func (s *Record) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// Messages implements gotato.Transcript. The returned slice is a copy.
func (s *Record) Messages() []gotato.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.messages)
}

// Len returns the number of committed Messages without copying them.
func (s *Record) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// Append implements gotato.Transcript.
func (s *Record) Append(message gotato.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message = message.Clone()
	s.messages = append(s.messages, message)
	s.touch()
	s.queue(EntryMessage, LayerFact, message)
	return nil
}

// Runs returns the Run records.
func (s *Record) Runs() []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.runs)
}

// RecordRunStart adds a running Run record.
func (s *Record) RecordRunStart(runID gotato.RunID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := Run{RunID: runID, Status: gotato.RunRunning, StartedAt: at.UTC()}
	s.runs = append(s.runs, run)
	s.running = runID
	s.touch()
	s.queue(EntryRunStart, LayerLog, run)
}

// RecordRunEnd finalizes the matching Run record and aggregates usage.
func (s *Record) RecordRunEnd(runID gotato.RunID, status gotato.RunStatus, errText, stopReason string, turns, toolCalls uint32, usage gotato.Usage, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := Run{RunID: runID, Status: status, Error: errText, StopReason: stopReason, EndedAt: at.UTC(), Turns: turns, ToolCalls: toolCalls, Usage: usage}
	run = s.endRun(run)
	s.touch()
	s.queue(EntryRunEnd, LayerLog, run)
}

// endRun finalizes the Run record with run's RunID, or adds run when there is
// none, adds its usage to the Record's, and returns the final Run. The caller
// holds the lock.
func (s *Record) endRun(run Run) Run {
	found := false
	for i := len(s.runs) - 1; i >= 0; i-- {
		if s.runs[i].RunID != run.RunID {
			continue
		}
		run.StartedAt = s.runs[i].StartedAt
		s.runs[i] = run
		found = true
		break
	}
	if !found {
		s.runs = append(s.runs, run)
	}
	if s.running == run.RunID {
		s.running = ""
	}
	s.usage.InputTokens += run.Usage.InputTokens
	s.usage.OutputTokens += run.Usage.OutputTokens
	s.usage.TotalTokens += run.Usage.TotalTokens
	s.usage.CacheReadTokens += run.Usage.CacheReadTokens
	s.usage.CacheWriteTokens += run.Usage.CacheWriteTokens
	return run
}

// Usage returns the aggregated usage across recorded Runs.
func (s *Record) Usage() gotato.Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usage
}

// Events returns the retained Events in order, with copied payloads.
func (s *Record) Events() []gotato.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneEvents(s.retainedEvents())
}

// RecordEvent retains one Event, dropping the oldest beyond the limit. Once the
// window is full the backing slice is reused as a ring buffer so a streaming
// producer does not reallocate the whole window on every Event.
func (s *Record) RecordEvent(event gotato.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventLimit < 0 {
		return
	}
	event = cloneEvent(event)
	s.retain(event)
	if s.logging {
		s.pendingEvents = append(s.pendingEvents, event)
	}
	s.touch()
}

// retain keeps one Event in the bounded window. The caller holds the lock.
func (s *Record) retain(event gotato.Event) {
	if s.eventLimit < 0 {
		return
	}
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
}

// retainedEvents returns the retained Events in insertion order. When the ring
// buffer has wrapped it returns a fresh slice; otherwise it aliases s.events
// and must only be read under the Session lock.
func (s *Record) retainedEvents() []gotato.Event {
	if s.eventHead == 0 || s.eventHead >= len(s.events) {
		return s.events
	}
	out := make([]gotato.Event, 0, len(s.events))
	out = append(out, s.events[s.eventHead:]...)
	out = append(out, s.events[:s.eventHead]...)
	return out
}

// Compactions returns the compaction history.
func (s *Record) Compactions() []Compaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.compactions)
}

// Metadata returns a copy of the application metadata.
func (s *Record) Metadata() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.metadata)
}

// Get returns one metadata value.
func (s *Record) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.metadata[key]
	return value, ok
}

// Set writes one metadata value. An empty value deletes the key. Setting the
// value a key already has changes nothing, so it adds nothing to the log.
func (s *Record) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.metadata[key]; current == value && (ok || value == "") {
		return
	}
	if value == "" {
		delete(s.metadata, key)
	} else {
		s.metadata[key] = value
	}
	s.touch()
	s.queue(EntryMetadata, LayerFact, MetadataChange{Key: key, Value: value})
}

func (s *Record) touch() { s.updatedAt = s.now().UTC() }

// Claim returns the Record's writer as its facts last named it.
func (s *Record) Claim() Claim {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claim
}

// claimFor makes instance the Record's writer with the next Epoch, as a fact.
func (s *Record) claimFor(instance string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instance = instance
	s.claim = Claim{Instance: instance, Epoch: s.claim.Epoch + 1}
	s.touch()
	s.queue(EntryClaim, LayerFact, s.claim)
}

// interruptRun ends a Run that started and never ended, because the writer
// that ran it stopped, as failed with "interrupted".
func (s *Record) interruptRun(runID gotato.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	known := false
	for _, run := range s.runs {
		known = known || run.RunID == runID
	}
	if !known && s.baseRuns > 0 {
		// The Run is counted in baseRuns already; ending it adds its record.
		s.baseRuns--
	}
	run := s.endRun(Run{RunID: runID, Status: gotato.RunFailed, Error: "interrupted", EndedAt: s.now().UTC()})
	s.touch()
	s.queue(EntryRunEnd, LayerLog, run)
}

// Fork creates the Record of a new Session from the current state of parent.
// The fork has its own ID, records the parent ID and the point of the
// parent's log it branches at, copies Messages, Usage, Compactions, and
// Metadata, and starts with empty Run and Event records. It is a state
// operation: no relationship between Sessions is implied.
func Fork(parent *Record, options ...Option) *Record {
	doc := parent.Snapshot()
	parentSeq := parent.Head()
	child := New(options...)
	child.mu.Lock()
	defer child.mu.Unlock()
	child.parentID = doc.ID
	child.parentSeq = parentSeq
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
