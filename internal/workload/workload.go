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
	report := newReport(cfg)
	for _, task := range tasks {
		taskReport, err := runTask(ctx, cfg, task)
		if err != nil {
			return report, fmt.Errorf("workload: %s: %w", task.Name, err)
		}
		report.addTask(taskReport)
	}
	return report, nil
}

// newReport starts the report of a run of cfg.
func newReport(cfg Config) Report {
	report := Report{Model: cfg.ModelName, Plays: cfg.Plays, Concurrency: cfg.Concurrency}
	if report.Model == "" && cfg.Model == nil {
		report.Model = "fake"
	}
	if cfg.Price != (Price{}) {
		price := cfg.Price
		report.Price = &price
	}
	return report
}

// addTask adds what one task measured to the report.
func (r *Report) addTask(task TaskReport) {
	r.Tasks = append(r.Tasks, task)
	r.ContextMismatches += task.ContextMismatches
	for _, failure := range task.Failures {
		if failure.Cause == CauseGotato || failure.Cause == CauseStore {
			r.GotatoFailures += failure.Count
		}
	}
}

func runTask(ctx context.Context, cfg Config, task reftasks.Task) (TaskReport, error) {
	model, windowed := taskModel(cfg, task)
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

	started := time.Now()
	plays := runPlays(ctx, runner, cfg, task)
	elapsed := time.Since(started)

	fold := newTaskFold(cfg, task, windowed, contextInspector{runner: inspector, store: inspectStore})
	for i, p := range plays {
		fold.add(ctx, i, p)
	}
	return fold.finish(elapsed), nil
}

// taskModel returns the Model a task plays against: the live one when the
// config has it, else the task's fake. A task with a window plays against a
// fake Model that has one: the driver counts every request against it, and
// refuses one over it. That Model is returned too, or nil.
func taskModel(cfg Config, task reftasks.Task) (gotato.Model, *windowModel) {
	model := task.Model
	if cfg.Model != nil {
		model = cfg.Model
	}
	if task.Window > 0 && cfg.Model == nil {
		windowed := newWindowModel(model, task.Window)
		return windowed, windowed
	}
	return model, nil
}

// runPlays plays the task cfg.Plays times, cfg.Concurrency at once.
func runPlays(ctx context.Context, runner *host.Runner, cfg Config, task reftasks.Task) []play {
	plays := make([]play, cfg.Plays)
	slots := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
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
	return plays
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
	storeEvents, err := subscribeStoreEvents(ctx, runner, s.ID)
	if err != nil {
		p.failures = append(p.failures, failure{kind: KindSetup, cause: CauseStore, message: err.Error()})
		return p
	}
	defer func() { _ = storeEvents.Close() }()
	answers, calls := p.playPrompts(ctx, runner, storeEvents, s.ID, task.Prompts)
	// The committed tool results add the text of each failure to the
	// report; the Events have already decided its cause.
	results := map[gotato.ToolCallID]gotato.ToolResult{}
	p.readFinal(ctx, st, s.ID, results)
	p.addToolFailures(calls, results)
	if err := task.Check(answers); err != nil {
		p.failures = append(p.failures, failure{kind: KindCheck, cause: CauseModel, message: err.Error()})
	} else {
		p.passed = true
	}
	return p
}

// subscribeStoreEvents subscribes to the Instance's session_loaded and
// session_saved Events of one Session.
func subscribeStoreEvents(ctx context.Context, runner *host.Runner, id string) (gotato.Subscription, error) {
	return runner.SubscribeInstance(ctx, gotato.SubscribeFilter(func(event gotato.Event) bool {
		switch event.(type) {
		case host.SessionLoaded, host.SessionSaved:
			return event.Head().SessionID == gotato.SessionID(id)
		}
		return false
	}))
}

