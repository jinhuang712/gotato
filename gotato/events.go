package gotato

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Event is something that happened to a Session or an Instance, as a value of
// its own type, named for what happened: RunStarted, ToolEnded,
// SessionClaimed. A caller switches on the type and reads fields the
// compiler checks:
//
//	switch e := event.(type) {
//	case gotato.RunStarted:
//	case gotato.TextDelta:
//		fmt.Print(e.Text)
//	case gotato.RunEnded:
//		fmt.Println(e.Status)
//	}
//
// Every type carries a Header. In JSON an Event is one flat object whose
// "kind" names its type; MarshalEvent and UnmarshalEvent convert it, and an
// unknown kind comes back as an UnknownEvent, so a consumer tolerates kinds
// that a later version adds.
type Event interface {
	// Kind names the Event's type, such as "run_started".
	Kind() EventKind
	// Head returns the Event's Header.
	Head() Header
}

// EventKind names the type of an Event, in its JSON "kind".
type EventKind string

// Class returns where an Event of this kind lives: stored in the Record, only
// sent, or an Instance Event.
func (k EventKind) Class() EventClass { return eventClasses[k] }

// EventClass says where an Event lives.
type EventClass string

const (
	// EventClassStored Events are Entries of the Session's Record. Their Number is
	// the Entry's position, so every Instance reads the same number.
	EventClassStored EventClass = "stored"
	// EventClassDelta Events are sent and never stored: text, reasoning and tool
	// progress. What they carry ends up whole in a Message or a Tool Result.
	EventClassDelta EventClass = "delta"
	// EventClassInstance Events happen where no Record can take them, because the
	// Instance holds no Claim on the Session. They go only to that Instance's
	// subscribers, and carry no number.
	EventClassInstance EventClass = "instance"
)

// Header is what every Event carries: when it happened and the Session it
// concerns, if any. A stored Event also has its Number, and an Event of a Run
// its Run and its Turn.
type Header struct {
	Time      time.Time `json:"time"`
	SessionID SessionID `json:"session_id,omitempty"`
	// Number is a stored Event's position in the Session's Record. A delta
	// carries the Number of the stored Event it follows. An Instance Event
	// has none.
	Number uint64     `json:"number,omitempty"`
	RunID  RunID      `json:"run_id,omitempty"`
	Turn   TurnNumber `json:"turn,omitempty"`
}

// Head returns the Header; every Event type gets it by embedding Header.
func (h Header) Head() Header { return h }

// NewHeader returns a Header stamped with the current time.
func NewHeader(session SessionID, run RunID, turn TurnNumber) Header {
	return Header{Time: time.Now(), SessionID: session, RunID: run, Turn: turn}
}

// WithNumber returns a copy of event whose Number is number.
func WithNumber(event Event, number uint64) Event {
	return withHeader(event, func(h *Header) { h.Number = number })
}

// WithSession returns a copy of event whose SessionID is id, when it has none.
func WithSession(event Event, id SessionID) Event {
	if event.Head().SessionID != "" || id == "" {
		return event
	}
	return withHeader(event, func(h *Header) { h.SessionID = id })
}

// withHeader copies an Event and edits the copy's Header. Event types are
// plain structs that embed Header, so one reflective copy serves them all.
func withHeader(event Event, edit func(*Header)) Event {
	value := reflect.ValueOf(event)
	if value.Kind() != reflect.Struct {
		return event
	}
	field := value.FieldByName("Header")
	if !field.IsValid() {
		return event
	}
	copied := reflect.New(value.Type()).Elem()
	copied.Set(value)
	header := field.Interface().(Header)
	edit(&header)
	copied.FieldByName("Header").Set(reflect.ValueOf(header))
	return copied.Interface().(Event)
}

// MarshalEvent encodes an Event as one flat JSON object whose "kind" names
// its type.
func MarshalEvent(event Event) ([]byte, error) { return json.Marshal(event) }

// UnmarshalEvent decodes the JSON that MarshalEvent wrote. A kind this
// version does not know comes back as an UnknownEvent.
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

// MarshalJSON writes the Event as it was read.
func (e UnknownEvent) MarshalJSON() ([]byte, error) {
	if len(e.Raw) == 0 {
		return marshalKind(e.EventKind, struct{}{})
	}
	return e.Raw, nil
}

