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
	EventAgentStart          EventKind = "agent_start"
	EventTurnStart           EventKind = "turn_start"
	EventMessageStart        EventKind = "message_start"
	EventMessageUpdate       EventKind = "message_update"
	EventMessageEnd          EventKind = "message_end"
	EventToolExecutionStart  EventKind = "tool_execution_start"
	EventToolExecutionUpdate EventKind = "tool_execution_update"
	EventToolExecutionEnd    EventKind = "tool_execution_end"
	EventToolResultCommitted EventKind = "tool_result_committed"
	EventTurnEnd             EventKind = "turn_end"
	EventAgentEnd            EventKind = "agent_end"
)

type Event struct {
	AgentID     AgentID        `json:"agent_id"`
	RunID       RunID          `json:"run_id"`
	Sequence    uint64         `json:"sequence"`
	Kind        EventKind      `json:"kind"`
	Class       EventClass     `json:"event_class"`
	Turn        TurnNumber     `json:"turn,omitempty"`
	MessageID   MessageID      `json:"message_id,omitempty"`
	ToolCallID  ToolCallID     `json:"tool_call_id,omitempty"`
	SpawnID     SpawnID        `json:"spawn_id,omitempty"`
	OriginRunID RunID          `json:"origin_run_id,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
}

type LifecycleKind string

const (
	LifecycleAgentCreated             LifecycleKind = "agent_created"
	LifecycleAgentRetirementRequested LifecycleKind = "agent_retirement_requested"
	LifecycleAgentClosing             LifecycleKind = "agent_closing"
	LifecycleAgentClosed              LifecycleKind = "agent_closed"
	LifecycleAgentRetirementFailed    LifecycleKind = "agent_retirement_failed"
)

type LifecycleEvent struct {
	Kind           LifecycleKind   `json:"kind"`
	AgentID        AgentID         `json:"agent_id"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Generation     AgentGeneration `json:"agent_generation,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
}

type EventStream interface {
	Next(context.Context) (Event, error)
	Close() error
}

type LifecycleStream interface {
	Next(context.Context) (LifecycleEvent, error)
	Close() error
}

type EventSource interface {
	Subscribe(context.Context) (EventStream, error)
}

type LifecycleSource interface {
	SubscribeLifecycle(context.Context) (LifecycleStream, error)
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
	closed  bool
	err     error
	closeFn func(*eventSubscription)
}

func newEventHub() *eventHub { return &eventHub{subs: make(map[uint64]*eventSubscription)} }

func (h *eventHub) subscribe(ctx context.Context) (EventStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &eventSubscription{ch: make(chan Event, 128)}
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
	go func() {
		<-ctx.Done()
		s.closeWith(ctx.Err())
	}()
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
	default:
		if ev.Class == EventCoalescable {
			return
		}
		s.closed = true
		s.err = errors.New("gotato: protected event buffer full")
		close(s.ch)
		if s.closeFn != nil {
			s.closeFn(s)
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
	s := &lifecycleSubscription{ch: make(chan LifecycleEvent, 32)}
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
	go func() { <-ctx.Done(); s.closeWith(ctx.Err()) }()
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
