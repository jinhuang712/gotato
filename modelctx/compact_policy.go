package modelctx

// defaultReserveTokens is the least room the default ceiling leaves in the
// window, for a reply and a Recap.
const defaultReserveTokens = 16000

// defaultCeiling is the ceiling a model's window gives: the window less the
// larger of a tenth of it and defaultReserveTokens, 180K for a 200K window. It
// is 0, no ceiling, when the window is unknown or too small to leave any.
func defaultCeiling(window int) int {
	if window <= 0 {
		return 0
	}
	return max(window-max(window/10, defaultReserveTokens), 0)
}

// CompactPolicy says when a Session compacts by itself. Past the ceiling, estimated
// in tokens, the Session is compacted before the model call that would send
// more; a request the provider refuses as too long is compacted once and sent
// again (AutoCompactor).
//
// CeilingTokens above 0 is the ceiling in force. At 0 it is
// defaultCeiling(WindowTokens), so a model whose window is known is compacted
// past it by default, and with no window there is no ceiling. A negative
// CeilingTokens turns the ceiling off. WindowTokens is the Model's context
// window, 0 when unknown. The kept tail is the latest Run's Turns, up to
// SegmentSettings.TailTokens (CompactConfig). DropReasoning is
// CompactConfig.DropReasoning for every compaction the policy makes.
type CompactPolicy struct {
	CeilingTokens int
	WindowTokens  int
	Recap         RecapWriter
	DropReasoning bool
	// SegmentBudgetTokens is the most the Segments together may hold, in
	// estimated tokens, before the oldest are merged. 0 means half the
	// ceiling in force, or defaultSegmentsBudgetTokens with none (effectiveBudget).
	SegmentBudgetTokens int
	SegmentSettings
}

// effectiveBudget is the Segments' budget in force: SegmentBudgetTokens when set,
// else half the ceiling in force, else defaultSegmentsBudgetTokens.
func (p CompactPolicy) effectiveBudget() int {
	if p.SegmentBudgetTokens > 0 {
		return p.SegmentBudgetTokens
	}
	return segmentsBudget(p.EffectiveCeilingTokens())
}

// EffectiveCeilingTokens is the ceiling in force: CeilingTokens when set, else the one the
// window gives, else 0 for none.
func (p CompactPolicy) EffectiveCeilingTokens() int {
	switch {
	case p.CeilingTokens > 0:
		return p.CeilingTokens
	case p.CeilingTokens < 0:
		return 0
	}
	return defaultCeiling(p.WindowTokens)
}
