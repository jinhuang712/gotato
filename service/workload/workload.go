// Package workload drives the reference workload that GOALS.md measures
// through a service.Runner, and reports what it saw: how each task went, every
// failed Run, tool call and check with its cause, token usage and cost, the
// time Gotato adds per Turn next to the model's own time, the time to load a
// Session, and whether each Turn's Context matches an inspection of the
// Transcript it was built from (G-G03).
//
// In CI the tasks from testkit.ReferenceTasks run with their fake Models. The
// live run passes a real Model and its price:
//
//	report, err := workload.Run(ctx, workload.Config{
//	    Model:     client,
//	    ModelName: "your-model",
//	    Price:     workload.Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
//	}, testkit.ReferenceTasks())
//
// Every number about a Run comes from the Runner's Events and RunResults,
// including the store's time to load and save the Session (session_loaded and
// session_saved), so a team can compute the same numbers from its own
// telemetry.
package workload

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
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
	Store session.Store
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

// Price is what a model charges, in US dollars per million tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Cost is what usage costs at this price. Usage.InputTokens counts cached
// tokens too, as every gateway wire API reports it, so cache reads and writes
// are taken out of it and priced on their own.
func (p Price) Cost(usage gotato.Usage) float64 {
	cached := min(usage.InputTokens, usage.CacheReadTokens+usage.CacheWriteTokens)
	return (float64(usage.InputTokens-cached)*p.Input +
		float64(usage.OutputTokens)*p.Output +
		float64(usage.CacheReadTokens)*p.CacheRead +
		float64(usage.CacheWriteTokens)*p.CacheWrite) / 1e6
}

// Report is what one workload run measured.
type Report struct {
	Model       string `json:"model"`
	Price       *Price `json:"price,omitempty"`
	Plays       int    `json:"plays"`
	Concurrency int    `json:"concurrency"`
	// GotatoFailures counts the failures caused by Gotato or the store, over
	// every task. GOALS.md G-G04 wants 0.
	GotatoFailures int `json:"gotato_failures"`
	// ContextMismatches counts the Turns whose Context differed from its
	// inspection, over every task. GOALS.md G-G03 wants 0.
	ContextMismatches int          `json:"context_mismatches"`
	Tasks             []TaskReport `json:"tasks"`
}

// TaskReport is what the plays of one task measured.
type TaskReport struct {
	Task string `json:"task"`
	// Plays and Passed count the plays, and those whose check held.
	Plays     int `json:"plays"`
	Passed    int `json:"passed"`
	Runs      int `json:"runs"`
	Turns     int `json:"turns"`
	ToolCalls int `json:"tool_calls"`
	// Failures groups every failed Run, tool call and check by kind, cause
	// and code.
	Failures []Failure    `json:"failures,omitempty"`
	Usage    gotato.Usage `json:"usage"`
	CostUSD  float64      `json:"cost_usd,omitempty"`
	// CacheHitRate is cached input tokens ÷ input tokens, from each play's
	// second Turn on (G-B01). It is absent when the model reports no usage.
	CacheHitRate *float64 `json:"cache_hit_rate,omitempty"`
	// ContextTurns counts the Turns compared with an inspection, and
	// ContextMismatches those that differed; ContextDetail describes the
	// first difference.
	ContextTurns      int    `json:"context_turns"`
	ContextMismatches int    `json:"context_mismatches"`
	ContextDetail     string `json:"context_detail,omitempty"`
	Time              Timing `json:"time"`
}

// Timing splits the time of a task's Runs, in milliseconds summed over its
// plays.
type Timing struct {
	// ElapsedMS is the wall time of all plays, from the first start to the
	// last end. Under load it is less than RunMS.
	ElapsedMS float64 `json:"elapsed_ms"`
	// RunMS is the wall time of every Run, summed.
	RunMS float64 `json:"run_ms"`
	// ModelMS is the model's own time: inside Stream and every Recv.
	ModelMS float64 `json:"model_ms"`
	ToolMS  float64 `json:"tool_ms"`
	// LoadMS and SaveMS are the store's time to load and save the Session.
	LoadMS float64 `json:"load_ms"`
	SaveMS float64 `json:"save_ms"`
	// GotatoMS is the rest of RunMS: building the Context, assembling the
	// request, the Loop, and saving.
	GotatoMS float64 `json:"gotato_ms"`
	// GotatoPerTurnMS is Gotato's time per Turn (G-B05).
	GotatoPerTurnMS float64 `json:"gotato_per_turn_ms"`
	// LoadPerRunMS is the cold-load time before each Run (G-B07).
	LoadPerRunMS float64 `json:"load_per_run_ms"`
	// BeforeRequestPerRunMS is the time from a Run's start to its first
	// context_built, when the first request goes to the model: what Gotato
	// adds before the first token (G-B06).
	BeforeRequestPerRunMS float64 `json:"before_request_per_run_ms"`
	// ModelFirstTokenMS is the model's own time to its first text token, from
	// a Turn's context_built to its first message_update, averaged over the
	// Turns that stream text (G-B06).
	ModelFirstTokenMS float64 `json:"model_first_token_ms"`
	// GotatoToModel and LoadToModel compare GotatoMS and LoadMS with the
	// model's own time. They are absent when the model took no measurable
	// time, as with the fake models.
	GotatoToModel *float64 `json:"gotato_to_model,omitempty"`
	LoadToModel   *float64 `json:"load_to_model,omitempty"`
}

