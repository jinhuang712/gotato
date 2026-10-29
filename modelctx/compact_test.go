package modelctx

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// failWrites makes the write that closes a Segment fail, for the test.
func failWrites(t *testing.T) {
	t.Helper()
	original := closeSegment
	closeSegment = func(*session.Record, gotato.CompactionWhy, gotato.Segment, []int, []gotato.Message, *gotato.Compaction) (gotato.Segment, error) {
		return gotato.Segment{}, gotato.ErrorOf(gotato.CodeStoreFailure, "disk gone")
	}
	t.Cleanup(func() { closeSegment = original })
}

func conversation(t *testing.T) *session.Record {
	t.Helper()
	record := session.New(session.WithID("faulty"))
	record.SetLoadedTools([]string{"files.read"})
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
	if _, err := session.CreateWriter(context.Background(), session.NewMemoryStore(), record, "instance-a"); err != nil {
		t.Fatal(err)
	}
	return record
}

// A compaction whose write fails is dropped: an Event says so, the error
// reaches an on-demand caller, and the Record, ToolSets included, is as it was.
func TestACompactionWhoseWriteFailsIsDropped(t *testing.T) {
	failWrites(t)
	record := conversation(t)
	_, err := Compact(context.Background(), record, CompactConfig{KeepMessages: 2})
	if !gotato.IsCode(err, gotato.CodeStoreFailure) {
		t.Fatalf("err = %v", err)
	}
	if record.Len() != 4 || len(record.Segments()) != 0 || len(record.Compactions()) != 0 || len(record.LoadedTools()) != 1 {
		t.Fatalf("the dropped compaction changed the Record: %d Messages, %d Segments, loaded Tools %v", record.Len(), len(record.Segments()), record.LoadedTools())
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
	if n < 2 || kinds[n-2] != gotato.EventKindCompactionStarted || kinds[n-1] != gotato.EventKindCompactionFailed || failed.Cause != gotato.CodeStoreFailure {
		t.Fatalf("kinds = %v, failed = %+v", kinds, failed)
	}
}

// runOnce runs one prompt on record with auto installed and a model that
// answers "still here".
func runOnce(t *testing.T, record *session.Record, auto *AutoCompactor) (gotato.RunResult, *testkit.FakeModel, error) {
	t.Helper()
	model := testkit.NewFakeModel(testkit.Text("still here"))
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(record), gotato.WithExtension(auto))
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
	auto := AutoCompact(record, CompactPolicy{CeilingTokens: 500})
	result, model, err := runOnce(t, record, auto)
	if err != nil {
		t.Fatalf("the Run failed with its compaction: %v", err)
	}
	if result.Status != gotato.RunStatusCompleted || result.FinalMessage == nil || gotato.TextOf(*result.FinalMessage) != "still here" {
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
	auto := AutoCompact(record, CompactPolicy{CeilingTokens: 1})
	result, model, err := runOnce(t, record, auto)
	if !gotato.IsCode(err, gotato.CodeLimitExceeded) || result.Status != gotato.RunStatusFailed || !strings.Contains(err.Error(), "compaction failed") {
		t.Fatalf("result = %+v, err = %v; want limit_exceeded naming the failed compaction", result, err)
	}
	if model.Calls() != 0 {
		t.Fatalf("the Model was called %d times for a Context past the ceiling", model.Calls())
	}
}

// midRun is a Record with an earlier exchange, then a Run that calls a tool at
// its first Turn and reports more tokens than the ceiling.
func midRun(t *testing.T, auto func(*session.Record) *AutoCompactor, model *testkit.FakeModel) (gotato.RunResult, *session.Record, error) {
	t.Helper()
	record := conversation(t)
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(record),
		gotato.WithTools(testkit.NewFakeTool("demo.read", "ok")), gotato.WithExtension(auto(record)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(context.Background(), gotato.UserMessage("investigate"))
	return result, record, err
}

func reportingCall() testkit.Script {
	script := testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "demo.read", Arguments: []byte(`{}`)})
	script[len(script)-1].Usage = gotato.Usage{InputTokens: 900, OutputTokens: 100}
	return script
}

