package text

import "strings"

// The first words of the two messages that ask a model for a Recap. They are
// the one definition: modelctx writes its asks with them, and a fake model
// (testkit.DemoModel, the reference tasks) recognizes an ask with IsRecapAsk,
// so no one matches the wording by hand.
const (
	// RecapAskPrefix starts the ask for the Recap of a stretch (a compaction).
	RecapAskPrefix = "Write a recap of"
	// MergeRecapAskPrefix starts the ask for one Recap of the Recaps of
	// several Segments (a merge).
	MergeRecapAskPrefix = "Write one recap of"
)

// IsRecapAsk reports whether message is the ask for a Recap of either kind.
func IsRecapAsk(message string) bool {
	return strings.HasPrefix(message, RecapAskPrefix) || strings.HasPrefix(message, MergeRecapAskPrefix)
}
