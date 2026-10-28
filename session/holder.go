package session

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/jinhuang712/gotato/gotato"
)

// Claim names the Instance that writes a Record, and its Epoch, which grows
// by one with every new Claim. A Claim is itself an Entry, at the position
// after the head its Instance read. So a writer that lost its Claim always
// writes to a position that is already taken, and the store refuses it in
// the same write.
type Claim struct {
	Instance string `json:"instance"`
	Epoch    uint64 `json:"epoch"`
}

// Claim returns the Record's writer as its facts last named it.
func (s *Record) Claim() Claim {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claim
}

// claimFor makes instance the Record's writer, as a fact. A new Claim takes
// the next Epoch; an Instance that holds the Record already keeps its own.
func (s *Record) claimFor(instance string, why gotato.ClaimWhy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.claim.Instance
	if from != instance || s.claim.Epoch == 0 {
		s.claim = Claim{Instance: instance, Epoch: s.claim.Epoch + 1}
	}
	s.instance = instance
	s.touch()
	s.emit(gotato.SessionClaimed{Header: s.header(), Instance: instance, Epoch: s.claim.Epoch, Why: why, From: from})
}

// Holder reports who holds the Record: the Claim it was last written under,
// and whether that holder is using it, with a Run or a change under way (the
// Record is busy until its writer releases it). A Record that was never
// claimed returns an empty Claim.
func (s *Record) Holder() (claim Claim, busy bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.claim, s.busy || s.running != ""
}

// RecordEpoch returns the Record's identity as a log: chosen when the Record
// is created, the same on every load and on every Instance, and different only
// when the Record is deleted and created again under the same ID. A stored
// Event's number means the same Event only within one epoch, so a client that
// resumes a stream from a number sends the epoch it saw.
func (s *Record) RecordEpoch() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch
}

// newEpoch returns a random identity for a new Record.
func newEpoch() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}
