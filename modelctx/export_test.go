package modelctx

// The names below left the public API; the external tests reach them here.

var (
	MergeDigest            = mergeDigest
	MergeOldest            = mergeOldest
	SegmentsBudget         = segmentsBudget
	MergeRecapAsk          = mergeRecapAsk
	MergeRecapModelRequest = mergeRecapModelRequest
)

// EffectiveBudget is CompactPolicy.effectiveBudget.
func (p CompactPolicy) EffectiveBudget() int { return p.effectiveBudget() }