// Mid-Run, a compaction whose write fails is dropped and the Run goes on with
// the Context it had, when the estimate of that Context fits the ceiling.
func TestARunGoesOnWhenItsMidRunCompactionIsDropped(t *testing.T) {
	failWrites(t)
	model := testkit.NewFakeModel(reportingCall(), testkit.Text("all done"))
	result, record, err := midRun(t, func(r *session.Record) *AutoCompactor { return AutoCompact(r, CompactPolicy{CeilingTokens: 500}) }, model)
	if err != nil || result.Status != gotato.RunStatusCompleted || model.Calls() != 2 {
		t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.Calls())
	}
	if len(record.Segments()) != 0 {
		t.Fatalf("the dropped compaction left %d Segments", len(record.Segments()))
	}
	failed := false
	for _, event := range record.Events() {
		_, ok := event.(gotato.CompactionFailed)
		failed = failed || ok
	}
	if !failed {
		t.Error("no compaction_failed Event says the compaction was dropped")
	}
}

// failMerges makes the write that merges Segments fail, for the test.
func failMerges(t *testing.T) {
	t.Helper()
	original := mergeSegments
	mergeSegments = func(*session.Record, gotato.CompactionWhy, gotato.Segment, []int, []gotato.Message, *gotato.Compaction) (gotato.Segment, error) {
		return gotato.Segment{}, gotato.ErrorOf(gotato.CodeStoreFailure, "disk gone")
	}
	t.Cleanup(func() { mergeSegments = original })
}

