package session

import (
	"fmt"
	"maps"
	"slices"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// Segments returns the Segments the Context starts with, in order, with the
// Messages that render them. They are the Segments the last Checkpoint uses;
// a Segment that was closed and left out of it (merged into a later one) is
// still in the log and is not listed.
func (s *Record) Segments() []gotato.Segment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.segmentsLocked()
}

func (s *Record) segmentsLocked() []gotato.Segment {
	if len(s.used) == 0 {
		return nil
	}
	out := make([]gotato.Segment, len(s.used))
	for i, number := range s.used {
		out[i] = s.closed[number].Clone()
	}
	return out
}

// NextSegment returns the number the next Segment closed in this Record gets.
func (s *Record) NextSegment() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxSegment + 1
}

func (s *Record) maxSegmentNumber() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxSegment
}

// prefixLen is how many of the Record's Messages the used Segments render.
// The caller holds the lock.
func (s *Record) prefixLen() int {
	n := 0
	for _, number := range s.used {
		n += len(s.closed[number].Messages)
	}
	return n
}

// Tail returns the Messages after the Segments' own: what the Session has
// committed since the last Checkpoint, and the Messages that Checkpoint kept.
// A Record with no Segments returns all its Messages.
func (s *Record) Tail() []gotato.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMessages(s.messages[min(s.prefixLen(), len(s.messages)):])
}

// Position says where the Record committed a Message since its last
// Checkpoint: the Entry's number and the time. ok is false for a Message the
// Record has no position for, such as one that came before the Checkpoint and
// was not kept, or one that is not stored yet.
func (s *Record) Position(id gotato.MessageID) (position gotato.MessagePosition, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	position, ok = s.positions[id]
	return position, ok
}

// positionTimes returns the positions a fork keeps: the times only, since an
// Entry number means something in the parent's Record alone.
func (s *Record) positionTimes() map[gotato.MessageID]gotato.MessagePosition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[gotato.MessageID]gotato.MessagePosition, len(s.positions))
	for id, position := range s.positions {
		position.Number = 0
		out[id] = position
	}
	return out
}

// inheritSegments makes the Segments of a parent's Context the fork's own.
// A Segment keeps its number, and names the parent's Session in its Source,
// unless it already names an earlier one. The caller has the Record to itself.
func (s *Record) inheritSegments(parentID string, segments []gotato.Segment, maxNumber int, positions map[gotato.MessageID]gotato.MessagePosition) {
	s.closed = map[int]gotato.Segment{}
	s.used = nil
	for _, segment := range segments {
		if segment.Source.SessionID == "" {
			segment.Source.SessionID = gotato.SessionID(parentID)
		}
		s.closed[segment.Number] = segment
		s.used = append(s.used, segment.Number)
	}
	s.maxSegment = maxNumber
	s.positions = positions
}

// CloseSegment records a Segment a compaction closed: it appends a
// segment_closed Event that holds the Segment once, with the Messages that
// render it, and returns the Segment as recorded. The Segment's Number is
// NextSegment when it is 0. The Segment is not part of the Context until a
// Checkpoint lists it (CheckpointSegments). Its Messages are never rewritten:
// the provider's prompt cache reads their bytes.
func (s *Record) CloseSegment(segment gotato.Segment) (gotato.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if segment.Number == 0 {
		segment.Number = s.maxSegment + 1
	}
	if _, taken := s.closed[segment.Number]; taken || segment.Number < 0 {
		return gotato.Segment{}, gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: segment %d is already closed", segment.Number))
	}
	segment = segment.Clone()
	segment.Rendered = len(segment.Messages)
	s.closeLocked(segment)
	s.touch()
	s.emit(gotato.SegmentClosed{Header: s.header(), Segment: segment.Clone()})
	return segment.Clone(), nil
}

func (s *Record) closeLocked(segment gotato.Segment) {
	s.closed[segment.Number] = segment
	s.maxSegment = max(s.maxSegment, segment.Number)
}

