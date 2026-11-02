package record

import (
	"context"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

const (
	// DefaultPageBytes is the page size of a ReadBack that sets none: the most
	// one page holds, in bytes, about 8K tokens. It is the line past which a
	// Tool Result is spilled and the size of a page of read_result and
	// read_segment.
	DefaultPageBytes = 32 << 10
	// minPageBytes is the least a page may hold.
	minPageBytes = 256
)

// ReadBack is the default gotato.ReadBack: it offers the two tools that read
// back what compaction and the spill moved out of the Context, and owns the
// page size they and the spill share. Install it with gotato.WithReadBack.
//
//	gotato.WithReadBack(record.ReadBack{Originals: record.RecordOriginals(record, store.Records)})
//
// The tools are fixed, since a Segment already written names them: read_result
// (ref.ReadResult) returns the original of a ref, a Tool Result or a Message,
// page by page, and read_segment (ref.ReadSegment) returns one Segment's
// Messages word for word. Their IDs, the paging, the escaping, the read Effect
// and the wording belong to ReadBack; where the originals are belongs to
// Originals.
type ReadBack struct {
	// Originals finds the originals. It is required.
	Originals Originals
	// PageSize is the most one page holds, in bytes: the spill threshold, the
	// pages the preview counts and the pages of both tools. Zero means
	// DefaultPageBytes, and one below 256 is 256.
	PageSize int
	// Location is the time zone of the times read_segment shows; nil means UTC.
	Location *time.Location
}

// PageBytes implements gotato.ReadBack: PageSize, the one page size of the
// spill and the read tools.
func (r ReadBack) PageBytes() int {
	if r.PageSize <= 0 {
		return DefaultPageBytes
	}
	return max(r.PageSize, minPageBytes)
}

// Tools implements gotato.ToolSource. It always offers read_result, so the
// tool list does not change on the Turn a Tool Result is first spilled, and
// read_segment once the Context holds a Segment, which Originals says by also
// listing them (Segments() []gotato.Segment, as OriginalsOf does). An
// Originals that does not list them gets read_segment from the start.
func (r ReadBack) Tools(context.Context) ([]gotato.Tool, error) {
	if r.Originals == nil {
		return nil, gotato.ErrorOf(gotato.CodeInvalidArgument, "record: ReadBack has no Originals")
	}
	tools := []gotato.Tool{r.resultTool()}
	if segments, listed := r.segments(); !listed || len(segments) > 0 {
		tools = append(tools, r.segmentTool())
	}
	return tools, nil
}

// segments lists the Segments of the Context when Originals can.
func (r ReadBack) segments() (segments []gotato.Segment, listed bool) {
	lister, ok := r.Originals.(interface{ Segments() []gotato.Segment })
	if !ok {
		return nil, false
	}
	return lister.Segments(), true
}
