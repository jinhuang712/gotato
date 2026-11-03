// Package workload drives the reference workload that GOALS.md measures
// through a host.Runner, and reports what it saw: how each task went, every
// failed Run, tool call and check with its cause, token usage and cost, the
// time Gotato adds per Turn next to the model's own time, the time to load a
// Session, and whether each Turn's Context matches an inspection of the
// Transcript it was built from (O9).
//
// In CI the tasks from reftasks.ReferenceTasks run with their fake Models. The
// live run passes a real Model and its price:
//
//	report, err := workload.Run(ctx, workload.Config{
//	    Model:     client,
//	    ModelName: "your-model",
//	    Price:     workload.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
//	}, reftasks.ReferenceTasks())
//
// Every number about a Run comes from the Runner's Events and RunResults,
// including the store's time to load and save the Session (session_loaded and
// session_saved), so a team can compute the same numbers from its own
// telemetry.
package workload

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// Config says how to drive the workload.
type Config struct {
	// Model replaces each task's fake Model when it is not nil: the live run.
	Model gotato.Model
	// ModelName names the Model in the report.
	ModelName string
	// Price turns token usage into a cost. A zero Price reports no cost.
	Price Price
	// Store holds the Sessions. Nil uses a memory store; a file store times
	// loads and saves from disk.
	Store store.Store
	// Plays is how many times each task is played. Zero plays it once.
	Plays int
	// Concurrency is how many plays of a task run at once on one Runner.
	// Zero or one plays them one after another; more runs the task under
	// load.
	Concurrency int
	// SkipContextCheck leaves out the comparison of each Turn's Context with
	// an inspection, for benchmarks that measure only the plays.
	SkipContextCheck bool
}

// Run plays every task and reports what it measured. A failed Run, tool call
// or check is reported, not returned; Run returns an error only when it cannot
// build a task's Runner.
func Run(ctx context.Context, cfg Config, tasks []reftasks.Task) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg.Plays = max(cfg.Plays, 1)
	cfg.Concurrency = max(cfg.Concurrency, 1)
	if cfg.Store.Records == nil || cfg.Store.Liveness == nil {
		cfg.Store = memoryStore()
	}
	report := Report{Model: cfg.ModelName, Plays: cfg.Plays, Concurrency: cfg.Concurrency}
	if report.Model == "" && cfg.Model == nil {
		report.Model = "fake"
	}
	if cfg.Price != (Price{}) {
		price := cfg.Price
		report.Price = &price
	}
	for _, task := range tasks {
		taskReport, err := runTask(ctx, cfg, task)
		if err != nil {
			return report, fmt.Errorf("workload: %s: %w", task.Name, err)
		}
		report.Tasks = append(report.Tasks, taskReport)
		report.ContextMismatches += taskReport.ContextMismatches
		for _, failure := range taskReport.Failures {
			if failure.Cause == CauseGotato || failure.Cause == CauseStore {
				report.GotatoFailures += failure.Count
			}
		}
	}
	return report, nil
}

