package storetest

import (
	"fmt"

	"github.com/jinhuang712/gotato/session"
)

// The rules a failure names, so that an adapter author reads what to fix and
// not only what differed.
const (
	ruleLog       = "Log must return every Entry, facts and log, in order, unchanged"
	ruleLogLimit  = "Log must return at most limit Entries, from the first one after the position, and every Entry when limit is 0 or less"
	ruleLoadFacts = "Load must return the Entries of Layer fact only, from the last Checkpoint on, in order, unchanged"
	ruleSegments  = "Load must return the segment_closed Entries from before the last Checkpoint in Loaded.Segments, and Entries from the Checkpoint on stay facts"
	ruleAtomic    = "a refused write must leave the Record as it was, and a write is all or nothing"
)

// failf builds a failure message. It starts with the name of the call that
// misbehaved, which is why it is not an fmt.Errorf: an error string does not
// start with a capital letter, but a method name does.
func failf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// sameEntries compares what a call returned (named by what) with the Entries
// wanted, and names the rule when they differ.
func sameEntries(what string, got, want []session.Entry, rule string) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s returned %v, want %v: %s", what, seqs(got), seqs(want), rule)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Seq != w.Seq || g.Kind != w.Kind || g.Layer != w.Layer || !g.At.Equal(w.At) || g.Instance != w.Instance || string(g.Data) != string(w.Data) {
			return fmt.Errorf("%s returned Entry %d as %+v (data %s), want %+v (data %s): the store must keep Seq, Kind, Layer, At, Instance and Data unchanged", what, w.Seq, g, g.Data, w, w.Data)
		}
	}
	return nil
}
