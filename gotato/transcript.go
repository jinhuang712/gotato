package gotato

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
// dies with the Session.
type memoryTranscript struct {
	messages []Message
}

func (t *memoryTranscript) Len() int { return len(t.messages) }

func (t *memoryTranscript) Messages() []Message { return t.messages }

func (t *memoryTranscript) Append(message Message) error {
	t.messages = append(t.messages, message)
	return nil
}

// WithTranscript makes the Session commit to an external Transcript, such as
// the session.Record it is loaded from, instead of a private in-memory one.
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