// ---- reasons ----------------------------------------------------------------

// ClaimWhy says why an Instance claimed a Session.
type ClaimWhy string

const (
	// ClaimWhyNewSession: the Instance created the Session.
	ClaimWhyNewSession ClaimWhy = "new_session"
	// ClaimWhyIdle: the Session was idle, released by its last Writer.
	ClaimWhyIdle ClaimWhy = "idle"
	// ClaimWhyWriterGone: its Writer's Liveness row was no longer alive.
	ClaimWhyWriterGone ClaimWhy = "writer_gone"
	// ClaimWhyWaited: the Instance waited for its Writer to finish first.
	ClaimWhyWaited ClaimWhy = "waited"
)

// CompactionWhy says why a compaction ran.
type CompactionWhy string

const (
	CompactionWhyOnDemand     CompactionWhy = "on_demand"
	CompactionWhyPastCeiling  CompactionWhy = "past_ceiling"
	CompactionWhyBeforeUnload CompactionWhy = "before_unload"
	// CompactionWhyBeforeCacheExpires: the Session was idle, and was compacted
	// shortly before the provider's prompt cache expired.
	CompactionWhyBeforeCacheExpires CompactionWhy = "before_cache_expires"
	// CompactionWhyOverflow: the provider refused a request as too long.
	CompactionWhyOverflow CompactionWhy = "overflow"
	// CompactionWhyPastBudget: the Segments together passed their budget, and
	// the oldest were merged with no Segment closed.
	CompactionWhyPastBudget CompactionWhy = "past_budget"
)

// MessageSource says where a user Message came from.
type MessageSource string

const (
	MessageSourcePrompt   MessageSource = "prompt"
	MessageSourceSteer    MessageSource = "steer"
	MessageSourceFollowUp MessageSource = "follow_up"
	// MessageSourceInterrupt is a Message that cut the model call in flight short
	// and started the next Turn of the same Run.
	MessageSourceInterrupt MessageSource = "interrupt"
	// MessageSourceNotice is a notice (G-F29): in message_dropped and
	// message_rejected it says what kind of message was dropped or refused.
	MessageSourceNotice MessageSource = "notice"
)

// Compaction records one replacement of a Message prefix by a summary, so an
// application can always tell what was compacted and what replaced it.
type Compaction struct {
	At               time.Time `json:"at"`
	ReplacedMessages int       `json:"replaced_messages"`
	FromMessageID    MessageID `json:"from_message_id,omitempty"`
	ToMessageID      MessageID `json:"to_message_id,omitempty"`
	// Recap names what wrote the Recap, or "none" when the Segment
	// has none.
	Recap       string `json:"recap"`
	BytesBefore int    `json:"bytes_before"`
	BytesAfter  int    `json:"bytes_after"`
	// Segment is the number of the Segment the compaction closed, or 0 for
	// a compaction that closed none. For a merge it is the merged Segment's
	// number.
	Segment int `json:"segment,omitempty"`
	// Merged lists the numbers of the Segments a merge replaced with one
	// merged Segment, oldest first. It is empty for a compaction that closed
	// a Segment.
	Merged []int `json:"merged,omitempty"`
	// TokensBefore and TokensAfter are estimates of the Context before and
	// after, and Ceiling the ceiling in force, or 0 for none.
	TokensBefore int `json:"tokens_before,omitempty"`
	TokensAfter  int `json:"tokens_after,omitempty"`
	Ceiling      int `json:"ceiling,omitempty"`
	// RecapUsage is what the model call that wrote the Recap used.
	RecapUsage *Usage `json:"recap_usage,omitempty"`
	// Tools are the loaded Tools the compaction kept loaded.
	Tools []string `json:"tools,omitempty"`
}

// ForkedFrom names the Session a new Session was forked from, and the
// position of its Record the fork branches at.
type ForkedFrom struct {
	SessionID SessionID `json:"session_id"`
	Number    uint64    `json:"number"`
}

// ToolOutcome is how one Tool call of a Turn ended.
type ToolOutcome struct {
	ToolID   string           `json:"tool_id"`
	CallID   ToolCallID       `json:"call_id"`
	Status   ToolResultStatus `json:"status"`
	Executed bool             `json:"executed"`
}

// ---- Runs and replies -------------------------------------------------------