// playPrompts runs each prompt on the Session in turn and returns the final
// texts of the Runs and the play's tool calls.
func (p *play) playPrompts(ctx context.Context, runner *host.Runner, storeEvents gotato.Subscription, id string, prompts []string) ([]string, []toolCall) {
	var o playObserver
	toolStarts := map[gotato.ToolCallID]time.Time{}
	var answers []string
	for _, prompt := range prompts {
		started := time.Now()
		o.started, o.requested, o.streaming = started, false, false
		result, err := runner.StreamRun(ctx, host.RunRequest{SessionID: id, Prompt: prompt}, func(event gotato.Event) error {
			o.observe(p, toolStarts, event)
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
	return answers, o.calls
}

// readFinal reads the Session back into p.final and its committed tool
// results by call into results.
func (p *play) readFinal(ctx context.Context, st store.Store, id string, results map[gotato.ToolCallID]gotato.ToolResult) {
	final, err := store.Open(ctx, st.Records, id)
	if err != nil {
		p.failures = append(p.failures, failure{kind: KindSetup, cause: CauseStore, message: "reading the Session back: " + err.Error()})
		return
	}
	p.final = final
	for _, message := range final.Messages() {
		if message.ToolResult != nil {
			results[message.ToolResult.CallID] = *message.ToolResult
		}
	}
}

// addToolFailures counts the play's tool calls and adds a failure for each
// that failed.
func (p *play) addToolFailures(calls []toolCall, results map[gotato.ToolCallID]gotato.ToolResult) {
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
}

// playObserver reads one play's Run Events into its play. It holds no
// pointer to the play or to the tool start times, so that neither leaves the
// stack.
type playObserver struct {
	calls   []toolCall
	waiting time.Time
	turn    int
	// started, requested and streaming belong to the current Run.
	started              time.Time
	requested, streaming bool
}

// observe reads one Run Event into p; toolStarts holds when each tool call
// started.
func (o *playObserver) observe(p *play, toolStarts map[gotato.ToolCallID]time.Time, event gotato.Event) {
	switch e := event.(type) {
	case gotato.TurnStarted:
		p.turns++
	case gotato.ContextBuilt:
		o.contextBuilt(p, e)
	case gotato.TextDelta:
		o.textDelta(p, e)
	case gotato.ReplyEnded:
		o.modelEnded(p, e.Time)
	case gotato.ToolStarted:
		toolStarts[e.CallID] = e.Time
	case gotato.ToolEnded:
		o.toolEnded(p, toolStarts, e)
	case gotato.TurnEnded:
		o.turnEnded(p, e)
	case gotato.ReplyCutOff:
		o.modelEnded(p, e.Time)
	case gotato.RunEnded:
		o.modelEnded(p, e.Time)
	}
}

// contextBuilt records a Turn's build and starts the model's time.
func (o *playObserver) contextBuilt(p *play, e gotato.ContextBuilt) {
	p.builds = append(p.builds, build{messages: e.SourceMessages, prefix: e.PrefixHash})
	o.waiting, o.streaming = e.Time, false
	if !o.requested {
		p.stats.addBeforeRequest(e.Time.Sub(o.started))
		o.requested = true
	}
}

// textDelta adds the model's time to its first token.
func (o *playObserver) textDelta(p *play, e gotato.TextDelta) {
	// The first text delta of a Turn: the model's time to its
	// first token.
	if !o.streaming && !o.waiting.IsZero() {
		p.stats.addFirstToken(e.Time.Sub(o.waiting))
		o.streaming = true
	}
}

// modelEnded adds the model's time when the model's reply ended at end.
func (o *playObserver) modelEnded(p *play, end time.Time) {
	if !o.waiting.IsZero() {
		p.stats.addModel(end.Sub(o.waiting))
		o.waiting = time.Time{}
	}
}

// toolEnded adds a tool's time and records its call.
func (o *playObserver) toolEnded(p *play, toolStarts map[gotato.ToolCallID]time.Time, e gotato.ToolEnded) {
	if since, ok := toolStarts[e.CallID]; ok {
		p.stats.addTool(e.Time.Sub(since))
	}
	o.calls = append(o.calls, toolCall{id: e.CallID, toolID: e.ToolID, status: string(e.Status), executed: e.Executed})
}

// turnEnded adds a later Turn's input and cached tokens.
func (o *playObserver) turnEnded(p *play, e gotato.TurnEnded) {
	// A play's first Turn has nothing cached yet; B1 counts
	// from the second on.
	if o.turn > 0 {
		p.laterInput += e.Usage.InputTokens
		p.laterCached += e.Usage.CacheReadTokens
	}
	o.turn++
}

// storeEventsTimeout bounds the wait for the store Events of one Run.
const storeEventsTimeout = 10 * time.Second

// readStoreTimes adds the store's time for one Run: the Instance's
// session_loaded, and the session_saved that ends the Run.
func readStoreTimes(ctx context.Context, events gotato.Subscription, stats *stats) {
	ctx, cancel := context.WithTimeout(ctx, storeEventsTimeout)
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
