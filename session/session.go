// Package session holds a Session's Record, what the Store keeps of it, and
// the Stores that keep it.
//
// A Record holds the Session's identity, the committed Messages, per-Run
// records, usage, a bounded window of its stored Events, compaction history,
// and free-form application metadata. Once it is stored, it is the Session's
// Event log: every Entry is a stored Event, numbered by its position. Facts,
// which rebuild the Session, and log Entries, which record what happened, are
// both Events (see Entry). A Record implements gotato.Transcript and
// gotato.Journal, so a live gotato.Session commits to it directly, takes its
// ID, and keeps its Events in it:
//
//	r := session.New()
//	s, _ := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(r))
//	s.Prompt(ctx, gotato.UserMessage("hi"))
//
// A compaction can close a stretch of history into a gotato.Segment: the
// Record writes it once (closeSegment), and a Checkpoint lists the Segments
// the Context starts with by number (checkpointSegments), so the Messages that
// render a Segment are stored once and never change. Tasks, projects, roles,
// and agent hierarchy are application semantics and belong in Metadata.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// SchemaVersion is the persisted document version written by stores.
const SchemaVersion = 1

// defaultEventLimit bounds the Events a Record keeps in memory. Older Events
// are dropped first; Runs, Messages, and Usage are never dropped, and the
// store keeps every Event.
const defaultEventLimit = 1000

// RunInfo is the per-RunInfo record kept in a Record: what its RunStarted and RunEnded
// Events say.
type RunInfo struct {
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

// Document is the serializable form of a Record. Snapshot and FromDocument convert to
// and from a Record. A document of an earlier version holds the Events of its
// time; they are not read again.
type Document struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	ParentID      string `json:"parent_id,omitempty"`
	// RecordEpoch is the Record's identity as a log (Record.RecordEpoch).
	RecordEpoch string            `json:"record_epoch,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Messages    []gotato.Message  `json:"messages"`
	Runs        []RunInfo         `json:"runs,omitempty"`
	Events      []gotato.Event    `json:"events,omitempty"`
	Usage       gotato.Usage      `json:"usage"`
	Compactions []Compaction      `json:"compactions,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
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

// replyPart is the text and reasoning a streaming reply produced, collected
// while a log is read.
type replyPart struct {
	RunID     gotato.RunID
	MessageID gotato.MessageID
	Text      string
	Reasoning string
}

// Record is what the Store keeps of a Session: what happened in it. It is safe
// for concurrent use; the live gotato.Session that runs against it must still
// be the only writer of Messages during a Run.
type Record struct {
	mu       sync.RWMutex
	id       string
	parentID string
	// epoch is the Record's identity as a log (see RecordEpoch).
	epoch     string
	createdAt time.Time
	updatedAt time.Time
	messages  []gotato.Message
	runs      []RunInfo
	// events is the window of stored Events the Record keeps in memory, in a
	// ring once it is full: eventHead is the position of the oldest Event.
	events      []gotato.Event
	eventHead   int
	usage       gotato.Usage
	compactions []Compaction
	metadata    map[string]string
	// loaded are the Tools the model loaded, in the order it did.
	loaded     []string
	eventLimit int
	now        func() time.Time

	// closed holds every Segment the Record has seen closed, by number, and
	// used the numbers of those the Context starts with, in order: the
	// first Messages of messages are their rendered Messages. maxSegment is
	// the highest number seen.
	closed     map[int]gotato.Segment
	used       []int
	maxSegment int
	// positions says where each Message since the last Checkpoint was
	// committed.
	positions map[gotato.MessageID]gotato.MessagePosition

	// parentSeq is the parent's head when this Record was forked from it.
	parentSeq uint64
	// head is the Seq of the last Entry the store holds, and last the number
	// of the last Event numbered, stored or not. logging turns on once the
	// Record is stored; from then on every change queues its Event in
	// pending, until a write takes it.
	head    uint64
	last    uint64
	logging bool
	pending []gotato.Event
	// instance names the Instance whose writes this Record's Entries are,
	// and claim the Record's writer as its facts last named it.
	instance string
	claim    Claim
	// running is the Run that started and has not ended. baseRuns counts
	// the Runs of a Record loaded from its facts alone, whose Run records
	// are in the log and were not read.
	running gotato.RunID
	busy    bool
	// notices are the notices queued and neither committed nor dropped; the
	// RecordInfo keeps them, so a Record loaded from its facts knows them too.
	notices []gotato.PendingNotice
	// lastRunEnd is when the last Run that ended did; it is kept in the
	// RecordInfo, so a Record loaded from its facts knows it too.
	lastRunEnd time.Time
	baseRuns   int
	// started and openParts hold, while a log is read, the Tool calls that
	// started and the replies that were streaming, for repair.
	started   map[gotato.ToolCallID]bool
	openParts []replyPart
	// retainRead says Events read from a log are kept in the window.
	retainRead bool
	// spilled caches the bodies of spilled results this live Record wrote or
	// read, by the Entry's position; see spilled.go.
	spilled spillCache
	// cutOff holds the Cut-off replies that reading a log made out of the
	// parts of replies whose Messages never arrived.
	cutOff []gotato.Message
}

// RecordOption configures New.
type RecordOption func(*Record)

// WithID sets the Session ID. Default: a random 16-byte hex ID.
func WithID(id string) RecordOption { return func(s *Record) { s.id = id } }

// WithMetadata sets initial application metadata.
func WithMetadata(metadata map[string]string) RecordOption {
	return func(s *Record) { s.metadata = maps.Clone(metadata) }
}

// WithMessages sets the Messages a new Record starts with, as history that
// its first Entry holds rather than Messages committed one by one.
func WithMessages(messages ...gotato.Message) RecordOption {
	return func(s *Record) { s.messages = cloneMessages(messages) }
}

// New creates an empty Record.
func New(options ...RecordOption) *Record {
	s := &Record{eventLimit: defaultEventLimit, now: time.Now, retainRead: true, closed: map[int]gotato.Segment{}, positions: map[gotato.MessageID]gotato.MessagePosition{}}
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
	s.epoch = newEpoch()
	s.createdAt = s.now().UTC()
	s.updatedAt = s.createdAt
	return s
}

// newEpoch returns a random identity for a new Record.
func newEpoch() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

// RecordEpoch returns the Record's identity as a log: chosen when the Record
// is created, the same on every load and on every Instance, and different only
// when the Record is deleted and created again under the same ID. A stored
// Event's number means the same Event only within one epoch, so a client that
// resumes a stream from a number sends the epoch it saw. A Record written
// before epochs has none of its own: it reports one made of its creation
// time, the same as RecordInfo.Epoch.
func (s *Record) RecordEpoch() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epochLocked()
}