// RunStarted: a Run starts.
type RunStarted struct{ Header }

// RunEnded: a Run settles, with its status, its cause and its stop reason. It
// does not carry the final text, which can be long: read it from the Run's
// result (service.RunResponse.FinalText), or from the Transcript.
type RunEnded struct {
	Header
	Status RunStatus `json:"status"`
	// Cause is the error code that ended a Run that did not complete.
	Cause ErrorCode `json:"cause,omitempty"`
	// StoppedByExtension is set when a TurnStopper ended the Run; StopReason
	// is the reason it gave.
	StoppedByExtension bool   `json:"stopped_by_extension,omitempty"`
	StopReason         string `json:"stop_reason,omitempty"`
	Turns              uint32 `json:"turns"`
	ToolCalls          uint32 `json:"tool_calls"`
	Usage              Usage  `json:"usage"`
	ElapsedMS          int64  `json:"elapsed_ms"`
	TextBytes          uint64 `json:"text_bytes,omitempty"`
	ReasoningBytes     uint64 `json:"reasoning_bytes,omitempty"`
}

// TurnStarted: a Turn starts.
type TurnStarted struct{ Header }

// TurnEnded: a Turn ends, with its usage.
type TurnEnded struct {
	Header
	StopReason     StopReason    `json:"stop_reason"`
	Usage          Usage         `json:"usage"`
	ElapsedMS      int64         `json:"elapsed_ms"`
	TextBytes      uint64        `json:"text_bytes"`
	ReasoningBytes uint64        `json:"reasoning_bytes"`
	ToolCalls      int           `json:"tool_calls"`
	Tools          []ToolOutcome `json:"tools,omitempty"`
}

