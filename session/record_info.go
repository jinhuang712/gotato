package session

import (
	"maps"
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Info returns the listing view without copying Messages or Events.
func (s *Record) Info() RecordInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.infoLocked()
}

func (s *Record) infoLocked() RecordInfo {
	summary := infoOf(s.id, s.parentID, s.createdAt, s.updatedAt, len(s.messages), s.baseRuns+len(s.runs), s.usage, s.metadata)
	summary.RecordEpoch = s.epoch
	summary.Seq = s.head
	summary.Running = s.running
	summary.Busy = s.busy
	summary.Notices = slices.Clone(s.notices)
	if !s.lastRunEnd.IsZero() {
		summary.LastRunEnd = s.lastRunEnd
	}
	return summary
}

func infoOf(id, parentID string, createdAt, updatedAt time.Time, messages, runs int, usage gotato.Usage, metadata map[string]string) RecordInfo {
	return RecordInfo{
		ID:        id,
		ParentID:  parentID,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Messages:  messages,
		Runs:      runs,
		Usage:     usage,
		Metadata:  maps.Clone(metadata),
	}
}
