package modelctx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// A model may use the same call ID in two Turns (the demo model does). The
// ref names the call ID alone, so it means the latest result of that ID: the
// log search finds the newest Entry, and the refs listed hold each ID once.
func TestARepeatedCallIDMeansTheLatestSpilledResult(t *testing.T) {
	f := newSpillFixture(t, "repeat")
	first := strings.Repeat("first body\n", gotato.SpillPageBytes/8)
	second := strings.Repeat("second body\n", gotato.SpillPageBytes/8)
	f.call("c1", first)
	f.call("c1", second)
	record := f.writer.Record()

	calls, byCall := record.SpilledResults()
	if len(calls) != 1 || len(byCall) != 1 {
		t.Fatalf("SpilledResults lists %v, want c1 once", calls)
	}

	spilled, found, err := session.FindSpilled(context.Background(), f.store, "repeat", "c1")
	if err != nil || !found {
		t.Fatalf("FindSpilled = %+v, %v, %v", spilled, found, err)
	}
	if spilled != byCall["c1"] {
		t.Fatalf("FindSpilled = %+v, SpilledResults has %+v for the same call", spilled, byCall["c1"])
	}
	body, pruned, err := record.SpilledBody(context.Background(), f.store, spilled)
	if err != nil || pruned || body != second {
		t.Fatalf("body = %.20q, pruned %v, err %v; want the second body", body, pruned, err)
	}
}