// ContextBuilt: a Turn's Context is built, with its prefix hash and sizes.
type ContextBuilt struct {
	Header
	Strategy         string            `json:"strategy,omitempty"`
	Messages         int               `json:"messages"`
	SourceMessages   int               `json:"source_messages"`
	SelectedMessages int               `json:"selected_messages"`
	PrefixHash       string            `json:"prefix_hash"`
	PrefixMessages   int               `json:"prefix_messages"`
	SystemBytes      int               `json:"system_bytes"`
	Tools            int               `json:"tools"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// ReplyStarted: the model's reply starts. It is stored before the Model is
// called, so a Writer that can no longer write keeps the Model from being
// called.
type ReplyStarted struct {
	Header
	MessageID MessageID `json:"message_id"`
}

// ReplyEnded: the model's reply ends, with the Message it committed, the model
// that answered and why it stopped.
type ReplyEnded struct {
	Header
	Message    Message    `json:"message"`
	Model      string     `json:"model,omitempty"`
	StopReason StopReason `json:"stop_reason"`
	ToolCalls  int        `json:"tool_calls"`
}

// TextDelta: a piece of the reply's text. A delta is sent and never stored.
type TextDelta struct {
	Header
	MessageID MessageID `json:"message_id"`
	Text      string    `json:"text"`
}

// ReasoningDelta: a piece of the reply's reasoning.
type ReasoningDelta struct {
	Header
	MessageID MessageID `json:"message_id"`
	Text      string    `json:"text"`
}

// ReplyCutOff: the reply's stream broke, and the text so far is kept as a
// Cut-off reply.
type ReplyCutOff struct {
	Header
	Message Message   `json:"message"`
	Cause   ErrorCode `json:"cause,omitempty"`
}

// ReplyPartKept: the text and reasoning a streaming reply has produced since
// the previous part, kept in the Record every second or few KiB so that a
// crash loses at most the last part.
type ReplyPartKept struct {
	Header
	MessageID MessageID `json:"message_id"`
	Text      string    `json:"text,omitempty"`
	Reasoning string    `json:"reasoning,omitempty"`
}

// ModelRetried: the gateway calls the model again.
type ModelRetried struct {
	Header
	Attempt int    `json:"attempt"`
	WaitMS  int64  `json:"wait_ms"`
	Error   string `json:"error,omitempty"`
}

// ---- Tools ------------------------------------------------------------------

// ToolStarted: a Tool starts. It is stored before the Tool runs, so a call
// that started is known to have started.
type ToolStarted struct {
	Header
	MessageID MessageID  `json:"message_id,omitempty"`
	CallID    ToolCallID `json:"call_id"`
	ToolID    string     `json:"tool_id"`
}

// ToolEnded: a Tool ends, with its status and how long it ran.
type ToolEnded struct {
	Header
	CallID    ToolCallID       `json:"call_id"`
	ToolID    string           `json:"tool_id,omitempty"`
	Status    ToolResultStatus `json:"status"`
	Executed  bool             `json:"executed"`
	ElapsedMS float64          `json:"elapsed_ms"`
}

// ToolProgress: a Tool reports its own progress. It is sent and never stored.
type ToolProgress struct {
	Header
	CallID ToolCallID `json:"call_id"`
	Text   string     `json:"text"`
}

// ToolResultCommitted: a Tool Result enters the Transcript, in the order the
// model asked.
type ToolResultCommitted struct {
	Header
	Message Message          `json:"message"`
	CallID  ToolCallID       `json:"call_id"`
	Status  ToolResultStatus `json:"status"`
}

// ToolResultSpilled: a Tool Result over one page is stored in full, as an
// Entry of its own, before the Tool Result Message that holds its preview
// and its ref is committed. It is a log entry: a load never reads it, and
// read_result reads its Body when the model asks. Subscribers, and the Events
// a Record retains, get it without the Body, whose size stays in Bytes.
type ToolResultSpilled struct {
	Header
	CallID ToolCallID `json:"call_id"`
	ToolID string     `json:"tool_id,omitempty"`
	// Bytes is the size of the body, in bytes of text.
	Bytes int `json:"bytes"`
	// Body is the text of the Tool Result, whole. The text Parts of a result
	// are joined with a line break.
	Body string `json:"body,omitempty"`
}

// WithoutBody returns the Event as subscribers and the retained window see it:
// a ToolResultSpilled without its Body. Any other Event is returned as it is.
func WithoutBody(event Event) Event {
	if spilled, ok := event.(ToolResultSpilled); ok {
		spilled.Body = ""
		return spilled
	}
	return event
}

// ToolLoadedWhy says why a Tool was loaded.
type ToolLoadedWhy string

// Why a Tool is loaded.
const (
	// ToolLoadedWhyNames: search_tools was given the Tool's name.
	ToolLoadedWhyNames ToolLoadedWhy = "names"
	// ToolLoadedWhyQuery: a search of search_tools found the Tool among its top matches.
	ToolLoadedWhyQuery ToolLoadedWhy = "query"
	// ToolLoadedWhyCall: the model called a catalog Tool it had not loaded.
	ToolLoadedWhyCall ToolLoadedWhy = "call"
)

// ToolLoaded: a catalog Tool is loaded, so the model is offered it. It is a
// fact of the Session: later Runs on any instance, forks and Checkpoints keep
// the Tool loaded until a compaction unloads it.
type ToolLoaded struct {
	Header
	// Tool is the Tool's qualified ID, "<toolset>.<tool>".
	Tool string        `json:"tool"`
	Why  ToolLoadedWhy `json:"why"`
}

// ToolUnloadedWhy says why a Tool was unloaded.
type ToolUnloadedWhy string

// ToolUnloadedWhyCompaction: a compaction closed a Segment that did not call the
// Tool, and the kept tail does not call it either.
const ToolUnloadedWhyCompaction ToolUnloadedWhy = "compaction"

// ToolUnloadedWhyGone: the Tool's ToolSet answered at the start of a Run and no
// longer carries the Tool.
const ToolUnloadedWhyGone ToolUnloadedWhy = "gone"

// ToolUnloaded: a loaded Tool is no longer offered. It is a fact of the
// Session, like ToolLoaded.
type ToolUnloaded struct {
	Header
	Tool string          `json:"tool"`
	Why  ToolUnloadedWhy `json:"why"`
}

// ---- Messages into a Session ------------------------------------------------

// RunRequested: a request for a new Run arrives. An Instance Event.
type RunRequested struct {
	Header
	Agent string `json:"agent,omitempty"`
}

// RunWaiting: a request waits for a busy Session. An Instance Event.
type RunWaiting struct {
	Header
	Why string `json:"why,omitempty"`
}

// RunRejected: a request is refused, with why: "busy", "at_capacity",
// "draining", or "activation_limit" when the Instance is past its hard limit of
// active Sessions. An Instance Event.
type RunRejected struct {
	Header
	Why string `json:"why"`
}

// MessageQueued: a Steer, FollowUp or Interrupt waits for its point in the Run.
type MessageQueued struct {
	Header
	MessageID MessageID     `json:"message_id"`
	Source    MessageSource `json:"source"`
}

// MessageCommitted: a user Message enters the Transcript, with where it came
// from: the prompt, a Steer, a FollowUp or an Interrupt.
type MessageCommitted struct {
	Header
	Message Message       `json:"message"`
	Source  MessageSource `json:"source"`
}

// NoticeQueued: a notice from a service or an extension waits for the next
// model call (G-F29). Text is kept so that the notice can be rebuilt from the
// Event.
type NoticeQueued struct {
	Header
	MessageID MessageID `json:"message_id"`
	// NoticeKind is the notice's label; "kind" in the JSON names the Event.
	NoticeKind string       `json:"notice_kind"`
	Source     NoticeSource `json:"source"`
	Text       string       `json:"text"`
}

// NoticeCommitted: a notice enters the Transcript, right before a model call.
// Queued is the number of the notice_queued it came from, absent for a notice
// of Gotato's own, which is committed at once; its cause is in
// Message.Notice.CausedBy.
type NoticeCommitted struct {
	Header
	Message Message `json:"message"`
	Queued  uint64  `json:"queued,omitempty"`
}

// MessageDropped: a waiting message is dropped because the Run ended first.
type MessageDropped struct {
	Header
	MessageID MessageID     `json:"message_id"`
	Source    MessageSource `json:"source"`
	Why       string        `json:"why"`
}

// MessageRejected: a message is refused because the queue is full.
type MessageRejected struct {
	Header
	MessageID MessageID     `json:"message_id"`
	Source    MessageSource `json:"source"`
	Why       string        `json:"why"`
}

// AbortRequested: an Abort arrives. The Run's RunEnded follows, and the time
// between the two is how long the Abort took.
type AbortRequested struct{ Header }

// ---- Claims -----------------------------------------------------------------

// SessionClaimed: an Instance claims the Session, with why, and which Instance
// held it before. A Writer that opens a Session it holds already writes it
// again, with the same Epoch.
type SessionClaimed struct {
	Header
	Instance string   `json:"instance"`
	Epoch    uint64   `json:"epoch"`
	Why      ClaimWhy `json:"why"`
	From     string   `json:"from,omitempty"`
}

// SessionReleased: the Writer lets the Session go.
type SessionReleased struct {
	Header
	Instance string `json:"instance"`
	Epoch    uint64 `json:"epoch"`
}

// SessionFenced: a write is refused because another Instance holds the Claim.
// An Instance Event.
type SessionFenced struct {
	Header
	Instance string `json:"instance"`
	// Position is the position of the Record that the refused write needed.
	Position uint64 `json:"position,omitempty"`
}

// LivenessLapsed: this Instance's Liveness row was not renewed in time, so it
// writes nothing more. An Instance Event.
type LivenessLapsed struct {
	Header
	Instance string `json:"instance"`
}

// ---- The Session ------------------------------------------------------------

// SessionCreated: a Session is created, with the Messages and metadata its
// Record starts with. It is the first Entry of every Record, and a Checkpoint.
type SessionCreated struct {
	Header
	CreatedAt time.Time `json:"created_at"`
	// RecordEpoch is the Record's identity as a log. It is chosen when the
	// Record is created and changes only if the Record is deleted and created
	// again under the same ID, so a client that holds a number knows whether
	// the number still means the Event it saw. A Record written before it
	// has none.
	RecordEpoch string            `json:"record_epoch,omitempty"`
	ForkedFrom  *ForkedFrom       `json:"forked_from,omitempty"`
	Messages    []Message         `json:"messages,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Compactions is the compaction history a fork inherits.
	Compactions []Compaction `json:"compactions,omitempty"`
	// Tools are the Tools the model had loaded, in the order it did, which a
	// fork inherits.
	Tools []string `json:"tools,omitempty"`
	// Segments are the numbers of the Segments the Context starts with, in
	// order, before Messages: a fork inherits them. Their SegmentClosed
	// Events follow this one.
	Segments []int `json:"segments,omitempty"`
	// Tail says where Messages were committed, when it is known.
	Tail []MessagePosition `json:"tail,omitempty"`
}

