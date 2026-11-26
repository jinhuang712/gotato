package workload

import (
	"context"
	"fmt"
	"time"

	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// taskFold folds the plays of one task, one after another, into its report.
type taskFold struct {
	cfg       Config
	task      reftasks.Task
	windowed  *windowModel
	inspector contextInspector
	report    TaskReport
	failures  *failureSet
	total     stats
	// laterInput and laterCached sum the input and cached tokens from each
	// play's second Turn on.
	laterInput, laterCached uint64
}

// newTaskFold starts the report of task, which plays against windowed when it
// is not nil.
func newTaskFold(cfg Config, task reftasks.Task, windowed *windowModel, inspector contextInspector) *taskFold {
	f := &taskFold{cfg: cfg, task: task, windowed: windowed, inspector: inspector, failures: newFailureSet()}
	f.report = TaskReport{Task: task.Name, Plays: cfg.Plays}
	if windowed != nil {
		f.report.Window = &WindowReport{Window: task.Window}
	}
	return f
}

// add folds play number i into the report.
func (f *taskFold) add(ctx context.Context, i int, p play) {
	report := &f.report
	if p.passed {
		report.Passed++
	}
	if f.windowed != nil {
		report.Window.add(f.windowed.seriesOf(host.CacheKey(p.sessionID)), p.final, i == 0)
	}
	report.Runs += p.runs
	report.Turns += p.turns
	report.ToolCalls += p.toolCalls
	report.Usage = addUsage(report.Usage, p.usage)
	f.laterInput += p.laterInput
	f.laterCached += p.laterCached
	f.total.add(p.stats)
	for _, failure := range p.failures {
		f.failures.add(failure)
	}
	// A task with a window compacts, so a Turn is no longer built from the
	// Session's first Messages, which this check inspects.
	if !f.cfg.SkipContextCheck && p.final != nil && f.task.Window == 0 {
		f.inspector.check(ctx, p.final, p.builds, report)
	}
}

// finish returns the report once every play is folded; elapsed is the wall
// time of all plays.
func (f *taskFold) finish(elapsed time.Duration) TaskReport {
	report := f.report
	report.Failures = f.failures.list()
	report.CostUSD = f.cfg.Price.Cost(report.Usage)
	if f.laterInput > 0 {
		rate := float64(f.laterCached) / float64(f.laterInput)
		report.CacheHitRate = &rate
	}
	report.Time = f.total.timing(elapsed, report.Turns, report.Runs)
	return report
}

// contextInspector rebuilds each Turn's Transcript in a store of its own and
// inspects it.
type contextInspector struct {
	runner *host.Runner
	store  store.Store
}

// check inspects, for every Turn, the Transcript that Turn was built
// from (the Session's first Messages, since a reference task never compacts)
// and compares its prefix hash with the Turn's context_built Event (O9).
func (c contextInspector) check(ctx context.Context, final *record.Record, builds []build, report *TaskReport) {
	messages := final.Messages()
	mismatch := func(detail string) {
		report.ContextMismatches++
		if report.ContextDetail == "" {
			report.ContextDetail = detail
		}
	}
	for _, b := range builds {
		report.ContextTurns++
		if b.messages > len(messages) {
			mismatch(fmt.Sprintf("a Turn was built from %d messages, but the Session holds %d", b.messages, len(messages)))
			continue
		}
		s := record.New(record.WithMetadata(final.Metadata()), record.WithMessages(messages[:b.messages]...), reftasks.WithLoadedToolsOf(messages[:b.messages]))
		if _, err := store.CreateWriter(ctx, c.store.Records, s, c.runner.Instance()); err != nil {
			mismatch("saving the Transcript to inspect: " + err.Error())
			continue
		}
		inspection, err := c.runner.Inspect(ctx, s.ID(), host.InspectConfig{})
		_ = c.store.Records.Delete(ctx, s.ID())
		switch {
		case err != nil:
			mismatch("inspecting: " + err.Error())
		case inspection.PrefixHash != b.prefix:
			mismatch(fmt.Sprintf("the Turn built from %d messages sent prefix %s, inspection shows %s", b.messages, b.prefix, inspection.PrefixHash))
		}
	}
}
