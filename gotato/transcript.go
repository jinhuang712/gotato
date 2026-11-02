package gotato

import (
	"slices"
	"strings"
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
//
// Capabilities says what else the Transcript can do. It is a method every
// Transcript has, not a set of interfaces found by assertion, so a wrapper that
// forgets one is not a Session that quietly stores less: an embedding wrapper
// forwards it, and any other writes the declaration in code.
type Transcript interface {
	Len() int
	Messages() []Message
	Append(Message) error
	Capabilities() Capabilities
}

// Capabilities is what a Transcript declares it can do. The zero value is a
// Transcript that stores nothing: a Session over it numbers its Events in
// memory, keeps none, and commits a large Tool Result whole.
//
// A Session over a Transcript that keeps its Events needs the parts that go with
// them, so NewSession refuses a Capabilities that sets Durable without Journal,
// Events and Loaded: a Session that stores must be able to number, read back and
// restore what it stores.
type Capabilities struct {
	// ID is the Session's ID when not empty, as a session.Record's is; the
	// Session generates one otherwise.
	ID string
	// Journal keeps the Session's Events, numbered, as the Entries of its
	// Record. Nil means the Session numbers them in memory and stores none.
	Journal Journal
	// Durable says the Events the Journal keeps are stored, so they survive
	// the process, and a Session writes the parts of a streaming reply, which
	// exist so that a crash loses at most the last of them. It may change
	// during the Transcript's life: a session.Record is not durable until it is
	// stored.
	Durable bool
	// Spill says the Journal keeps the Entries it writes, so the body of a
	// spilled Tool Result can be read back. A Session spills only over a
	// Transcript that declares it.
	Spill bool
	// Events lets a Subscription that fell behind catch up from the Record.
	Events EventSource
	// Loaded restores the Tools a Run loaded.
	Loaded LoadedToolSource
}

// check refuses a declaration that stores without what a Session needs of a
// store.
func (c Capabilities) check() error {
	if !c.Durable {
		return nil
	}
	var missing []string
	if c.Journal == nil {
		missing = append(missing, "Journal")
	}
	if c.Events == nil {
		missing = append(missing, "Events")
	}
	if c.Loaded == nil {
		missing = append(missing, "Loaded")
	}
	if len(missing) == 0 {
		return nil
	}
	return runtimeError(CodeInvalidArgument, "WithTranscript", "the Transcript declares Durable without "+strings.Join(missing, ", "), nil)
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

// Capabilities declares nothing: a Session over the default Transcript stores
// nothing.
func (t *memoryTranscript) Capabilities() Capabilities { return Capabilities{} }
