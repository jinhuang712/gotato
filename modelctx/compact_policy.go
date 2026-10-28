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
// SegmentSettings.TailTokens. DropReasoning leaves the reasoning out of the
// Messages every compaction keeps (see DropsReasoning). CompactConfig embeds a
// CompactPolicy, so Compact on demand and the automatic compaction share one
// set of settings.
//
// The zero CompactPolicy is valid and does little: with no window and no
// ceiling the ceiling trigger is off, so it compacts only a request the
// provider refused as too long, and its Segments have no Recap. Set
// WindowTokens, from WindowOf(model), or CeilingTokens to compact before the
// limit. A Runner fills the window from the Agent's Model. Validate refuses
// the settings that cannot work.
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

// segmentsBudget returns the most the Segments of a Context may hold, in
// estimated tokens, under the ceiling in force: half of it, or
// defaultSegmentsBudgetTokens when there is no ceiling (ceiling 0). When the
// Segments pass it, the oldest are merged (a merge).
func segmentsBudget(ceiling int) int {
	if ceiling > 0 {
		return max(ceiling/2, 1)
	}
	return defaultSegmentsBudgetTokens
}

// defaultSegmentsBudgetTokens is the Segments' budget with no ceiling: the most
// the Segments together may hold, in estimated tokens (bytes ÷ 4), when the
// model has no known window and neither the Agent nor the Session sets a
// ceiling. With a ceiling, the budget is half of it (segmentsBudget).
const defaultSegmentsBudgetTokens = 32000

// Validate checks the settings that cannot work as set. A Recap of type
// ModelRecap needs a Model (a Runner fills it from the Agent's), and a
// negative Timeout is refused. Compact and the AutoCompactor call it before
// they do anything, so a misconfiguration is an error at first use, not a
// Segment that quietly has no Recap.
func (p CompactPolicy) Validate() error {
	switch recap := p.Recap.(type) {
	case ModelRecap:
		return recap.validate()
	case *ModelRecap:
		if recap != nil {
			return recap.validate()
		}
	}
	return nil
}
