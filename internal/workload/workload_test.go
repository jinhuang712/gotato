package workload_test

import (
	"context"
	"math"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/internal/workload"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func taskReport(t *testing.T, report workload.Report, name string) workload.TaskReport {
	t.Helper()
	for _, task := range report.Tasks {
		if task.Task == name {
			return task
		}
	}
	t.Fatalf("no report for task %s", name)
	return workload.TaskReport{}
}

// TestReferenceWorkload is the reference workload as CI runs it: every task
// with its fake model. It checks GOALS.md G-G03 (each Turn's Context matches
// its inspection) and G-G04 (no failure caused by Gotato or the store).
func TestReferenceWorkload(t *testing.T) {
	report, err := workload.Run(context.Background(), workload.Config{}, reftasks.ReferenceTasks())
	if err != nil {
		t.Fatal(err)
	}
	if report.GotatoFailures != 0 || report.ContextMismatches != 0 {
		t.Fatalf("G-G04 failures %d, G-G03 mismatches %d: %+v", report.GotatoFailures, report.ContextMismatches, report)
	}
	want := map[string]struct{ runs, turns, toolCalls int }{
		"lookup":       {1, 2, 1},
		"conversation": {10, 13, 3},
		"tool_error":   {1, 4, 3},
		"support_case": {1, 5, 4},
		"tool_search":  {7, 20, 13},
		"history_10":   {1, 2, 1},
		"history_100":  {1, 2, 1},
		"history_1000": {1, 2, 1},
	}
	for _, task := range report.Tasks {
		w, ok := want[task.Task]
		if !ok {
			t.Fatalf("unexpected task %s", task.Task)
		}
		if task.Passed != 1 || task.Runs != w.runs || task.Turns != w.turns || task.ToolCalls != w.toolCalls {
			t.Errorf("%s: passed %d, runs %d, turns %d, tool calls %d; want 1, %d, %d, %d",
				task.Task, task.Passed, task.Runs, task.Turns, task.ToolCalls, w.runs, w.turns, w.toolCalls)
		}
		if task.ContextTurns != task.Turns {
			t.Errorf("%s: %d Turns compared with an inspection, want %d", task.Task, task.ContextTurns, task.Turns)
		}
		// Every task's last reply streams text, so G-B06 has both halves.
		if task.Time.BeforeRequestPerRunMS <= 0 || task.Time.ModelFirstTokenMS < 0 || task.Time.ModelFirstTokenMS > task.Time.ModelMS {
			t.Errorf("%s: before request %v ms per Run, first token %v ms", task.Task, task.Time.BeforeRequestPerRunMS, task.Time.ModelFirstTokenMS)
		}
		if task.CacheHitRate != nil || task.CostUSD != 0 {
			t.Errorf("%s: a fake model reports no usage, so no cache rate or cost: %+v", task.Task, task)
		}
	}
	// The tool error task fails twice on purpose: once on the model's
	// arguments, once in the tool.
	failures := taskReport(t, report, "tool_error").Failures
	if len(failures) != 2 ||
		failures[0].Cause != workload.CauseModel || failures[0].Code != string(gotato.ErrToolArgumentFailure) ||
		failures[1].Cause != workload.CauseTool || failures[1].Example != "order 7 not found" {
		t.Fatalf("tool_error failures = %+v", failures)
	}
}

