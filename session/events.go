package session

import (
	"context"
	"slices"

	"github.com/jinhuang712/gotato/gotato"
)

// defaultEventLimit bounds the Events a Record keeps in memory. Older Events
// are dropped first; Runs, Messages, and Usage are never dropped, and the
// store keeps every Event.
const defaultEventLimit = 1000

// Events returns the stored Events the Record keeps in memory, in order: the
// last defaultEventLimit of them. Events are values; do not modify what they
// point to.
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