// MetadataChanged: a setting in the Session's metadata changes. An empty Value
// deletes the key.
type MetadataChanged struct {
	Header
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// CompactionStarted: a compaction starts.
type CompactionStarted struct {
	Header
	Why CompactionWhy `json:"why"`
}

// CompactionEnded: a compaction appends its Checkpoint: the Segments by
// number, the Messages and metadata the Session continues with, every compaction so far, and the Claim
// in force, so that a load that starts here still knows the Writer and its
// Epoch.
type CompactionEnded struct {
	Header
	Instance   string            `json:"instance,omitempty"`
	Epoch      uint64            `json:"epoch,omitempty"`
	Why        CompactionWhy     `json:"why"`
	Compaction *Compaction       `json:"compaction,omitempty"`
	History    []Compaction      `json:"history,omitempty"`
	Messages   []Message         `json:"messages"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	// Tools are the Tools the model had loaded, in the order it did, as a
	// Checkpoint keeps them.
	Tools []string `json:"tools,omitempty"`
	// Segments are the numbers of the Segments the Context starts with, in
	// order. A load resolves each to the Messages its SegmentClosed Event
	// stored, so the Checkpoint does not copy them.
	Segments []int `json:"segments,omitempty"`
	// Tail says where the Messages the Checkpoint keeps, which follow the
	// Segments, were committed: their IDs, Entry numbers and times.
	Tail []MessagePosition `json:"tail,omitempty"`
}

// SegmentClosed: a compaction closed a Segment. It is written once, before
// the Checkpoint (CompactionEnded) that lists its number, and holds the
// Messages that render the Segment, which are never rewritten. The Entries it
// covers stay in the Record.
type SegmentClosed struct {
	Header
	Segment Segment `json:"segment"`
}

// CompactionFailed: a compaction failed.
type CompactionFailed struct {
	Header
	Why   CompactionWhy `json:"why"`
	Cause ErrorCode     `json:"cause,omitempty"`
}

// SessionUnloaded: the Session leaves this Instance's memory, with why. A live
// Session that closes says "close". The Runner's warm copy of an idle Session
// says "ttl" when its time in memory ended, "taken_over" when another Instance
// wrote the Record (or deleted it) so the copy was out of date, "deleted" when
// this Instance deleted the Session, "drain" or "closed" when the Runner let
// every copy go, "failed" when a write of the copy failed and it is dropped, and
// "liveness_lapsed" when this Instance could not show its Liveness row. An
// Instance Event.
type SessionUnloaded struct {
	Header
	Why string `json:"why"`
}

// SessionDeleted: the Session's Record is deleted. An Instance Event.
type SessionDeleted struct{ Header }

// ---- The store and extensions -----------------------------------------------

// SessionLoaded: how long the store took to load the Session. An Instance
// Event.
type SessionLoaded struct {
	Header
	Messages  int     `json:"messages"`
	ElapsedMS float64 `json:"elapsed_ms"`
	Failed    bool    `json:"failed,omitempty"`
}

// SessionSaved: how long the store took to write a Run's steps, summed, and
// how many writes that was. An Instance Event.
type SessionSaved struct {
	Header
	Messages  int     `json:"messages"`
	ElapsedMS float64 `json:"elapsed_ms"`
	Writes    int     `json:"writes"`
	Failed    bool    `json:"failed,omitempty"`
}

// StoreWriteFailed: a write failed, for another reason than a taken position.
// An Instance Event; the store's own error text stays out, because Events
// reach clients.
type StoreWriteFailed struct {
	Header
	Code ErrorCode `json:"code,omitempty"`
}

// ExtensionFailed: an advisory extension failed, and the Run went on. It is
// also the warning for a ToolSet that did not resolve at the start of a Run:
// Stage is "toolset:<name>" and Code is tool_resolution_failure.
type ExtensionFailed struct {
	Header
	Stage string    `json:"stage"`
	Code  ErrorCode `json:"code,omitempty"`
}

// EventsSkipped: an Instance's stream fell behind and skipped Events. Each
// stored Event names its Session and number, so the subscriber can read what
// it missed from the Record. An Instance Event.
type EventsSkipped struct {
	Header
	Skipped uint64 `json:"skipped"`
}
