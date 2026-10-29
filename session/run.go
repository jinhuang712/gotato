package session

import (
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// RunInfo is the per-Run record kept in a Record: what its RunStarted and
// RunEnded Events say.
type RunInfo struct {
	RunID      gotato.RunID     `json:"run_id"`
	Status     gotato.RunStatus `json:"status"`
	Error      string           `json:"error,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	EndedAt    time.Time        `json:"ended_at,omitzero"`
	Turns      uint32           `json:"turns"`
	ToolCalls  uint32           `json:"tool_calls"`
	Usage      gotato.Usage     `json:"usage"`
	StopReason string           `json:"stop_reason,omitempty"`
	// RequestID is the caller's identifier of the request that started the
	// Run (RunStarted.RequestID).
	RequestID string `json:"request_id,omitempty"`
}

func runStarted(e gotato.RunStarted) RunInfo {
	return RunInfo{RunID: e.RunID, Status: gotato.RunStatusRunning, StartedAt: e.Time.UTC(), RequestID: e.RequestID}
}

func runEnded(e gotato.RunEnded) RunInfo {
	return RunInfo{
		RunID: e.RunID, Status: e.Status, Error: string(e.Cause), StopReason: e.StopReason,
		EndedAt: e.Time.UTC(), Turns: e.Turns, ToolCalls: e.ToolCalls, Usage: e.Usage,
	}
}

// Runs returns the Run records.
func (s *Record) Runs() []RunInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.runs)
}

// LastRunEnd returns when the last Run that ended did: the Session's last busy
// time. It is the zero time when no Run has ended. A Record loaded from its
// facts reads it from the RecordInfo, so it does not need the log.
func (s *Record) LastRunEnd() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastRunEnd
}

// endRun finalizes the Run record with run's RunID, or adds run when there is
// none, and adds its usage to the Record's. The caller holds the lock.
func (s *Record) endRun(run RunInfo) {
	found := false
	for i, old := range slices.Backward(s.runs) {
		if old.RunID != run.RunID {
			continue
		}
		run.StartedAt = old.StartedAt
		run.RequestID = old.RequestID
		s.runs[i] = run
		found = true
		break
	}
	if !found {
		s.runs = append(s.runs, run)
	}
	if s.running == run.RunID {
		s.running = ""
	}
	if run.EndedAt.After(s.lastRunEnd) {
		s.lastRunEnd = run.EndedAt
	}
	s.usage.InputTokens += run.Usage.InputTokens
	s.usage.OutputTokens += run.Usage.OutputTokens
	s.usage.TotalTokens += run.Usage.TotalTokens
	s.usage.CacheReadTokens += run.Usage.CacheReadTokens
	s.usage.CacheWriteTokens += run.Usage.CacheWriteTokens
}

// interruptRun ends a Run that started and never ended, because the writer
// that ran it stopped, as failed with "interrupted".
func (s *Record) interruptRun(runID gotato.RunID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	known := false
	for _, run := range s.runs {
		known = known || run.RunID == runID
	}
	if !known && s.baseRuns > 0 {
		// The Run is counted in baseRuns already; ending it adds its record.
		s.baseRuns--
	}
	header := s.header()
	header.RunID = runID
	ended := gotato.RunEnded{Header: header, Status: gotato.RunStatusFailed, Cause: "interrupted"}
	s.endRun(runEnded(ended))
	s.touch()
	s.emit(ended)
}

// Usage returns the aggregated usage across recorded Runs.
func (s *Record) Usage() gotato.Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usage
}