// Kind is what failed.
type Kind string

const (
	KindSetup    Kind = "setup"     // the driver could not prepare or read back the Session
	KindRun      Kind = "run"       // a Run did not complete
	KindToolCall Kind = "tool_call" // a tool call returned a failed result
	KindCheck    Kind = "check"     // the answers missed the task's goal
)

// Cause is who a failure is attributed to.
type Cause string

const (
	// CauseModel: the model got a tool call wrong, or its answers missed the
	// task's goal.
	CauseModel Cause = "model"
	// CauseProvider: a model call failed after the gateway's retries, or its
	// reply could not be read.
	CauseProvider Cause = "provider"
	// CauseTool: the tool ran and failed.
	CauseTool Cause = "tool"
	// CauseStore: the store could not load or save the Session.
	CauseStore Cause = "store"
	// CauseLimit: a Run reached a limit or its deadline.
	CauseLimit Cause = "limit"
	// CauseExtension: an Extension failed or refused a tool call.
	CauseExtension Cause = "extension"
	// CauseCancelled: the caller cancelled.
	CauseCancelled Cause = "cancelled"
	// CauseGotato: everything else. GOALS.md G-G04 wants none.
	CauseGotato Cause = "gotato"
)

// Failure is one group of failures of the same kind, cause and code.
type Failure struct {
	Kind  Kind   `json:"kind"`
	Cause Cause  `json:"cause"`
	Code  string `json:"code,omitempty"`
	Count int    `json:"count"`
	// Example is the message of the first failure in the group.
	Example string `json:"example"`
}