// Holder reports who holds the Record: the Claim it was last written under,
// and whether that holder is using it, with a Run or a change under way (the
// Record is busy until its writer releases it). A Record that was never
// claimed returns an empty Claim.
func (s *Record) Holder() (claim Claim, busy bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claim, s.busy || s.running != ""
}

// epochLocked is the epoch every surface reports: the Record's own, or for a
// Record written before epochs the one RecordInfo.Epoch names, made of its
// creation time.
func (s *Record) epochLocked() string {
	if s.epoch != "" {
		return s.epoch
	}
	return "created-" + s.createdAt.Format(infoTime)
}

// NewID returns a random, restart-safe identifier.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
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

// Snapshot returns a deep-copied Document of the current state.
func (s *Record) Snapshot() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Document{
		SchemaVersion: SchemaVersion,
		ID:            s.id,
		ParentID:      s.parentID,
		RecordEpoch:   s.epochLocked(),
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

// Append implements gotato.Transcript. The Message is stored by the Event
// that carries it, which the live Session writes next.
func (s *Record) Append(message gotato.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message.Clone())
	s.touch()
	return nil
}

// Write implements gotato.Journal: it numbers events as the next Events of the
// Record, folds what they say into its Runs and usage, and keeps them. The
// Events of a stored Record also wait for the next write of its Writer, which
// numbers them the same way.
func (s *Record) Write(_ context.Context, events []gotato.Event) ([]gotato.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]gotato.Event, len(events))
	for i, event := range events {
		event = gotato.WithSession(event, gotato.SessionID(s.id))
		out[i] = s.number(event)
		s.keepSpilled(out[i])
		s.foldLive(out[i])
		s.indexMessage(out[i], out[i].Head().Time)
	}
	return out, nil
}

