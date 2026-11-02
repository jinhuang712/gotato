package record

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jinhuang712/gotato/gotato"
)

// The functions of this file are how a writer drives a Record and rebuilds
// one: the store package's Writer is their caller. A Record holds what it
// needs to be saved (its pending Entries, its Claim, its Header) and knows
// nothing of where it is saved to; the writer does the writing, and tells the
// Record what the store now holds.

// StartWriting readies a Record that was never stored for its first write: it
// turns the log on, so that the Record's state is its first Event
// (session_created, a Checkpoint), and claims the Record for instance as a new
// Session. The Record is busy until Release, unless idle: nothing runs against
// it, and its release is queued with its first Claim, so another Instance may
// claim it at once.
func (s *Record) StartWriting(instance string, idle bool) {
	s.mu.Lock()
	s.instance = instance
	s.claim = Claim{}
	s.mu.Unlock()
	s.startLog()
	s.claimFor(instance, gotato.ClaimWhyNewSession)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = true
	if idle {
		s.busy = false
		s.emit(gotato.SessionReleased{Header: s.header(), Instance: s.claim.Instance, Epoch: s.claim.Epoch})
	}
}

// Attach makes the Record instance's, with its log on, and returns who held
// the Claim, whether that holder was using the Session (busy, or a Run not
// ended), and the Run that started and never ended, if any.
func (s *Record) Attach(instance string) (holder Claim, busy bool, running gotato.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instance = instance
	s.logging = true
	return s.claim, s.busy || s.running != "", s.running
}

// TakeOver claims the Record for instance, and settles what the last writer
// left: the Cut-off replies and the Run that never ended (running, from
// Attach).
//
// The Session is busy from this write on, so another Instance waits instead of
// claiming it before this writer's Run has started. A writer that holds the
// Claim already writes it again: it takes the next position too, so when
// another Instance claims the Record at the same moment, exactly one of the
// two writes lands, and the other sees a conflict before it calls the model.
func (s *Record) TakeOver(instance string, why gotato.ClaimWhy, running gotato.RunID) {
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	s.claimFor(instance, why)
	// A reply that was streaming when the last writer stopped is kept as the
	// Cut-off reply it had reached, as a fact, so every later load has it.
	s.mu.Lock()
	for _, message := range s.cutOff {
		s.emit(gotato.ReplyCutOff{Header: s.header(), Message: message.Clone(), Cause: "interrupted"})
	}
	s.cutOff = nil
	s.mu.Unlock()
	if running != "" {
		// A message the dead writer accepted as queued, and did not commit,
		// is gone with it: say so before the Run ends.
		s.mu.Lock()
		s.dropQueued("writer_gone")
		s.mu.Unlock()
		s.interruptRun(running)
	}
}

// Reclaim claims the Record again for another Run, by the Instance that
// holds it, with the same Epoch, and marks it busy until Release. A writer
// that released the Record and kept it in memory (a warm copy) does it.
func (s *Record) Reclaim() {
	s.mu.Lock()
	s.busy = true
	instance := s.instance
	s.mu.Unlock()
	s.claimFor(instance, gotato.ClaimWhyIdle)
}

// Release queues a session_released Event, so that the Session is no longer
// busy: another Instance may then claim it without waiting. A Record that is
// not busy queues nothing.
func (s *Record) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		s.busy = false
		s.emit(gotato.SessionReleased{Header: s.header(), Instance: s.claim.Instance, Epoch: s.claim.Epoch})
	}
}

// View rebuilds a Record from its whole log, for reading only: its facts, and
// its Run records, usage and Events. The Entries must start at a Checkpoint
// and be in Seq order. A Record that View returns keeps no pending Events, so
// it is never written.
func View(entries []Entry) (*Record, error) {
	r, err := fromEntries(entries)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.logging = false
	r.mu.Unlock()
	return r, nil
}

// FromLoaded rebuilds a Record from one load of it: its facts, and its Run
// count, usage and what was in flight from the Header's State, since the Run
// records are in the log. When the Header's LogFrom is set, a Run started and
// never ended, and the load held the log from the last Checkpoint on too: it
// holds the parts of a reply that was streaming, which become a Cut-off reply,
// so a crash loses at most the last part. It all comes from the one read, so
// there is nothing for it to disagree with.
func FromLoaded(loaded Loaded) (*Record, error) {
	var state recordState
	if len(loaded.Header.State) > 0 {
		if err := json.Unmarshal(loaded.Header.State, &state); err != nil {
			return nil, fmt.Errorf("record: the state of the Record: %w", err)
		}
	}
	r, err := rebuildRecord(loaded.Entries, false, loaded.Segments)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if loaded.Header.LogFrom != 0 {
		// The log was read for its parts only: its Runs are in the state.
		r.runs, r.eventHead = nil, 0
	}
	r.head = loaded.Seq
	r.last = loaded.Seq
	r.checkpointSeq = loaded.Entries[0].Seq
	// A load that starts at a compaction_ended Checkpoint has not read
	// session_created: what it held comes from the Header.
	if r.createdAt.IsZero() {
		r.createdAt = loaded.Header.Info.CreatedAt
	}
	if r.parentID == "" {
		r.parentID = loaded.Header.Info.ParentID
	}
	if r.epoch == "" {
		r.epoch = loaded.Header.Epoch
	}
	r.baseRuns = state.Runs
	r.usage = state.Usage
	r.running = state.Running
	r.busy = state.Busy
	r.notices = slices.Clone(state.Notices)
	if !state.LastRunEnd.IsZero() {
		r.lastRunEnd = state.LastRunEnd.UTC()
	}
	r.logging = false
	r.retainRead = true
	return r, nil
}