// CheckpointSegments replaces the Context with the Segments listed by number,
// which must be closed already (CloseSegment), followed by tail, and appends
// the compaction_ended Checkpoint that holds it. The Checkpoint stores the
// numbers and tail, not the Segments' Messages, which stay where
// CloseSegment wrote them. compaction is recorded when it is not nil. Nothing
// written before is rewritten, and it must not run while a Run is using the
// Record.
func (s *Record) CheckpointSegments(why gotato.CompactionWhy, numbers []int, tail []gotato.Message, compaction *Compaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prefix []gotato.Message
	for _, number := range numbers {
		segment, ok := s.closed[number]
		if !ok {
			return gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: segment %d is not closed", number))
		}
		prefix = append(prefix, cloneMessages(segment.Messages)...)
	}
	s.used = slices.Clone(numbers)
	s.messages = append(prefix, cloneMessages(tail)...)
	s.checkpointLocked(why, tail, compaction)
	return nil
}

// checkpointLocked records the Checkpoint of a Context whose Messages after
// the Segments are tail: it narrows the positions to the tail, records the
// compaction, and queues the Event. The caller holds the lock and has set
// messages and used.
func (s *Record) checkpointLocked(why gotato.CompactionWhy, tail []gotato.Message, compaction *Compaction) {
	kept := tailPositions(tail, s.positions)
	s.positions = map[gotato.MessageID]gotato.MessagePosition{}
	for _, position := range kept {
		s.positions[position.ID] = position
	}
	if compaction != nil {
		s.compactions = append(s.compactions, *compaction)
	}
	s.touch()
	s.emit(gotato.CompactionEnded{
		Header: s.header(), Instance: s.claim.Instance, Epoch: s.claim.Epoch, Why: why, Compaction: compaction, History: slices.Clone(s.compactions),
		Messages: cloneMessages(tail), Segments: slices.Clone(s.used), Tail: kept,
		Metadata: maps.Clone(s.metadata), ToolSets: slices.Clone(s.toolSets),
	})
}

// tailPositions lists the positions known for messages, in order.
func tailPositions(messages []gotato.Message, known map[gotato.MessageID]gotato.MessagePosition) []gotato.MessagePosition {
	var out []gotato.MessagePosition
	for _, message := range messages {
		if position, ok := known[message.ID]; ok && message.ID != "" {
			out = append(out, position)
		}
	}
	return out
}

// indexMessage notes where a Message was committed when event carries one.
// The caller holds the lock.
func (s *Record) indexMessage(event gotato.Event, at time.Time) {
	message, ok := messageOf(event)
	if !ok || message.ID == "" {
		return
	}
	s.positions[message.ID] = gotato.MessagePosition{ID: message.ID, Number: event.Head().Number, Time: at}
}

// messageOf returns the Message a commit Event carries.
func messageOf(event gotato.Event) (gotato.Message, bool) {
	switch e := event.(type) {
	case gotato.MessageCommitted:
		return e.Message, true
	case gotato.ReplyEnded:
		return e.Message, true
	case gotato.ReplyCutOff:
		return e.Message, true
	case gotato.ToolResultCommitted:
		return e.Message, true
	}
	return gotato.Message{}, false
}

// resolveSegments puts the rendered Messages of the Segments a Checkpoint
// listed in front of the Messages read since, once a load has seen every
// segment_closed Entry. The caller has the Record to itself.
func (s *Record) resolveSegments() error {
	if len(s.used) == 0 {
		return nil
	}
	var prefix []gotato.Message
	for _, number := range s.used {
		segment, ok := s.closed[number]
		if !ok {
			return gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: the checkpoint uses segment %d, which the log does not hold", number))
		}
		prefix = append(prefix, segment.Messages...)
	}
	s.messages = append(prefix, s.messages...)
	return nil
}