func runTask(ctx context.Context, cfg Config, task reftasks.Task) (TaskReport, error) {
	model := task.Model
	if cfg.Model != nil {
		model = cfg.Model
	}
	// A task with a window plays against a fake Model that has one: the
	// driver counts every request against it, and refuses one over it.
	var windowed *windowModel
	if task.Window > 0 && cfg.Model == nil {
		windowed = newWindowModel(model, task.Window)
		model = windowed
	}
	spec := profile.AgentProfile{
		Name:        task.Name,
		Model:       model,
		ModelName:   cfg.ModelName,
		Instruction: task.Instruction,
		Tools:       task.Tools,
		ToolSets:    task.ToolSets,
		Limits:      task.Limits,
	}
	if task.Recap {
		spec.Compact.Recap = record.ModelRecap{}
	}
	runner, err := host.New(withAgents(host.Config{Store: cfg.Store}, []profile.AgentProfile{spec}...))
	if err != nil {
		return TaskReport{}, err
	}
	// The inspector rebuilds each Turn's Transcript in a store of its own and
	// inspects it with the same Spec.
	inspectStore := memoryStore()
	inspector, err := host.New(withAgents(host.Config{Store: inspectStore}, []profile.AgentProfile{spec}...))
	if err != nil {
		return TaskReport{}, err
	}

	plays := make([]play, cfg.Plays)
	slots := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	started := time.Now()
	for i := range plays {
		slots <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			plays[i] = playTask(ctx, runner, cfg.Store, task)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(started)

	report := TaskReport{Task: task.Name, Plays: cfg.Plays}
	if windowed != nil {
		report.Window = &WindowReport{Window: task.Window}
	}
	failures := newFailureSet()
	var total stats
	var laterInput, laterCached uint64
	for i, p := range plays {
		if p.passed {
			report.Passed++
		}
		if windowed != nil {
			report.Window.add(windowed.seriesOf(host.CacheKey(p.sessionID)), p.final, i == 0)
		}
		report.Runs += p.runs
		report.Turns += p.turns
		report.ToolCalls += p.toolCalls
		report.Usage = addUsage(report.Usage, p.usage)
		laterInput += p.laterInput
		laterCached += p.laterCached
		total.add(p.stats)
		for _, failure := range p.failures {
			failures.add(failure)
		}
		// A task with a window compacts, so a Turn is no longer built from the
		// Session's first Messages, which this check inspects.
		if !cfg.SkipContextCheck && p.final != nil && task.Window == 0 {
			checkContext(ctx, inspector, inspectStore, p.final, p.builds, &report)
		}
	}
	report.Failures = failures.list()
	report.CostUSD = cfg.Price.Cost(report.Usage)
	if laterInput > 0 {
		rate := float64(laterCached) / float64(laterInput)
		report.CacheHitRate = &rate
	}
	report.Time = total.timing(elapsed, report.Turns, report.Runs)
	return report, nil
}

// play is what one play of a task saw.
type play struct {
	passed      bool
	runs        int
	turns       int
	toolCalls   int
	usage       gotato.Usage
	laterInput  uint64
	laterCached uint64
	builds      []build
	failures    []failure
	stats       *stats
	final       *record.Record
	sessionID   string
}

// build is one Turn's context_built Event: how many committed Messages the
// Context was built from, and the prefix hash of the request.
type build struct {
	messages int
	prefix   string
}

func playTask(ctx context.Context, runner *host.Runner, st store.Store, task reftasks.Task) play {
	p := play{stats: &stats{}}
	s, err := runner.CreateSession(ctx, host.CreateSessionRequest{Agent: task.Name, Messages: task.History})
	if err != nil {
		p.failures = append(p.failures, failure{kind: KindSetup, cause: CauseStore, message: err.Error()})
		return p
	}
	p.sessionID = s.ID

	// Every number about a Run comes from its Events, as a team would
	// compute it from its own telemetry. The model's time runs from a Turn's
	// context_built to its reply_ended, or to run_ended when the call failed;
	// a tool's time runs from tool_started to tool_ended; the store's time is
	// in the Instance's session_loaded and session_saved.
	var answers []string
	var calls []toolCall
	var waiting time.Time
	toolStarts := map[gotato.ToolCallID]time.Time{}
	toolIDs := map[gotato.ToolCallID]string{}
	turn := 0
	storeEvents, err := runner.SubscribeInstance(ctx, gotato.SubscribeFilter(func(event gotato.Event) bool {
		switch event.(type) {
		case host.SessionLoaded, host.SessionSaved:
			return event.Head().SessionID == gotato.SessionID(s.ID)
		}
		return false
	}))
	if err != nil {
		p.failures = append(p.failures, failure{kind: KindSetup, cause: CauseStore, message: err.Error()})
		return p
	}
	defer func() { _ = storeEvents.Close() }()
	for _, prompt := range task.Prompts {
		started := time.Now()
		requested, streaming := false, false
		result, err := runner.StreamRun(ctx, host.RunRequest{SessionID: s.ID, Prompt: prompt}, func(event gotato.Event) error {
			switch e := event.(type) {
			case gotato.TurnStarted:
				p.turns++
			case gotato.ContextBuilt:
				p.builds = append(p.builds, build{messages: e.SourceMessages, prefix: e.PrefixHash})
				waiting, streaming = e.Time, false
				if !requested {
					p.stats.addBeforeRequest(e.Time.Sub(started))
					requested = true
				}
			case gotato.TextDelta:
				// The first text delta of a Turn: the model's time to its
				// first token.
				if !streaming && !waiting.IsZero() {
					p.stats.addFirstToken(e.Time.Sub(waiting))
					streaming = true
				}
			case gotato.ReplyEnded:
				if !waiting.IsZero() {
					p.stats.addModel(e.Time.Sub(waiting))
					waiting = time.Time{}
				}
			case gotato.ToolStarted:
				toolStarts[e.CallID] = e.Time
				toolIDs[e.CallID] = e.ToolID
			case gotato.ToolEnded:
				if since, ok := toolStarts[e.CallID]; ok {
					p.stats.addTool(e.Time.Sub(since))
				}
				calls = append(calls, toolCall{id: e.CallID, toolID: e.ToolID, status: string(e.Status), executed: e.Executed})
			case gotato.TurnEnded:
				// A play's first Turn has nothing cached yet; B1 counts
				// from the second on.
				if turn > 0 {
					p.laterInput += e.Usage.InputTokens
					p.laterCached += e.Usage.CacheReadTokens
				}
				turn++
			case gotato.ReplyCutOff:
				if !waiting.IsZero() {
					p.stats.addModel(e.Time.Sub(waiting))
					waiting = time.Time{}
				}
			case gotato.RunEnded:
				if !waiting.IsZero() {
					p.stats.addModel(e.Time.Sub(waiting))
					waiting = time.Time{}
				}
			}
			return nil
		})
		p.stats.addRun(time.Since(started))
		p.runs++
		p.usage = addUsage(p.usage, result.Result.Usage)
		if f, failed := runFailure(result, err); failed {
			p.failures = append(p.failures, f)
		}
		answers = append(answers, result.FinalText)
		readStoreTimes(ctx, storeEvents, p.stats)
	}

	// The committed tool results add the text of each failure to the
	// report; the Events have already decided its cause.
	results := map[gotato.ToolCallID]gotato.ToolResult{}
	final, err := store.Open(ctx, st.Records, s.ID)
	if err != nil {
		p.failures = append(p.failures, failure{kind: KindSetup, cause: CauseStore, message: "reading the Session back: " + err.Error()})
	} else {
		p.final = final
		for _, message := range final.Messages() {
			if message.ToolResult != nil {
				results[message.ToolResult.CallID] = *message.ToolResult
			}
		}
	}
	p.toolCalls = len(calls)
	for _, call := range calls {
		var committed *gotato.ToolResult
		if result, ok := results[call.id]; ok {
			committed = &result
		}
		if f, failed := toolFailure(call, committed); failed {
			p.failures = append(p.failures, f)
		}
	}
	if err := task.Check(answers); err != nil {
		p.failures = append(p.failures, failure{kind: KindCheck, cause: CauseModel, message: err.Error()})
	} else {
		p.passed = true
	}
	return p
}

// readStoreTimes adds the store's time for one Run: the Instance's
// session_loaded, and the session_saved that ends the Run.
func readStoreTimes(ctx context.Context, events gotato.Subscription, stats *stats) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		event, err := events.Next(ctx)
		if err != nil {
			return
		}
		switch e := event.(type) {
		case host.SessionLoaded:
			stats.addLoad(time.Duration(e.ElapsedMS * float64(time.Millisecond)))
		case host.SessionSaved:
			stats.addSave(time.Duration(e.ElapsedMS * float64(time.Millisecond)))
			return
		}
	}
}

