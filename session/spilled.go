package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/jinhuang712/gotato/gotato"
)

// defaultSpillCacheBytes is how much of the bodies of spilled results a live
// Record keeps in memory once they are stored: the rest is read from the
// store when the model asks. A body that is not stored yet, or whose Record
// has no store, is never dropped, since it is the only copy.
const defaultSpillCacheBytes = 16 << 20

// spillCache holds the bodies a live Record has seen, by the position of the
// Entry that holds each.
type spillCache struct {
	mu     sync.Mutex
	bodies map[uint64]string
	order  []uint64
	bytes  int
}

// KeepsSpilledResults implements gotato.SpillKeeper: a Record's Entries are
// kept by its store, so the body of a spilled result can be read back.
func (s *Record) KeepsSpilledResults() bool { return true }

// keepSpilled caches the body of a ToolResultSpilled Event the Record numbered.
func (s *Record) keepSpilled(event gotato.Event) {
	if spilled, ok := event.(gotato.ToolResultSpilled); ok {
		s.spilled.put(event.Head().Number, spilled.Body)
	}
}

func (c *spillCache) put(seq uint64, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.bodies[seq]; ok {
		return
	}
	if c.bodies == nil {
		c.bodies = map[uint64]string{}
	}
	c.bodies[seq] = body
	c.order = append(c.order, seq)
	c.bytes += len(body)
}

func (c *spillCache) get(seq uint64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, ok := c.bodies[seq]
	return body, ok
}

// trim drops the oldest bodies that the store holds, until the cache fits.
func (c *spillCache) trim(stored uint64, limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.order[:0]
	for _, seq := range c.order {
		if c.bytes > limit && seq <= stored {
			c.bytes -= len(c.bodies[seq])
			delete(c.bodies, seq)
			continue
		}
		kept = append(kept, seq)
	}
	c.order = kept
}

// SpilledResults lists the spilled results the Context holds, by Tool call, in
// the order they were committed.
func (s *Record) SpilledResults() (calls []gotato.ToolCallID, byCall map[gotato.ToolCallID]gotato.SpilledResult) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byCall = map[gotato.ToolCallID]gotato.SpilledResult{}
	for _, message := range s.messages {
		if result := message.ToolResult; result != nil && result.Spilled != nil {
			if _, seen := byCall[result.CallID]; !seen {
				calls = append(calls, result.CallID)
			}
			byCall[result.CallID] = *result.Spilled
		}
	}
	return calls, byCall
}

// SpilledBody returns the full text of a spilled result: from the cache of this
// live Record, or else read from the Entry at spilled.Seq in log, the store of
// the Record (or of spilled.SessionID, when the result was inherited from a
// parent). pruned is true when the Entry is no longer there, which a team does
// when it prunes the log or deletes the parent. A body read is cached, within
// defaultSpillCacheBytes of the bodies the store holds.
func (s *Record) SpilledBody(ctx context.Context, log EntryLog, spilled gotato.SpilledResult) (body string, pruned bool, err error) {
	if from := string(spilled.SessionID); from != "" && from != s.ID() {
		// An Entry of another Record (a fork's parent): its position means
		// nothing in this Record's cache.
		if same, err := SameRecord(ctx, log, from, spilled.RecordEpoch); err != nil || !same {
			return "", err == nil, err
		}
		return readSpilled(ctx, log, from, spilled.Seq)
	}
	if body, ok := s.spilled.get(spilled.Seq); ok {
		return body, false, nil
	}
	body, pruned, err = readSpilled(ctx, log, s.ID(), spilled.Seq)
	if err != nil || pruned {
		return "", pruned, err
	}
	s.spilled.put(spilled.Seq, body)
	s.spilled.trim(s.Head(), defaultSpillCacheBytes)
	return body, false, nil
}

// FindSpilled finds the spilled result of a Tool call in the log of Record id,
// for a call whose Message the Context no longer holds because a Segment
// closed over it. ok is false when the Record has none. A model may use the same
// call ID in two Turns; the result is then the latest one, as in SpilledResults.
func FindSpilled(ctx context.Context, log EntryLog, id string, call gotato.ToolCallID) (spilled gotato.SpilledResult, ok bool, err error) {
	return findSpilled(ctx, log, id, call, 0)
}

