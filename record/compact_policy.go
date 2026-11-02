package record

import "time"

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

// CompactPolicy keeps the numbers a compaction works to: when it is due and
// how much the Segments may hold. The parts that decide and build are the
// Compactor's own (Trigger, Cutter, Builder, RecapWriter, Picker), each with
// the settings it needs.
//
// Past the ceiling, estimated in tokens, the Session is compacted before the
// model call that would send more; a request the provider refuses as too long
// is compacted once and sent again (AutoCompactor).
//
// CeilingTokens above 0 is the ceiling in force. At 0 it is
// defaultCeiling(WindowTokens), so a model whose window is known is compacted
// past it by default, and with no window there is no ceiling. A negative
// CeilingTokens turns the ceiling off. WindowTokens is the Model's context
// window, 0 when unknown. DropReasoning leaves the reasoning out of the
// Messages every compaction keeps (ModelFacts.RoundReasoning). A Compactor
// embeds a CompactPolicy, so Compact on demand and the automatic compaction
// share one set of numbers.
//
// The zero CompactPolicy is valid and does little: with no window and no
// ceiling the ceiling trigger is off, so it compacts only a request the
// provider refused as too long. Set WindowTokens, from
// Model.Config().Facts.Window.Tokens(), or CeilingTokens to compact before the
// limit. A Runner fills the window from the Agent's Model.
type CompactPolicy struct {
	CeilingTokens int
	WindowTokens  int
	DropReasoning bool
	// SegmentBudgetTokens is the most the Segments together may hold, in
	// estimated tokens, before the oldest are merged. 0 means half the
	// ceiling in force, or defaultSegmentsBudgetTokens with none (effectiveBudget).
	SegmentBudgetTokens int
	// Location is the time zone of the times a Segment, a merge's digest or
	// Recap, a Recap ask and read_segment show. Nil means UTC.
	Location *time.Location
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

// overriddenBy returns p with every number that o sets in its place: a
// CeilingTokens that is not 0 (a negative one turns the ceiling off), a positive
// WindowTokens or SegmentBudgetTokens, a Location, and DropReasoning when o has it.
func (p CompactPolicy) overriddenBy(o CompactPolicy) CompactPolicy {
	if o.CeilingTokens != 0 {
		p.CeilingTokens = o.CeilingTokens
	}
	if o.WindowTokens > 0 {
		p.WindowTokens = o.WindowTokens
	}
	if o.SegmentBudgetTokens > 0 {
		p.SegmentBudgetTokens = o.SegmentBudgetTokens
	}
	if o.Location != nil {
		p.Location = o.Location
	}
	p.DropReasoning = p.DropReasoning || o.DropReasoning
	return p
}

func (p CompactPolicy) location() *time.Location {
	if p.Location == nil {
		return time.UTC
	}
	return p.Location
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
