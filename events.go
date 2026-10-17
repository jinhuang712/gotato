package gotato

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type EventClass string

const (
	EventProtected   EventClass = "protected"
	EventCoalescable EventClass = "coalescable"
)

type EventKind string

const (
	EventRunStart            EventKind = "run_start"
	EventTurnStart           EventKind = "turn_start"
	EventMessageStart        EventKind = "message_start"
	EventMessageUpdate       EventKind = "message_update"
	EventMessageEnd          EventKind = "message_end"
	EventToolExecutionStart  EventKind = "tool_execution_start"
	EventToolExecutionUpdate EventKind = "tool_execution_update"
	EventToolExecutionEnd    EventKind = "tool_execution_end"
	EventToolResultCommitted EventKind = "tool_result_committed"
	EventToolSetActivated    EventKind = "toolset_activated"
	EventTurnEnd             EventKind = "turn_end"
	EventRunEnd              EventKind = "run_end"
	// EventContextBuilt is emitted once per Turn, after the ContextBuilder
	// and context Extensions ran and before the Model request is sent.
	EventContextBuilt EventKind = "context_built"
	// EventSessionCompacted is recorded when a Session prefix was replaced by
	// a summary. It is produced by the compaction operation, not by the Loop.
	EventSessionCompacted EventKind = "session_compacted"
	// EventSessionLoaded and EventSessionSaved time a store load and save.
	// The service Runner emits them for each load and save a Run makes:
	// session_loaded before run_start, and session_saved after run_end.
	// They reach the same places as the Loop's Events but are not recorded
	// in the Session. An Event emitted before the Run starts has a
	// SessionID, but no RunID and sequence 0.
	EventSessionLoaded EventKind = "session_loaded"
	EventSessionSaved  EventKind = "session_saved"
)

// Payload keys by Kind. Consumers must tolerate unknown keys and kinds.
//
//	run_start               (none)
//	run_end                 status, error?, stopped_by_extension?, stop_reason?
//	turn_start              (none)
//	turn_end                stop_reason, summary{elapsed_ms,text_bytes,reasoning_bytes,tool_calls,input_tokens,output_tokens,total_tokens,
//	                        cache_read_tokens?,cache_write_tokens?,tool_results?}
//	context_built           strategy, messages, source_messages, selected_messages, prefix_hash, prefix_messages,
//	                        system_bytes, tools, metadata?{the ContextBuilder's Metadata, as strings}
//	message_start           role, source? (steer|follow_up)
//	message_update          text
//	message_end             role?, tool_calls?
//	tool_execution_start    tool_id
//	tool_execution_update   text
//	tool_execution_end      status, executed
//	tool_result_committed   status
//	toolset_activated       toolset
//	session_compacted       replaced_messages, summary_message_id, summarizer
//	session_loaded          messages, elapsed_ms (fractional), failed?
//	session_saved           messages, elapsed_ms (fractional), failed?

type Event struct {
	SessionID  SessionID      `json:"session_id"`
	RunID      RunID          `json:"run_id"`
	Sequence   uint64         `json:"sequence"`
	Kind       EventKind      `json:"kind"`
	Class      EventClass     `json:"event_class"`
	Turn       TurnNumber     `json:"turn,omitempty"`
	MessageID  MessageID      `json:"message_id,omitempty"`
	ToolCallID ToolCallID     `json:"tool_call_id,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
}

type LifecycleKind string

const (
	LifecycleSessionCreated LifecycleKind = "session_created"
	LifecycleSessionClosing LifecycleKind = "session_closing"
	LifecycleSessionClosed  LifecycleKind = "session_closed"
)

type LifecycleEvent struct {
	Kind      LifecycleKind `json:"kind"`
	SessionID SessionID     `json:"session_id"`
	Reason    string        `json:"reason,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

type EventStream interface {
	Next(context.Context) (Event, error)
	Close() error
}

type LifecycleStream interface {
	Next(context.Context) (LifecycleEvent, error)
	Close() error
}

type eventHub struct {
	mu     sync.Mutex
	nextID uint64
	subs   map[uint64]*eventSubscription
	closed bool
}

type eventSubscription struct {
	mu      sync.Mutex
	ch      chan Event
	done    chan struct{}
	closed  bool
	err     error
	closeFn func(*eventSubscription)
}

func newEventHub() *eventHub { return &eventHub{subs: make(map[uint64]*eventSubscription)} }

func (h *eventHub) subscribe(ctx context.Context) (EventStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &eventSubscription{ch: make(chan Event, 128), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, io.EOF
	}
	h.nextID++
	id := h.nextID
	h.subs[id] = s
	s.closeFn = func(sub *eventSubscription) {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
	h.mu.Unlock()
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				s.closeWith(ctx.Err())
			case <-s.done:
			}
		}()
	}
	return s, nil
}

