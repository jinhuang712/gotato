package record

import (
	"maps"
)

// Info returns the listing view without copying Messages or Events.
func (s *Record) Info() Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.infoLocked()
}

// infoLocked is the listing columns: what a store may index, and nothing that
// a rebuild reads.
func (s *Record) infoLocked() Info {
	return Info{
		ID:        s.id,
		ParentID:  s.parentID,
		CreatedAt: s.createdAt,
		UpdatedAt: s.updatedAt,
		Messages:  len(s.messages),
		Runs:      s.baseRuns + len(s.runs),
		Usage:     s.usage,
		Metadata:  maps.Clone(s.metadata),
	}
}
