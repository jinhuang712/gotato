package service

import "github.com/jinhuang712/gotato/gotato"

// The Runner's Instance Events (decision 048): the Events that happen where no
// Record can take them, such as a request that arrives or a store that is slow.
// They are published on the Instance's stream (Runner.SubscribeInstance) and
// nowhere else. The core keeps the two it publishes itself, SessionUnloaded and
// EventsSkipped.
//
// Each type embeds gotato.Header and states its Kind and Class, as an Event
// type of any package does, so gotato.MarshalEvent writes it as one flat object
// whose "kind" names it, with the same kinds and fields as before.
// gotato.UnmarshalEvent reads it back as a gotato.UnknownEvent: Gotato ships no
// Go client that needs more. Encode one with gotato.MarshalEvent, not with
// encoding/json alone, which would leave the kind out.

// The kinds of the Runner's Instance Events, as they are named in JSON.
const (
	EventKindRunRequested     gotato.EventKind = "run_requested"
	EventKindRunWaiting       gotato.EventKind = "run_waiting"
	EventKindRunRejected      gotato.EventKind = "run_rejected"
	EventKindSessionFenced    gotato.EventKind = "session_fenced"
	EventKindLivenessLapsed   gotato.EventKind = "liveness_lapsed"
	EventKindSessionDeleted   gotato.EventKind = "session_deleted"
	EventKindSessionLoaded    gotato.EventKind = "session_loaded"
	EventKindSessionSaved     gotato.EventKind = "session_saved"
	EventKindStoreWriteFailed gotato.EventKind = "store_write_failed"
)

// RunRequested is published when a request for a new Run arrives. An Instance
// Event.
type RunRequested struct {
	gotato.Header
	Agent string `json:"agent,omitempty"`
}

// RunWaiting is published when a request waits for a busy Session. An Instance
// Event.
type RunWaiting struct {
	gotato.Header
	Why string `json:"why,omitempty"`
}

// RunRejected is published when a request is refused, with why: "busy",
// "at_capacity", "draining", or "activation_limit" when the Instance is past
// its hard limit of active Sessions. An Instance Event.
type RunRejected struct {
	gotato.Header
	Why string `json:"why"`
}

// SessionFenced is published when a write is refused because another Instance
// holds the Claim. An Instance Event.
type SessionFenced struct {
	gotato.Header
	Instance string `json:"instance"`
	// Position is the position of the Record that the refused write needed.
	Position uint64 `json:"position,omitempty"`
}

// LivenessLapsed is published when this Instance's Liveness row is not renewed
// in time, so it writes nothing more. An Instance Event.
type LivenessLapsed struct {
	gotato.Header
	Instance string `json:"instance"`
}

// SessionDeleted is published when the Session's Record is deleted. An Instance
// Event.
type SessionDeleted struct{ gotato.Header }

// SessionLoaded is published with how long the store took to load the Session.
// An Instance Event.
type SessionLoaded struct {
	gotato.Header
	Messages  int     `json:"messages"`
	ElapsedMS float64 `json:"elapsed_ms"`
	Failed    bool    `json:"failed,omitempty"`
}

// SessionSaved is published with how long the store took to write a Run's
// steps, summed, and how many writes that was. An Instance Event.
type SessionSaved struct {
	gotato.Header
	Messages  int     `json:"messages"`
	ElapsedMS float64 `json:"elapsed_ms"`
	Writes    int     `json:"writes"`
	Failed    bool    `json:"failed,omitempty"`
}

// StoreWriteFailed is published when a write fails, for another reason than a
// taken position. An Instance Event; the store's own error text stays out,
// because Events reach clients.
type StoreWriteFailed struct {
	gotato.Header
	Code gotato.ErrorCode `json:"code,omitempty"`
}

// Kind implements gotato.Event.
func (RunRequested) Kind() gotato.EventKind { return EventKindRunRequested }

// Class implements gotato.Event.
func (RunRequested) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (RunWaiting) Kind() gotato.EventKind { return EventKindRunWaiting }

// Class implements gotato.Event.
func (RunWaiting) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (RunRejected) Kind() gotato.EventKind { return EventKindRunRejected }

// Class implements gotato.Event.
func (RunRejected) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (SessionFenced) Kind() gotato.EventKind { return EventKindSessionFenced }

// Class implements gotato.Event.
func (SessionFenced) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (LivenessLapsed) Kind() gotato.EventKind { return EventKindLivenessLapsed }

// Class implements gotato.Event.
func (LivenessLapsed) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (SessionDeleted) Kind() gotato.EventKind { return EventKindSessionDeleted }

// Class implements gotato.Event.
func (SessionDeleted) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (SessionLoaded) Kind() gotato.EventKind { return EventKindSessionLoaded }

// Class implements gotato.Event.
func (SessionLoaded) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (SessionSaved) Kind() gotato.EventKind { return EventKindSessionSaved }

// Class implements gotato.Event.
func (SessionSaved) Class() gotato.EventClass { return gotato.EventClassInstance }

// Kind implements gotato.Event.
func (StoreWriteFailed) Kind() gotato.EventKind { return EventKindStoreWriteFailed }

// Class implements gotato.Event.
func (StoreWriteFailed) Class() gotato.EventClass { return gotato.EventClassInstance }