func (h *eventHub) publish(ev Event) {
	h.mu.Lock()
	subs := make([]*eventSubscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.enqueue(ev)
	}
}

func (h *eventHub) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*eventSubscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.subs = make(map[uint64]*eventSubscription)
	h.mu.Unlock()
	for _, s := range subs {
		s.closeWith(nil)
	}
}

func (s *eventSubscription) enqueue(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
		return
	default:
	}
	if ev.Class == EventCoalescable {
		// Progress is optional; drop it rather than displacing state.
		return
	}
	// A protected event must not be lost to a backlog of coalescable progress.
	// Make room by discarding the oldest coalescable event; only when the
	// buffer holds nothing but protected events does the subscription end.
	buffered := make([]Event, 0, cap(s.ch)+1)
	for {
		select {
		case queued := <-s.ch:
			buffered = append(buffered, queued)
		default:
			buffered = append(buffered, ev)
			if len(buffered) > cap(s.ch) {
				drop := -1
				for i, queued := range buffered {
					if queued.Class == EventCoalescable {
						drop = i
						break
					}
				}
				if drop < 0 {
					// Even without progress events the buffer is full: the
					// subscriber is too slow and the subscription ends.
					s.closed = true
					s.err = errors.New("gotato: protected event buffer full")
					close(s.ch)
					close(s.done)
					if s.closeFn != nil {
						s.closeFn(s)
					}
					return
				}
				buffered = append(buffered[:drop], buffered[drop+1:]...)
			}
			for _, pending := range buffered {
				s.ch <- pending
			}
			return
		}
	}
}

func (s *eventSubscription) closeWith(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if err != nil && !errors.Is(err, context.Canceled) {
		s.err = err
	}
	close(s.ch)
	close(s.done)
	closeFn := s.closeFn
	s.mu.Unlock()
	if closeFn != nil {
		closeFn(s)
	}
}

func (s *eventSubscription) Next(ctx context.Context) (Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case ev, ok := <-s.ch:
		if ok {
			return ev, nil
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return Event{}, err
		}
		return Event{}, io.EOF
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}

func (s *eventSubscription) Close() error { s.closeWith(nil); return nil }

type lifecycleHub struct {
	mu     sync.Mutex
	nextID uint64
	subs   map[uint64]*lifecycleSubscription
	closed bool
}

type lifecycleSubscription struct {
	mu      sync.Mutex
	ch      chan LifecycleEvent
	done    chan struct{}
	closed  bool
	err     error
	closeFn func(*lifecycleSubscription)
}

func newLifecycleHub() *lifecycleHub {
	return &lifecycleHub{subs: make(map[uint64]*lifecycleSubscription)}
}

func (h *lifecycleHub) subscribe(ctx context.Context) (LifecycleStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &lifecycleSubscription{ch: make(chan LifecycleEvent, 32), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, io.EOF
	}
	h.nextID++
	id := h.nextID
	h.subs[id] = s
	s.closeFn = func(sub *lifecycleSubscription) { h.mu.Lock(); delete(h.subs, id); h.mu.Unlock() }
	h.mu.Unlock()
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				s.closeWith(ctx.Err())
			case <-s.done:
			}
		}()
	}
	return s, nil
}

func (h *lifecycleHub) publish(ev LifecycleEvent) {
	h.mu.Lock()
	subs := make([]*lifecycleSubscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.enqueue(ev)
	}
}

func (h *lifecycleHub) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*lifecycleSubscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.subs = make(map[uint64]*lifecycleSubscription)
	h.mu.Unlock()
	for _, s := range subs {
		s.closeWith(nil)
	}
}

func (s *lifecycleSubscription) enqueue(ev LifecycleEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
	default:
		s.closed = true
		s.err = errors.New("gotato: lifecycle event buffer full")
		close(s.ch)
		close(s.done)
		if s.closeFn != nil {
			s.closeFn(s)
		}
	}
}

func (s *lifecycleSubscription) closeWith(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if err != nil && !errors.Is(err, context.Canceled) {
		s.err = err
	}
	close(s.ch)
	close(s.done)
	closeFn := s.closeFn
	s.mu.Unlock()
	if closeFn != nil {
		closeFn(s)
	}
}

func (s *lifecycleSubscription) Next(ctx context.Context) (LifecycleEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case ev, ok := <-s.ch:
		if ok {
			return ev, nil
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return LifecycleEvent{}, err
		}
		return LifecycleEvent{}, io.EOF
	case <-ctx.Done():
		return LifecycleEvent{}, ctx.Err()
	}
}

func (s *lifecycleSubscription) Close() error { s.closeWith(nil); return nil }