// EventsAfter implements gotato.EventSource from the Events the Record keeps
// in memory. It fails with gotato.ErrEventsLost when the window no longer
// reaches back to after, as a Record that is not stored cannot read them again;
// a stored Record's Writer is the source of its Session.
func (s *Record) EventsAfter(_ context.Context, after uint64, _ int) ([]gotato.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	window := s.retainedEvents()
	if (len(window) == 0 && s.last > after) || (len(window) > 0 && window[0].Head().Number > after+1) {
		return nil, gotato.ErrEventsLost
	}
	var out []gotato.Event
	for _, event := range window {
		if event.Head().Number > after {
			out = append(out, event)
		}
	}
	return out, nil
}

// number gives event the next number, keeps it in the window, and queues it
// as an Entry once the Record is stored. The caller holds the lock.
func (s *Record) number(event gotato.Event) gotato.Event {
	s.last++
	event = gotato.WithNumber(event, s.last)
	s.retain(gotato.WithoutBody(event))
	if s.logging {
		s.pending = append(s.pending, event)
	}
	s.touch()
	return event
}

// emit queues an Event of the Record's own, such as a metadata change, for the
// next write, once the Record is stored. The caller holds the lock.
func (s *Record) emit(event gotato.Event) {
	if !s.logging {
		return
	}
	s.number(event)
}

// Durable implements an optional method of gotato.Journal: a Record keeps its
// Events beyond the process only once it is stored.
func (s *Record) Durable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logging
}

// header is the Header of an Event the Record itself produces.
func (s *Record) header() gotato.Header {
	return gotato.NewHeader(gotato.SessionID(s.id), "", 0)
}

// Emit queues a stored Event for the next write of the Record's Writer, such
// as a CompactionStarted. It does nothing while the Record is not stored.
func (s *Record) Emit(event gotato.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(gotato.WithSession(event, gotato.SessionID(s.id)))
}

// foldLive applies what a Run's Event says about Runs and usage. Messages and
// metadata are already in the Record: the Transcript and Set put them there.
// The caller holds the lock.
func (s *Record) foldLive(event gotato.Event) {
	switch e := event.(type) {
	case gotato.RunStarted:
		s.runs = append(s.runs, runStarted(e))
		s.running = e.RunID
	case gotato.RunEnded:
		s.endRun(runEnded(e))
	case gotato.ToolLoaded:
		s.load(e.ToolID)
	case gotato.ToolUnloaded:
		s.unload(e.ToolID)
	case gotato.NoticeQueued, gotato.NoticeCommitted, gotato.MessageDropped:
		s.foldNotice(e)
	}
}

// load adds a Tool to the loaded ones. The caller holds the lock.
func (s *Record) load(id string) {
	if !slices.Contains(s.loaded, id) {
		s.loaded = append(s.loaded, id)
	}
}

// unload removes a Tool from the loaded ones. The caller holds the lock.
func (s *Record) unload(id string) {
	s.loaded = slices.DeleteFunc(s.loaded, func(have string) bool { return have == id })
}

// LoadedTools returns the qualified IDs of the catalog Tools the model loaded
// in the Session, in the order it did. A Session offers them again in later
// Runs on any instance, a fork inherits them, and a Checkpoint keeps them.
func (s *Record) LoadedTools() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.loaded)
}

// SetLoadedTools replaces the Tools the Record holds as loaded with ids, in
// that order, and records no Event: a compaction narrows them just before the
// Checkpoint that keeps them, and emits a ToolUnloaded for each it drops. It
// must not run while a Run is using the Record.
func (s *Record) SetLoadedTools(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = slices.Clone(ids)
}

func runStarted(e gotato.RunStarted) RunInfo {
	return RunInfo{RunID: e.RunID, Status: gotato.RunStatusRunning, StartedAt: e.Time.UTC()}
}

func runEnded(e gotato.RunEnded) RunInfo {
	return RunInfo{
		RunID: e.RunID, Status: e.Status, Error: string(e.Cause), StopReason: e.StopReason,
		EndedAt: e.Time.UTC(), Turns: e.Turns, ToolCalls: e.ToolCalls, Usage: e.Usage,
	}
}

// PendingNotices returns the notices that were queued and neither committed
// nor dropped, in the order they were queued. The next Run on any instance
// queues them again (G-F29).
func (s *Record) PendingNotices() []gotato.PendingNotice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.notices)
}

