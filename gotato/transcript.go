package gotato

import (
	"slices"
	"sync"
)

// Transcript is the committed history a Session runs against: it is the
// Session's Record of what happened. The Session appends every committed
// Message to it and reads it back when it builds the next Context.
//
// The Session goroutine is the only writer while a Run is in flight. A
// Transcript may be shared between successive Runs and between Sessions, but it
// must not be mutated concurrently with a Run that is using it.
//
// Messages returns the committed Messages in order. The Session treats the
// returned slice as read-only and never mutates it. Len reports the committed
// Message count without materializing the history, so a Session can enforce its
// Message bound without copying.
type Transcript interface {
	Len() int
	Messages() []Message
	Append(Message) error
}

// memoryTranscript is the default Transcript: a private slice that lives and
// dies with the Session. It is safe to read while a Run appends to it, since
// Session.Transcript hands it to callers; Messages returns a copy of the
// slice.
type memoryTranscript struct {
	mu       sync.RWMutex
	messages []Message
}

func (t *memoryTranscript) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.messages)
}

func (t *memoryTranscript) Messages() []Message {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return slices.Clone(t.messages)
}

func (t *memoryTranscript) Append(message Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messages = append(t.messages, message)
	return nil
}

// WithTranscript makes the Session commit to an external Transcript, such as
// the session.Record it is loaded from, instead of a private in-memory one.
//
// A Transcript may have optional capabilities, each found by a type assertion
// on the value given here, so a wrapper must forward them all (embedding the
// concrete type does; embedding the Transcript interface does not):
//
//	ID() string                 the Session takes the Record's ID
//	Journal                     Events are stored, numbered by the Record
//	Durable() bool              (of the Journal) whether they survive the process
//	SpillKeeper                 a large Tool Result is spilled, and read back
//	EventSource                 a Subscription catches up from the Record
//	LoadedToolSource            Tools a Run loaded are restored
//
// Without a Journal the Session numbers its Events in memory and stores none
// (service.Runner does this on purpose for a Run that is not saved), so a
// wrapper that hides Write makes the Session non-durable without an error.
func WithTranscript(transcript Transcript) SessionOption {
	return func(c *sessionConfig) error {
		if transcript == nil {
			return runtimeError(CodeInvalidArgument, "WithTranscript", "transcript is nil", nil)
		}
		c.transcript = transcript
		return nil
	}
}

// Transcript returns the Transcript this Session commits to.
func (a *Session) Transcript() Transcript { return a.transcript }