// threeSegments is a Record with three Segments closed and a Message after them.
func threeSegments(t *testing.T) *session.Record {
	t.Helper()
	record := conversation(t)
	for i := 1; i <= 3; i++ {
		if i > 1 {
			for _, m := range []gotato.Message{gotato.UserMessage("question"), gotato.AssistantMessage("answer")} {
				m.ID = gotato.MessageID(fmt.Sprintf("extra-%d-%s", i, m.Role))
				if err := record.Append(m); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := Compact(context.Background(), record, CompactConfig{CompactPolicy: CompactPolicy{SegmentSettings: SegmentSettings{TailTokens: 1}, SegmentBudgetTokens: 1 << 30}}); err != nil {
			t.Fatal(err)
		}
	}
	return record
}

// A merge whose write fails is dropped like any compaction: the Segments stay
// as they were, an Event says why, and the Segment a compaction closed before
// it stays closed.
func TestAMergeWhoseWriteFailsLeavesTheSegmentsUnmerged(t *testing.T) {
	failMerges(t)
	record := threeSegments(t)
	before := len(record.Events())
	result, err := mergeOldest(context.Background(), record, MergeConfig{BudgetTokens: 1})
	if err != nil || result.Merged || result.Reason != MergeReasonWriteFailed || !strings.Contains(result.Error, "disk gone") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(record.Segments()) != 3 || record.Segments()[0].Number != 1 {
		t.Fatalf("the failed merge changed the Segments: %d", len(record.Segments()))
	}
	var kinds []gotato.EventKind
	var failed gotato.CompactionFailed
	for _, event := range record.Events()[before:] {
		kinds = append(kinds, event.Kind())
		if e, ok := event.(gotato.CompactionFailed); ok {
			failed = e
		}
	}
	if len(kinds) != 2 || kinds[0] != gotato.EventKindCompactionStarted || kinds[1] != gotato.EventKindCompactionFailed || failed.Cause != gotato.CodeStoreFailure || failed.Why != gotato.CompactionWhyPastBudget {
		t.Fatalf("kinds = %v, failed = %+v", kinds, failed)
	}

	more := gotato.UserMessage("more")
	more.ID = "more-1"
	if err := record.Append(more); err != nil {
		t.Fatal(err)
	}
	compacted, err := Compact(context.Background(), record, CompactConfig{CompactPolicy: CompactPolicy{SegmentSettings: SegmentSettings{TailTokens: 1}, SegmentBudgetTokens: 1}})
	if err != nil || !compacted.Replaced || compacted.Merge == nil || compacted.Merge.Reason != MergeReasonWriteFailed || len(record.Segments()) != 4 {
		t.Fatalf("compact = %+v, %d Segments; the closed Segment must stay", compacted, len(record.Segments()))
	}
}

// stopper cancels the Run while its Recap is written.
type stopper struct{ cancel context.CancelFunc }

func (stopper) Name() string { return "stopper" }
func (s stopper) WriteRecap(ctx context.Context, _ RecapRequest) (gotato.Message, error) {
	s.cancel()
	return gotato.Message{}, ctx.Err()
}

// A Run stopped during its mid-Run compaction ends canceled, and no Segment
// is left.
func TestARunStoppedDuringItsMidRunCompactionEndsCanceled(t *testing.T) {
	record := conversation(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := testkit.NewFakeModel(reportingCall(), testkit.Text("never"))
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(record),
		gotato.WithTools(testkit.NewFakeTool("demo.read", "ok")),
		gotato.WithExtension(AutoCompact(record, CompactPolicy{CeilingTokens: 500, Recap: stopper{cancel}})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	result, err := agent.Prompt(ctx, gotato.UserMessage("investigate"))
	if !gotato.IsCode(err, gotato.CodeCanceled) || model.Calls() != 1 || len(record.Segments()) != 0 {
		t.Fatalf("result = %+v, err = %v, %d calls, %d Segments", result, err, model.Calls(), len(record.Segments()))
	}
}

// The latest Turn starts at the Run's last reply, whatever follows it: tool
// results, a notice, a steer, or the user Message of an interrupt. On the
// Run's first Turn it starts at the Run's first Message, the prompt. With no
// Run to go by, it is the last Message that is not a tool result.
func TestLastTurnStartIsTheRunsLastReply(t *testing.T) {
	user := func(run gotato.RunID) gotato.Message {
		m := gotato.UserMessage("u")
		m.RunID = run
		return m
	}
	reply := func(run gotato.RunID, calls int) gotato.Message {
		m := gotato.AssistantMessage("a")
		m.RunID = run
		for range calls {
			m.ToolCalls = append(m.ToolCalls, gotato.ToolCall{ID: "c", ToolID: "t"})
		}
		return m
	}
	result := func(run gotato.RunID) gotato.Message { return gotato.Message{RunID: run, Role: gotato.RoleToolResult} }
	notice := func(run gotato.RunID) gotato.Message { return gotato.Message{RunID: run, Role: gotato.RoleNotice} }
	cutOff := func(run gotato.RunID) gotato.Message {
		m := reply(run, 0)
		m.StopReason = gotato.StopReasonCutOff
		return m
	}
	for _, c := range []struct {
		name     string
		run      gotato.RunID
		messages []gotato.Message
		want     int
	}{
		{"tool results and a notice", "r1", []gotato.Message{user("r1"), reply("r1", 2), result("r1"), result("r1"), notice("r1")}, 1},
		{"a tool result, a steer and a notice", "r1", []gotato.Message{user("r1"), reply("r1", 1), result("r1"), user("r1"), notice("r1")}, 1},
		{"an interrupt keeps the cut-off reply", "r1", []gotato.Message{user("r0"), reply("r0", 0), user("r1"), cutOff("r1"), user("r1")}, 3},
		{"the first Turn keeps the prompt and its notice", "r1", []gotato.Message{user("r0"), reply("r0", 0), user("r1"), notice("r1")}, 2},
		{"no Run falls back to the last Message that is not a tool result", "", []gotato.Message{user(""), reply("", 1), result(""), result("")}, 1},
		{"no Run, and a notice last", "", []gotato.Message{user(""), reply("", 1), result(""), notice("")}, 3},
	} {
		if got := lastTurnStart(c.messages, c.run); got != c.want {
			t.Errorf("%s: lastTurnStart = %d, want %d", c.name, got, c.want)
		}
	}
}