// foldNotice applies what a notice Event says about the pending notices. The
// caller holds the lock.
func (s *Record) foldNotice(event gotato.Event) {
	switch e := event.(type) {
	case gotato.NoticeQueued:
		// Only a service's notice is durable. Gotato's own, such as time_away,
		// is worked out afresh at each Run start, so keeping one would only
		// risk a stale or a duplicate.
		if e.Source != gotato.NoticeSourceService {
			return
		}
		s.notices = append(s.notices, gotato.PendingNotice{MessageID: e.MessageID, Kind: e.Label, Source: e.Source, Text: e.Text, Number: e.Number})
	case gotato.NoticeCommitted:
		s.notices = slices.DeleteFunc(s.notices, func(p gotato.PendingNotice) bool { return p.Number == e.Queued && e.Queued != 0 })
	case gotato.MessageDropped:
		if e.Source == gotato.MessageSourceNotice {
			s.notices = slices.DeleteFunc(s.notices, func(p gotato.PendingNotice) bool { return p.MessageID == e.MessageID })
		}
	}
}

// LastRunEnd returns when the last Run that ended did: the Session's last busy
// time. It is the zero time when no Run has ended. A Record loaded from its
// facts reads it from the RecordInfo, so it does not need the log.
func (s *Record) LastRunEnd() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRunEnd
}

// Runs returns the Run records.
func (s *Record) Runs() []RunInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.runs)
}

// endRun finalizes the Run record with run's RunID, or adds run when there is
// none, adds its usage to the Record's, and returns the final Run. The caller
// holds the lock.
func (s *Record) endRun(run RunInfo) RunInfo {
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
	if run.EndedAt.After(s.lastRunEnd) {
		s.lastRunEnd = run.EndedAt
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

// Events returns the stored Events the Record keeps in memory, in order: the
// last defaultEventLimit of them. Events
// are values; do not modify what they point to.
func (s *Record) Events() []gotato.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.retainedEvents())
}

// retain keeps one Event in the bounded window. The caller holds the lock.
func (s *Record) retain(event gotato.Event) {
	if s.eventLimit < 0 {
		return
	}
	if len(s.events) < s.eventLimit {
		s.events = append(s.events, event)
		return
	}
	if len(s.events) > s.eventLimit {
		// A loaded document held more Events than the limit; normalize.
		s.events = slices.Clone(s.events[len(s.events)-s.eventLimit:])
		s.eventHead = 0
	}
	s.events[s.eventHead] = event
	s.eventHead = (s.eventHead + 1) % s.eventLimit
}

// retainedEvents returns the retained Events in insertion order. When the ring
// buffer has wrapped it returns a fresh slice; otherwise it aliases s.events
// and must only be read under the lock.
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
	s.emit(gotato.MetadataChanged{Header: s.header(), Key: key, Value: value})
}

func (s *Record) touch() { s.updatedAt = s.now().UTC() }

// Claim returns the Record's writer as its facts last named it.
func (s *Record) Claim() Claim {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claim
}

// claimFor makes instance the Record's writer, as a fact. A new Claim takes
// the next Epoch; an Instance that holds the Record already keeps its own.
func (s *Record) claimFor(instance string, why gotato.ClaimWhy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.claim.Instance
	if from != instance || s.claim.Epoch == 0 {
		s.claim = Claim{Instance: instance, Epoch: s.claim.Epoch + 1}
	}
	s.instance = instance
	s.touch()
	s.emit(gotato.SessionClaimed{Header: s.header(), Instance: instance, Epoch: s.claim.Epoch, Why: why, From: from})
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
	header := s.header()
	header.RunID = runID
	ended := gotato.RunEnded{Header: header, Status: gotato.RunStatusFailed, Cause: "interrupted"}
	s.endRun(runEnded(ended))
	s.touch()
	s.emit(ended)
}

// Fork creates the Record of a new Session from the current state of parent.
// The fork has its own ID, records the parent ID and the point of the
// parent's log it branches at, copies Messages, Usage, Compactions, and
// Metadata, and starts with empty Run and Event records. It is a state
// operation: no relationship between Sessions is implied.
func Fork(parent *Record, options ...RecordOption) *Record {
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
	child.loaded = doc.LoadedTools
	child.inheritSegments(doc.ID, doc.Segments, parent.maxSegmentNumber(), parent.positionTimes())
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