// FindSpilled finds the spilled result of a Tool call that a Segment of the
// Record closed over: in the Record's own log first, then in the log of each
// source Session of its Segments, newest Segment first, within the Entries the
// Segment covers (a fork inherits Segments whose Entries are its parent's). The
// result names that Session in SessionID, so SpilledBody reads it there.
func (s *Record) FindSpilled(ctx context.Context, log EntryLog, call gotato.ToolCallID) (spilled gotato.SpilledResult, ok bool, err error) {
	spilled, ok, err = findSpilled(ctx, log, s.ID(), call, 0)
	if err != nil || ok {
		return spilled, ok, err
	}
	segments := s.Segments()
	for _, segment := range slices.Backward(segments) {
		sources := []gotato.SegmentSource{segment.Source}
		if len(segment.Merged) > 0 {
			sources = sources[:0]
			for _, merged := range slices.Backward(segment.Merged) {
				sources = append(sources, merged.Source)
			}
		}
		for _, source := range sources {
			from := string(source.SessionID)
			if from == "" || from == s.ID() {
				continue
			}
			if same, err := SameRecord(ctx, log, from, source.RecordEpoch); err != nil {
				return gotato.SpilledResult{}, false, err
			} else if !same {
				continue
			}
			spilled, ok, err = findSpilled(ctx, log, from, call, source.ToNumber)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return gotato.SpilledResult{}, false, err
			}
			if ok {
				spilled.SessionID, spilled.RecordEpoch = source.SessionID, source.RecordEpoch
				return spilled, true, nil
			}
		}
	}
	return gotato.SpilledResult{}, false, nil
}

// findSpilled searches the log of Record id for the latest spilled result of
// call, among the Entries up to maxSeq (all of them when it is 0).
func findSpilled(ctx context.Context, log EntryLog, id string, call gotato.ToolCallID, maxSeq uint64) (spilled gotato.SpilledResult, ok bool, err error) {
	entries, err := log.Log(ctx, id, 0, 0)
	if err != nil {
		return gotato.SpilledResult{}, false, err
	}
	for _, entry := range slices.Backward(entries) {
		if entry.Kind != gotato.EventKindToolResultSpilled || (maxSeq > 0 && entry.Seq > maxSeq) {
			continue
		}
		event, known, err := eventOf(entry, gotato.SessionID(id))
		if err != nil {
			return gotato.SpilledResult{}, false, err
		}
		if e, isSpilled := event.(gotato.ToolResultSpilled); known && isSpilled && e.CallID == call {
			return gotato.SpilledResult{Seq: entry.Seq, Bytes: e.Bytes}, true, nil
		}
	}
	return gotato.SpilledResult{}, false, nil
}

// readSpilled reads the body of the tool_result_spilled Entry at seq of Record
// id from log. pruned is true when the Entry is not there.
func readSpilled(ctx context.Context, log EntryLog, id string, seq uint64) (body string, pruned bool, err error) {
	if seq == 0 {
		return "", true, nil
	}
	entries, err := log.Log(ctx, id, seq-1, 1)
	if err != nil {
		return "", false, err
	}
	if len(entries) == 0 || entries[0].Seq != seq {
		return "", true, nil
	}
	if entries[0].Kind != gotato.EventKindToolResultSpilled {
		return "", false, fmt.Errorf("session: entry %d of %s is a %s Entry, not a tool_result_spilled one", seq, id, entries[0].Kind)
	}
	event, _, err := eventOf(entries[0], gotato.SessionID(id))
	if err != nil {
		return "", false, err
	}
	spilled, ok := event.(gotato.ToolResultSpilled)
	if !ok {
		return "", false, fmt.Errorf("session: entry %d of %s does not hold a tool_result_spilled Event", seq, id)
	}
	return spilled.Body, false, nil
}
