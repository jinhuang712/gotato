package record

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
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

// recordState is Gotato's own bytes in Header.State: what a Record loaded from
// its facts needs and the facts do not hold, since the Run records and the
// notices they came from are log Entries. No store reads it.
type recordState struct {
	// Running is the Run that started and has not ended, if any.
	Running gotato.RunID `json:"running,omitempty"`
	// Busy says the writer opened the Session and has not released it: a Run
	// or a change is under way, also before its Run has started.
	Busy bool `json:"busy,omitempty"`
	// LastRunEnd is when the last Run that ended did, zero when none has
	// (Record.LastRunEnd).
	LastRunEnd time.Time `json:"last_run_end,omitzero"`
	// Notices are the notices queued and neither committed nor dropped
	// (Record.PendingNotices), so that the next Run on any instance can queue
	// them again.
	Notices []gotato.PendingNotice `json:"notices,omitempty"`
	// Runs and Usage are the counts a load starts from: the Runs and the
	// usage of the Record up to this write.
	Runs  int          `json:"runs,omitempty"`
	Usage gotato.Usage `json:"usage"`
}

// headerLocked is the Header the store keeps once the pending Entries are
// written: the epoch, the listing columns, the state, and from where a load
// reads the log too, which it does while a Run is in flight.
func (s *Record) headerLocked() (Header, error) {
	state, err := json.Marshal(recordState{
		Running:    s.running,
		Busy:       s.busy,
		LastRunEnd: s.lastRunEnd,
		Notices:    slices.Clone(s.notices),
		Runs:       s.baseRuns + len(s.runs),
		Usage:      s.usage,
	})
	if err != nil {
		return Header{}, err
	}
	h := Header{Epoch: s.epoch, Info: s.infoLocked(), State: state}
	if s.running != "" {
		h.LogFrom = s.checkpointSeq
	}
	return h, nil
}
