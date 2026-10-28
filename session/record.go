// Package session holds a Session's Record, what the Store keeps of it, and
// the Stores that keep it.
//
// To write a Store adapter, read these five types first: Store (its doc lists
// the rules adapters miss), Entry, Loaded, RecordInfo, and InstanceRow with
// InstanceState, plus the errors ErrNotFound, ErrExists, ErrConflict and
// ErrExpired. Prove the adapter with session/storetest. MemoryStore and
// FileStore are the Stores that ship. Writer, Claim, WriteStats and the Writer
// options are for hosts that run Sessions on many Instances (the
// service package uses them), and Document, Fork, View and the Record methods
// for code that works with a Record directly.
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
//	s, _ := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithTranscript(r))
//	s.Prompt(ctx, gotato.UserMessage("hi"))
//
// A compaction can close a stretch of history into a gotato.Segment: the
// Record writes it once, and a Checkpoint lists the Segments the Context
// starts with by number, so the Messages that render a Segment are stored once
// and never change. Tasks, projects, roles, and agent hierarchy are
// application semantics and belong in Metadata.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

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
	compactions []gotato.Compaction
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
	// queued are the user messages queued (message_queued) and neither
	// committed nor dropped in the log read since the last Checkpoint. They are
	// read only when the last Writer stopped in the middle of a Run, to be
	// dropped by the Writer that takes the Record over (takeOver).
	queued []gotato.MessageQueued
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

// NewID returns a random, restart-safe identifier.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
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

// PendingNotices returns the notices that were queued and neither committed
// nor dropped, in the order they were queued. The next Run on any instance
// queues them again (G-F29).
func (s *Record) PendingNotices() []gotato.PendingNotice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.notices)
}

// foldQueued applies what a message Event says about the messages queued and
// not yet resolved. The caller holds the lock.
func (s *Record) foldQueued(event gotato.Event) {
	switch e := event.(type) {
	case gotato.MessageQueued:
		s.queued = append(s.queued, e)
	case gotato.MessageCommitted:
		s.queued = slices.DeleteFunc(s.queued, func(q gotato.MessageQueued) bool { return q.MessageID == e.Message.ID })
	case gotato.MessageDropped:
		s.queued = slices.DeleteFunc(s.queued, func(q gotato.MessageQueued) bool { return q.MessageID == e.MessageID })
	}
}

// dropQueued ends the messages that a Writer that stopped left queued: each
// gets a message_dropped, so the Record never keeps a message that was accepted
// and is resolved by nothing. The caller holds the lock.
func (s *Record) dropQueued(why string) {
	for _, queued := range s.queued {
		header := s.header()
		header.RunID = queued.RunID
		s.emit(gotato.MessageDropped{Header: header, MessageID: queued.MessageID, Source: queued.Source, Why: why})
	}
	s.queued = nil
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
		s.notices = append(s.notices, gotato.PendingNotice{MessageID: e.MessageID, Kind: e.NoticeKind, Source: e.Source, Text: e.Text, Number: e.Number})
	case gotato.NoticeCommitted:
		s.notices = slices.DeleteFunc(s.notices, func(p gotato.PendingNotice) bool { return p.Number == e.Queued && e.Queued != 0 })
	case gotato.MessageDropped:
		if e.Source == gotato.MessageSourceNotice {
			s.notices = slices.DeleteFunc(s.notices, func(p gotato.PendingNotice) bool { return p.MessageID == e.MessageID })
		}
	}
}

// Compactions returns the compaction history.
func (s *Record) Compactions() []gotato.Compaction {
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

// Fork creates the Record of a new Session from the current state of parent.
// The fork has its own ID, records the parent ID and the point of the
// parent's log it branches at, copies Messages, Segments, Usage, Compactions,
// loaded Tools and Metadata (unless options set the Metadata), and starts with
// empty Run and Event records. It is a state
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