// toolCall is one tool_ended Event.
type toolCall struct {
	id       gotato.ToolCallID
	toolID   string
	status   string
	executed bool
}

// checkContext inspects, for every Turn, the Transcript that Turn was built
// from (the Session's first Messages, since a reference task never compacts)
// and compares its prefix hash with the Turn's context_built Event (O9).
func checkContext(ctx context.Context, inspector *host.Runner, st store.Store, final *record.Record, builds []build, report *TaskReport) {
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
		if _, err := store.CreateWriter(ctx, st.Records, s, inspector.Instance()); err != nil {
			mismatch("saving the Transcript to inspect: " + err.Error())
			continue
		}
		inspection, err := inspector.Inspect(ctx, s.ID(), host.InspectConfig{})
		_ = st.Records.Delete(ctx, s.ID())
		switch {
		case err != nil:
			mismatch("inspecting: " + err.Error())
		case inspection.PrefixHash != b.prefix:
			mismatch(fmt.Sprintf("the Turn built from %d messages sent prefix %s, inspection shows %s", b.messages, b.prefix, inspection.PrefixHash))
		}
	}
}

func addUsage(a, b gotato.Usage) gotato.Usage {
	return gotato.Usage{
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
		CacheReadTokens:  a.CacheReadTokens + b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
	}
}

// memoryStore is a Store of one MemoryStore, for a play that was given none.
func memoryStore() store.Store {
	m := store.NewMemoryStore()
	return store.Store{Records: m, Liveness: m}
}

// withAgents registers agents as the Config's AgentProfiles, the first the
// default.
func withAgents(cfg host.Config, agents ...profile.AgentProfile) host.Config {
	registry := profile.NewRegistry(nil)
	for _, agent := range agents {
		if err := registry.RegisterAgentProfileInline(agent); err != nil {
			panic(err)
		}
	}
	cfg.Profiles, cfg.DefaultAgent = registry, agents[0].Name
	return cfg
}
