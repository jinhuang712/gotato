package record

import (
	"encoding/json"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// The names below left the public API; the external tests reach them here.

var (
	FromEntries = fromEntries
	LayerOf     = layerOf
	ReadSpilled = readSpilled

	MergeDigest            = mergeDigest
	MergeOldest            = mergeOldest
	SegmentsBudget         = segmentsBudget
	MergeRecapAsk          = mergeRecapAsk
	MergeRecapModelRequest = mergeRecapModelRequest
	LastTurnStart          = lastTurnStart
	CloneAll               = cloneAll
	CloneSegments          = cloneSegments
	TaggedPart             = taggedPart
	NumberAttr             = numberAttr
)

// RecordState is recordState: what a Header's State holds.
type RecordState = recordState

// DecodeState decodes the State of h.
func DecodeState(h Header) (RecordState, error) {
	var state recordState
	err := json.Unmarshal(h.State, &state)
	return state, err
}

// CloseWithoutCheckpoint is Record.closeSegment.
func (s *Record) CloseWithoutCheckpoint(segment gotato.Segment) (gotato.Segment, error) {
	return s.closeSegment(segment)
}

// CheckpointSegments is Record.checkpointSegments.
func (s *Record) CheckpointSegments(why gotato.CompactionWhy, numbers []int, tail []gotato.Message, compaction *gotato.Compaction) error {
	return s.checkpointSegments(why, numbers, tail, compaction)
}

// StartLog turns r's log on, as storing r does.
func StartLog(r *Record) { r.startLog() }

// TakePending returns r's pending Entries and marks them stored.
func TakePending(r *Record) []Entry {
	entries, _, _, err := r.TakePending()
	if err != nil {
		panic(err)
	}
	if len(entries) > 0 {
		r.Stored(entries[len(entries)-1].Seq)
	}
	return entries
}

// ChangeContextInPlace changes the first Part of the first Message of r's
// Context in place, and returns what the first Message of Segment number reads
// after that. ok is false when r holds no such Message or Segment.
func ChangeContextInPlace(r *Record, number int) (segmentText string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 || len(r.closed[number].Messages) == 0 {
		return "", false
	}
	r.messages[0].Parts[0].Text = "changed in place"
	return gotato.TextOf(r.closed[number].Messages[0]), true
}

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

// WithRecordClock overrides the Record's clock.
func WithRecordClock(now func() time.Time) Option {
	return func(s *Record) { s.now = now }
}

// Checkpoint is Record.checkpoint.
func (s *Record) Checkpoint(why gotato.CompactionWhy, messages []gotato.Message, compaction *gotato.Compaction) {
	s.checkpoint(why, messages, compaction)
}

// NotRunToolResult is notRunToolResult.
const NotRunToolResult = notRunToolResult

// ResultTool and SegmentTool are the tools of a ReadBack, whatever its
// Originals lists.
func ResultTool(r ReadBack) gotato.Tool  { return r.resultTool() }
func SegmentTool(r ReadBack) gotato.Tool { return r.segmentTool() }

// EffectiveBudget is CompactPolicy.effectiveBudget.
func (p CompactPolicy) EffectiveBudget() int { return p.effectiveBudget() }