func TestWorkloadUnderLoad(t *testing.T) {
	tasks := []reftasks.Task{reftasks.LookupTask(), reftasks.ConversationTask()}
	report, err := workload.Run(context.Background(), workload.Config{Plays: 16, Concurrency: 8}, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if report.GotatoFailures != 0 || report.ContextMismatches != 0 || report.Concurrency != 8 {
		t.Fatalf("report = %+v", report)
	}
	for _, task := range report.Tasks {
		if task.Plays != 16 || task.Passed != 16 || len(task.Failures) != 0 {
			t.Fatalf("%s: %+v", task.Task, task)
		}
		if task.Time.RunMS < task.Time.GotatoMS || task.Time.GotatoPerTurnMS <= 0 {
			t.Fatalf("%s: timing = %+v", task.Task, task.Time)
		}
	}
}

func TestWorkloadOnAFileStore(t *testing.T) {
	store, err := session.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report, err := workload.Run(context.Background(), workload.Config{Store: store}, []reftasks.Task{reftasks.HistoryTask(100)})
	if err != nil {
		t.Fatal(err)
	}
	task := report.Tasks[0]
	if task.Passed != 1 || report.GotatoFailures != 0 || report.ContextMismatches != 0 {
		t.Fatalf("report = %+v", report)
	}
	if task.Time.LoadMS <= 0 || task.Time.SaveMS <= 0 || task.Time.LoadPerRunMS <= 0 {
		t.Fatalf("a file store takes time to load and save: %+v", task.Time)
	}
}

// usageModel reports usage on every call, as a provider would: 1,000 input
// tokens, 800 of them read from the cache, and 50 output tokens.
type usageModel struct{ inner gotato.Model }

func (m usageModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	stream, err := m.inner.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	var events testkit.Script
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			break
		}
		if event.Kind == gotato.ModelDone {
			events = append(events, gotato.ModelEvent{Kind: gotato.ModelUsage, Usage: gotato.Usage{
				InputTokens: 1000, CacheReadTokens: 800, OutputTokens: 50, TotalTokens: 1050,
			}})
		}
		events = append(events, event)
	}
	return testkit.NewReplayModel(events).Stream(ctx, request)
}

func TestCostAndCacheHitRate(t *testing.T) {
	price := workload.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	task := reftasks.LookupTask()
	report, err := workload.Run(context.Background(), workload.Config{
		Model:     usageModel{inner: task.Model},
		ModelName: "usage-model",
		Price:     price,
	}, []reftasks.Task{task})
	if err != nil {
		t.Fatal(err)
	}
	lookup := report.Tasks[0]
	if report.Model != "usage-model" || report.Price == nil || lookup.Usage.InputTokens != 2000 || lookup.Usage.CacheReadTokens != 1600 {
		t.Fatalf("report = %+v", report)
	}
	// Two Turns: 400 uncached input tokens at $3, 1,600 cached at $0.30 and
	// 100 output tokens at $15, per million.
	if want := (400*3 + 1600*0.3 + 100*15) / 1e6; math.Abs(lookup.CostUSD-want) > 1e-12 {
		t.Fatalf("cost = %v, want %v", lookup.CostUSD, want)
	}
	// Only the second Turn counts toward the cache hit rate.
	if lookup.CacheHitRate == nil || *lookup.CacheHitRate != 0.8 {
		t.Fatalf("cache hit rate = %v, want 0.8", lookup.CacheHitRate)
	}
}

// failingModel fails every call, as a provider that is down would.
type failingModel struct{}

func (failingModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, gotato.ErrorOf(gotato.ErrModelFailure, "provider unavailable")
}

func TestFailedRunsAreAttributed(t *testing.T) {
	report, err := workload.Run(context.Background(), workload.Config{Model: failingModel{}}, []reftasks.Task{reftasks.LookupTask()})
	if err != nil {
		t.Fatal(err)
	}
	failures := report.Tasks[0].Failures
	if report.GotatoFailures != 0 || len(failures) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if failures[0].Kind != workload.KindCheck || failures[0].Cause != workload.CauseModel ||
		failures[1].Kind != workload.KindRun || failures[1].Cause != workload.CauseProvider || failures[1].Code != string(gotato.ErrModelFailure) {
		t.Fatalf("failures = %+v", failures)
	}
}

// BenchmarkReferenceTasks plays each task with its fake model and reports
// Gotato's time per Turn (G-B05, G-B09) and the load time per Run (G-B07).
// Run it with -benchmem for allocations.
func BenchmarkReferenceTasks(b *testing.B) {
	for _, task := range reftasks.ReferenceTasks() {
		b.Run(task.Name, func(b *testing.B) {
			report, err := workload.Run(context.Background(), workload.Config{Plays: b.N, SkipContextCheck: true}, []reftasks.Task{task})
			if err != nil {
				b.Fatal(err)
			}
			result := report.Tasks[0]
			if result.Passed != b.N {
				b.Fatalf("%d of %d plays passed: %+v", result.Passed, b.N, result.Failures)
			}
			b.ReportMetric(result.Time.GotatoPerTurnMS*1e6, "gotato-ns/turn")
			b.ReportMetric(result.Time.LoadPerRunMS*1e6, "load-ns/run")
		})
	}
}
