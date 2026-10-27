package session

import (
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The names below left the public API; the external tests reach them here.

var (
	EventOf     = eventOf
	FromEntries = fromEntries
	LayerOf     = layerOf
	ReadSpilled = readSpilled
)

// CloseSegment is Record.closeSegment.
func (s *Record) CloseSegment(segment gotato.Segment) (gotato.Segment, error) {
	return s.closeSegment(segment)
}

// CheckpointSegments is Record.checkpointSegments.
func (s *Record) CheckpointSegments(why gotato.CompactionWhy, numbers []int, tail []gotato.Message, compaction *Compaction) error {
	return s.checkpointSegments(why, numbers, tail, compaction)
}

// StartLog turns r's log on, as storing r does.
func StartLog(r *Record) { r.startLog() }

// TakePending returns r's pending Entries and marks them stored.
func TakePending(r *Record) []Entry {
	entries, _, _, err := r.takePending()
	if err != nil {
		panic(err)
	}
	if len(entries) > 0 {
		r.stored(entries[len(entries)-1].Seq)
	}
	return entries
}

// SetBeforeWrite makes f call fn at the start of every write of a batch, after
// Append has checked its writer.
func SetBeforeWrite(f *FileStore, fn func()) { f.beforeWrite = fn }

// DefaultEventLimit is the number of Events a Record keeps in memory.
const DefaultEventLimit = defaultEventLimit

// WithEventLimit bounds the Events kept in memory. Zero keeps
// DefaultEventLimit; a negative value keeps none.
func WithEventLimit(limit int) Option {
	return func(s *Record) {
		if limit == 0 {
			limit = defaultEventLimit
		}
		s.eventLimit = limit
	}
}

// WithClock overrides the Record's clock.
func WithClock(now func() time.Time) Option { return func(s *Record) { s.now = now } }
