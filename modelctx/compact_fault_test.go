package modelctx

import (
	"context"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// failWrites makes the write that closes a Segment fail, for the test.
func failWrites(t *testing.T) {
	t.Helper()
	original := closeSegment
	closeSegment = func(*session.Record, gotato.CompactionWhy, gotato.Segment, []int, []gotato.Message, *session.Compaction) (gotato.Segment, error) {
		return gotato.Segment{}, gotato.ErrorOf(gotato.ErrStoreFailure, "disk gone")
	}
	t.Cleanup(func() { closeSegment = original })
}

func conversation(t *testing.T) *session.Record {
	t.Helper()
	record := session.New(session.WithID("faulty"))
	record.SetActiveToolSets([]string{"files"})
	for i, text := range []string{"u1", "a1", "u2", "a2"} {
		m := gotato.UserMessage(text)
		if i%2 == 1 {
			m = gotato.AssistantMessage(text)
		}
		m.ID = gotato.MessageID("m" + text)
		if err := record.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	// Store the Record, so that it keeps its Events as a stored Record does.
	if _, err := session.Create(context.Background(), session.NewMemoryStore(), record, "instance-a"); err != nil {
		t.Fatal(err)
	}
	return record
}

// A compaction whose write fails is dropped: an Event says so, the error
// reaches an on-demand caller, and the Record, ToolSets included, is as it was.
func TestACompactionWhoseWriteFailsIsDropped(t *testing.T) {
	failWrites(t)
	record := conversation(t)
	_, err := Compact(context.Background(), record, CompactOptions{Keep: 2})
	if !gotato.IsCode(err, gotato.ErrStoreFailure) {
		t.Fatalf("err = %v", err)
	}
	if record.Len() != 4 || len(record.Segments()) != 0 || len(record.Compactions()) != 0 || len(record.ActiveToolSets()) != 1 {
		t.Fatalf("the dropped compaction changed the Record: %d Messages, %d Segments, ToolSets %v", record.Len(), len(record.Segments()), record.ActiveToolSets())
	}
	var kinds []gotato.EventKind
	var failed gotato.CompactionFailed
	for _, event := range record.Events() {
		kinds = append(kinds, event.Kind())
		if e, ok := event.(gotato.CompactionFailed); ok {
			failed = e
		}
	}
	n := len(kinds)
	if n < 2 || kinds[n-2] != gotato.KindCompactionStarted || kinds[n-1] != gotato.KindCompactionFailed || failed.Cause != gotato.ErrStoreFailure {
		t.Fatalf("kinds = %v, failed = %+v", kinds, failed)
	}
}

// runOnce runs one prompt on record with auto installed and a model that
// answers "still here".
func runOnce(t *testing.T, record *session.Record, auto *AutoCompactor) (gotato.RunResult, *testkit.FakeModel, error) {
	t.Helper()
	model := testkit.NewFakeModel(testkit.Text("still here"))
	agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(record), gotato.WithExtension(auto))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), gotato.UserMessage("next"))
	return result, model, err
}

// The model reported more tokens than the Messages are estimated to hold, so
// the Session is past the ceiling by the model's count and under it by the
// estimate. The compaction is dropped and the Run goes on with the Context it
// had.
func TestARunGoesOnWhenItsCompactionIsDroppedAndTheEstimateFits(t *testing.T) {
	failWrites(t)
	record := conversation(t)
	reported := gotato.AssistantMessage("a3")
	reported.ID = "ma3"
	reported.Usage = gotato.Usage{InputTokens: 900, OutputTokens: 100}
	if err := record.Append(reported); err != nil {
		t.Fatal(err)
	}
	auto := AutoCompact(record, CompactPolicy{Ceiling: 500})
	result, model, err := runOnce(t, record, auto)
	if err != nil {
		t.Fatalf("the Run failed with its compaction: %v", err)
	}
	if result.Status != gotato.RunCompleted || result.FinalMessage == nil || gotato.TextOf(*result.FinalMessage) != "still here" {
		t.Fatalf("result = %+v", result)
	}
	if _, runs := auto.Last(); runs != 0 || len(record.Segments()) != 0 {
		t.Fatalf("the dropped compaction counts: runs = %d, segments = %d", runs, len(record.Segments()))
	}
	request, _ := model.LastRequest()
	if len(request.Messages) != 6 || strings.Contains(gotato.TextOf(request.Messages[0]), "<segment") {
		t.Fatalf("the Run did not keep the Context it had: %d Messages", len(request.Messages))
	}
	var failed bool
	for _, event := range record.Events() {
		_, ok := event.(gotato.CompactionFailed)
		failed = failed || ok
	}
	if !failed {
		t.Error("no compaction_failed Event says the compaction was dropped")
	}
}

// Still past the ceiling after the dropped compaction, the Run ends, and says
// the compaction failed.
func TestARunEndsWhenItsCompactionIsDroppedAndTheContextIsStillPastTheCeiling(t *testing.T) {
	failWrites(t)
	record := conversation(t)
	auto := AutoCompact(record, CompactPolicy{Ceiling: 1})
	result, model, err := runOnce(t, record, auto)
	if !gotato.IsCode(err, gotato.ErrLimitExceeded) || result.Status != gotato.RunFailed || !strings.Contains(err.Error(), "compaction failed") {
		t.Fatalf("result = %+v, err = %v; want limit_exceeded naming the failed compaction", result, err)
	}
	if model.Calls() != 0 {
		t.Fatalf("the Model was called %d times for a Context past the ceiling", model.Calls())
	}
}
