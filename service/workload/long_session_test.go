package workload

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/testkit"
)

func playLong(t *testing.T, task testkit.Task) TaskReport {
	t.Helper()
	report, err := Run(context.Background(), Config{}, []testkit.Task{task})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tasks) != 1 || report.Tasks[0].Window == nil {
		t.Fatalf("report = %+v, want one task with a window", report)
	}
	return report.Tasks[0]
}

// G-G10: a Session played for 2,000 Turns, each with a 20 KB Tool Result,
// against a fake Model with a 128K-token window, sends 0 requests over the
// window and fails 0 Runs for its size. The Context stays bounded: the size of
// the request levels off instead of growing with the Session's age (G-B14).
// Under the race detector the Session is 300 Turns, which takes seconds and not
// a minute.
func TestALongSessionStaysWithinItsWindow(t *testing.T) {
	task := testkit.LongSessionTask()
	turns := testkit.LongSessionTurns
	if raceEnabled {
		task = testkit.LongSession(300, testkit.LongSessionTurnsPerRun, testkit.LongSessionResultBytes, testkit.LongSessionWindow, false)
		turns = 300
	}
	report := playLong(t, task)
	w := report.Window
	if report.Turns != turns || report.Runs != turns/testkit.LongSessionTurnsPerRun || report.Passed != 1 || len(report.Failures) != 0 {
		t.Fatalf("turns %d, runs %d, passed %d, failures %+v", report.Turns, report.Runs, report.Passed, report.Failures)
	}
	if w.Requests != turns || w.OverWindow != 0 || w.MaxRequest > testkit.LongSessionWindow || w.RecapOver != 0 {
		t.Fatalf("%d requests, %d over the %d window, largest %d", w.Requests, w.OverWindow, w.Window, w.MaxRequest)
	}
	if w.Compactions < turns/20 {
		t.Errorf("%d compactions in %d Turns of 20 KB: the Session did not grow past its ceiling", w.Compactions, turns)
	}
	// The ceiling is the window less its reserve, 112K tokens; no request is past it.
	if ceiling := 112_000; w.MaxRequest > ceiling+100 {
		t.Errorf("the largest request is %d tokens, past the ceiling %d", w.MaxRequest, ceiling)
	}
	for _, at := range w.TokensAtTurn {
		if at.Tokens > 112_100 {
			t.Errorf("the request at Turn %d holds %d tokens", at.Turn, at.Tokens)
		}
	}
	if len(w.TokensAtTurn) < 3 {
		t.Errorf("sizes at Turns %v", w.TokensAtTurn)
	}
	if report.Time.GotatoPerTurnMS <= 0 {
		t.Errorf("no time per Turn: %+v", report.Time)
	}
}

// The oldest Segments merge in a long Session: the Segments stay within their
// budget, one merged Segment opens the Context, and no Run fails for it.
func TestALongSessionMergesTheOldestSegments(t *testing.T) {
	report := playLong(t, testkit.LongSession(300, 10, 4096, 32_000, false))
	w := report.Window
	if report.Passed != 1 || len(report.Failures) != 0 || w.OverWindow != 0 {
		t.Fatalf("passed %d, failures %+v, %d requests over the window", report.Passed, report.Failures, w.OverWindow)
	}
	if w.Merges < 2 || w.Compactions <= w.Merges {
		t.Fatalf("%d merges in %d compactions", w.Merges, w.Compactions)
	}
	if w.Recap != nil {
		t.Errorf("the model's Recap is off, but the report has its cost: %+v", w.Recap)
	}
}

// With the model's Recap on, the report counts what each Recap call spent and
// what each compaction took out of the Context (G-B16). A Recap's request is the
// Context plus an ask, so it fits the window too.
func TestALongSessionWithTheModelsRecapCountsItsCost(t *testing.T) {
	report := playLong(t, testkit.LongSession(300, 10, 4096, 32_000, true))
	w := report.Window
	if report.Passed != 1 || len(report.Failures) != 0 || w.OverWindow != 0 || w.RecapOver != 0 {
		t.Fatalf("passed %d, failures %+v, over %d, recap over %d", report.Passed, report.Failures, w.OverWindow, w.RecapOver)
	}
	r := w.Recap
	if r == nil || r.Calls == 0 || r.Calls < r.Closings || r.Closings == 0 || r.InputTokens == 0 || r.OutputTokens == 0 || r.TakenOutTokens == 0 {
		t.Fatalf("recap cost = %+v", r)
	}
	if w.RecapAsks != r.Closings {
		t.Errorf("%d Recap requests for %d closings", w.RecapAsks, r.Closings)
	}
	if w.Merges == 0 {
		t.Error("no merge: the merged Recaps were not asked for")
	}
}

// The check can fail: a window smaller than the preview of one spilled Tool
// Result makes every later request over it, and the report says so, with the
// Runs that failed for it.
func TestALongSessionPastItsWindowIsReported(t *testing.T) {
	report := playLong(t, testkit.LongSession(20, 10, 80_000, 1_500, false))
	w := report.Window
	if w.OverWindow == 0 || report.Passed != 0 || len(report.Failures) == 0 {
		t.Fatalf("over %d, passed %d, failures %+v: a Session that cannot fit its window must fail the check", w.OverWindow, report.Passed, report.Failures)
	}
	found := false
	for _, f := range report.Failures {
		found = found || (f.Kind == KindRun && f.Code == "context_overflow")
	}
	if !found {
		t.Errorf("failures = %+v, want a Run that ended with context_overflow", report.Failures)
	}
}