// Run plays every task and reports what it measured. A failed Run, tool call
// or check is reported, not returned; Run returns an error only when it cannot
// build a task's Runner.
func Run(ctx context.Context, cfg Config, tasks []testkit.Task) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg.Plays = max(cfg.Plays, 1)
	cfg.Concurrency = max(cfg.Concurrency, 1)
	if cfg.Store == nil {
		cfg.Store = session.NewMemoryStore()
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

func runTask(ctx context.Context, cfg Config, task testkit.Task) (TaskReport, error) {
	model := task.Model
	if cfg.Model != nil {
		model = cfg.Model
	}
	spec := service.Agent{
		Name:        task.Name,
		Model:       model,
		ModelName:   cfg.ModelName,
		Instruction: task.Instruction,
		Tools:       task.Tools,
		Limits:      task.Limits,
	}
	runner, err := service.New(service.Config{Store: cfg.Store, Agents: []service.Agent{spec}})
	if err != nil {
		return TaskReport{}, err
	}
	// The inspector rebuilds each Turn's Transcript in a store of its own and
	// inspects it with the same Spec.
	inspectStore := session.NewMemoryStore()
	inspector, err := service.New(service.Config{Store: inspectStore, Agents: []service.Agent{spec}})
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
	failures := newFailureSet()
	var total stats
	var laterInput, laterCached uint64
	for _, p := range plays {
		if p.passed {
			report.Passed++
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
		if !cfg.SkipContextCheck && p.final != nil {
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
	final       *session.Record
}

// build is one Turn's context_built Event: how many committed Messages the
// Context was built from, and the prefix hash of the request.
type build struct {
	messages int
	prefix   string
}

type failure struct {
	kind    Kind
	cause   Cause
	code    string
	message string
}

func playTask(ctx context.Context, runner *service.Runner, store session.Store, task testkit.Task) play {
	p := play{stats: &stats{}}
	s, err := runner.CreateSession(ctx, task.Name, nil)
	if err == nil && len(task.History) > 0 {
		for _, message := range task.History {
			_ = s.Append(message)
		}
		err = store.Save(ctx, s)
	}
	if err != nil {
		p.failures = append(p.failures, failure{kind: KindSetup, cause: CauseStore, message: err.Error()})
		return p
	}

	// Every number about a Run comes from its Events, as a team would
	// compute it from its own telemetry. The model's time runs from a Turn's
	// context_built to its reply's message_end, or to run_end when the
	// call failed; a tool's time runs from tool_execution_start to
	// tool_execution_end; the store's time is in session_loaded and
	// session_saved.
	var answers []string
	var calls []toolCall
	var waiting time.Time
	toolStarts := map[gotato.ToolCallID]time.Time{}
	toolIDs := map[gotato.ToolCallID]string{}
	turn := 0
	for _, prompt := range task.Prompts {
		started := time.Now()
		requested, streaming := false, false
		result, err := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt}, func(event gotato.Event) error {
			switch event.Kind {
			case gotato.EventSessionLoaded:
				p.stats.addLoad(elapsed(event))
			case gotato.EventSessionSaved:
				p.stats.addSave(elapsed(event))
			case gotato.EventTurnStart:
				p.turns++
			case gotato.EventContextBuilt:
				prefix, _ := event.Payload["prefix_hash"].(string)
				p.builds = append(p.builds, build{messages: int(number(event.Payload["source_messages"])), prefix: prefix})
				waiting, streaming = event.Timestamp, false
				if !requested {
					p.stats.addBeforeRequest(event.Timestamp.Sub(started))
					requested = true
				}
			case gotato.EventMessageUpdate:
				// The first text delta of a Turn: the model's time to its
				// first token.
				if !streaming && !waiting.IsZero() {
					p.stats.addFirstToken(event.Timestamp.Sub(waiting))
					streaming = true
				}
			case gotato.EventMessageEnd:
				// The model's reply carries tool_calls; a prompt or a steered
				// message carries its role instead.
				if _, reply := event.Payload["tool_calls"]; reply && !waiting.IsZero() {
					p.stats.addModel(event.Timestamp.Sub(waiting))
					waiting = time.Time{}
				}
			case gotato.EventToolExecutionStart:
				toolStarts[event.ToolCallID] = event.Timestamp
				toolIDs[event.ToolCallID], _ = event.Payload["tool_id"].(string)
			case gotato.EventToolExecutionEnd:
				if since, ok := toolStarts[event.ToolCallID]; ok {
					p.stats.addTool(event.Timestamp.Sub(since))
				}
				executed, _ := event.Payload["executed"].(bool)
				calls = append(calls, toolCall{id: event.ToolCallID, toolID: toolIDs[event.ToolCallID], status: fmt.Sprint(event.Payload["status"]), executed: executed})
			case gotato.EventTurnEnd:
				// A play's first Turn has nothing cached yet; G-B01 counts
				// from the second on.
				if turn > 0 {
					summary, _ := event.Payload["summary"].(map[string]any)
					p.laterInput += number(summary["input_tokens"])
					p.laterCached += number(summary["cache_read_tokens"])
				}
				turn++
			case gotato.EventRunEnd:
				if !waiting.IsZero() {
					p.stats.addModel(event.Timestamp.Sub(waiting))
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
	}

	// The committed tool results add the text of each failure to the
	// report; the Events have already decided its cause.
	results := map[gotato.ToolCallID]gotato.ToolResult{}
	final, err := store.Get(ctx, s.ID())
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

// toolCall is one tool_execution_end Event.
type toolCall struct {
	id       gotato.ToolCallID
	toolID   string
	status   string
	executed bool
}

// checkContext inspects, for every Turn, the Transcript that Turn was built
// from (the Session's first Messages, since a reference task never compacts)
// and compares its prefix hash with the Turn's context_built Event (G-G03).
func checkContext(ctx context.Context, inspector *service.Runner, store session.Store, final *session.Record, builds []build, report *TaskReport) {
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
		s := session.New()
		for key, value := range final.Metadata() {
			s.Set(key, value)
		}
		for _, message := range messages[:b.messages] {
			_ = s.Append(message)
		}
		if err := store.Save(ctx, s); err != nil {
			mismatch("saving the Transcript to inspect: " + err.Error())
			continue
		}
		inspection, err := inspector.Inspect(ctx, s.ID())
		_ = store.Delete(ctx, s.ID())
		switch {
		case err != nil:
			mismatch("inspecting: " + err.Error())
		case inspection.PrefixHash != b.prefix:
			mismatch(fmt.Sprintf("the Turn built from %d messages sent prefix %s, inspection shows %s", b.messages, b.prefix, inspection.PrefixHash))
		}
	}
}

// runFailure attributes a Run that did not complete.
func runFailure(result service.RunResult, err error) (failure, bool) {
	switch {
	case errors.Is(err, service.ErrNotPersisted):
		return failure{kind: KindRun, cause: CauseStore, code: "not_persisted", message: err.Error()}, true
	case errors.Is(err, session.ErrNotFound):
		return failure{kind: KindRun, cause: CauseStore, code: "not_found", message: err.Error()}, true
	case err == nil && result.Result.Status == gotato.RunCompleted:
		return failure{}, false
	}
	var code gotato.ErrorCode
	message := string(result.Result.Status)
	if result.Result.Error != nil {
		code, message = result.Result.Error.Code, result.Result.Error.Message
	} else if err != nil {
		var runtimeErr *gotato.RuntimeError
		if errors.As(err, &runtimeErr) {
			code = runtimeErr.Code
		}
		message = err.Error()
	}
	return failure{kind: KindRun, cause: causeOf(code), code: string(code), message: message}, true
}

// causeOf attributes a Run's error code.
func causeOf(code gotato.ErrorCode) Cause {
	switch code {
	case gotato.ErrModelFailure, gotato.ErrModelProtocolFailure:
		return CauseProvider
	case gotato.ErrToolExecutionFailure:
		return CauseTool
	case gotato.ErrToolResolutionFailure, gotato.ErrToolArgumentFailure:
		return CauseModel
	case gotato.ErrExtensionFailure:
		return CauseExtension
	case gotato.ErrLimitExceeded, gotato.ErrDeadlineExceeded:
		return CauseLimit
	case gotato.ErrCancelled:
		return CauseCancelled
	default:
		return CauseGotato
	}
}

// toolFailure attributes a tool call from its tool_execution_end Event: a
// call that failed before the tool ran was refused by Gotato for an unknown
// tool or bad arguments, so it is the model's; a tool that ran and failed is
// the tool's. The committed result, when there is one, gives the failure its
// text and code, and a result that reports an internal invariant is Gotato's.
func toolFailure(call toolCall, committed *gotato.ToolResult) (failure, bool) {
	f := failure{kind: KindToolCall, message: call.toolID + ": " + call.status}
	if committed != nil {
		f.message = committed.SafeError
		f.code = string(codePrefix(committed.SafeError))
	}
	switch {
	case call.status == string(gotato.ToolResultOK):
		return failure{}, false
	case call.status == string(gotato.ToolResultBlocked):
		f.cause = CauseExtension
	case call.status == string(gotato.ToolResultCanceled):
		f.cause = CauseCancelled
	case f.code == string(gotato.ErrInternalInvariant):
		f.cause = CauseGotato
	case call.executed:
		f.cause = CauseTool
	default:
		f.cause = CauseModel
	}
	return f, true
}

// codePrefix reads the error code a Gotato error message starts with, as in
// "tool_argument_validation_failure: …" or "tool_execution_failure (Tool): …".
func codePrefix(message string) gotato.ErrorCode {
	end := strings.IndexAny(message, ": (")
	if end <= 0 {
		return ""
	}
	switch code := gotato.ErrorCode(message[:end]); code {
	case gotato.ErrToolArgumentFailure, gotato.ErrToolResolutionFailure, gotato.ErrToolExecutionFailure, gotato.ErrInternalInvariant:
		return code
	}
	return ""
}

// failureSet groups failures by kind, cause and code, in the order first seen.
type failureSet struct {
	order  []string
	groups map[string]*Failure
}

func newFailureSet() *failureSet { return &failureSet{groups: map[string]*Failure{}} }

func (s *failureSet) add(f failure) {
	key := string(f.kind) + "/" + string(f.cause) + "/" + f.code
	group, ok := s.groups[key]
	if !ok {
		group = &Failure{Kind: f.kind, Cause: f.cause, Code: f.code, Example: f.message}
		s.groups[key] = group
		s.order = append(s.order, key)
	}
	group.Count++
}

func (s *failureSet) list() []Failure {
	out := make([]Failure, 0, len(s.order))
	for _, key := range s.order {
		out = append(out, *s.groups[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
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

// elapsed reads the store's time from a session_loaded or session_saved
// Event.
func elapsed(event gotato.Event) time.Duration {
	ms, _ := event.Payload["elapsed_ms"].(float64)
	return time.Duration(ms * float64(time.Millisecond))
}

// number reads a numeric Event payload value, in Go or as decoded from JSON.
func number(value any) uint64 {
	switch typed := value.(type) {
	case int:
		return uint64(max(typed, 0))
	case int64:
		return uint64(max(typed, 0))
	case uint32:
		return uint64(typed)
	case uint64:
		return typed
	case float64:
		return uint64(max(typed, 0))
	}
	return 0
}
