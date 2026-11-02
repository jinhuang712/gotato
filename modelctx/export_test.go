package modelctx

import "github.com/jinhuang712/gotato/gotato"

// The names below left the public API; the external tests reach them here.

var (
	MergeDigest            = mergeDigest
	MergeOldest            = mergeOldest
	SegmentsBudget         = segmentsBudget
	MergeRecapAsk          = mergeRecapAsk
	MergeRecapModelRequest = mergeRecapModelRequest
)

// ResultTool and SegmentTool are the tools of a ReadBack, whatever its
// Originals lists.
func ResultTool(r ReadBack) gotato.Tool  { return r.resultTool() }
func SegmentTool(r ReadBack) gotato.Tool { return r.segmentTool() }

// EffectiveBudget is CompactPolicy.effectiveBudget.
func (p CompactPolicy) EffectiveBudget() int { return p.effectiveBudget() }
