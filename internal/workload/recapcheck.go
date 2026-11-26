package workload

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
)

// RecapCheckConfig says how to run the Recap check (GOALS.md B12).
type RecapCheckConfig struct {
	// Model replaces the check's fake Model: the check on your own gateway.
	Model gotato.Model
	// ModelName names the Model in the report.
	ModelName string
	// Store holds the Session. Nil uses a memory store.
	Store store.Store
	// MaxTokens is the Recap's output limit; 0 keeps the default (8K).
	MaxTokens uint32
}

// RecapScore is how many questions an answer set got right.
type RecapScore struct {
	Correct int     `json:"correct"`
	Share   float64 `json:"share"`
}

// RecapAnswer is one question with the answers before and after the
// compaction.
type RecapAnswer struct {
	Ask           string `json:"ask"`
	Want          string `json:"want"`
	Before        string `json:"before"`
	BeforeCorrect bool   `json:"before_correct"`
	After         string `json:"after"`
	AfterCorrect  bool   `json:"after_correct"`
}

// RecapReport is what the Recap check measured: the share of the questions,
// whose answers appeared only in Tool Results, that the model answers
// correctly before the compaction and after it.
type RecapReport struct {
	Check     string        `json:"check"`
	Model     string        `json:"model"`
	Questions int           `json:"questions"`
	Before    RecapScore    `json:"before"`
	After     RecapScore    `json:"after"`
	Answers   []RecapAnswer `json:"answers"`
	// Replaced says whether the compaction closed a Segment, and RecapWritten
	// whether the model wrote a Recap that passed the rules. Without one, the
	// check measured nothing about the Recap.
	Replaced     bool   `json:"replaced"`
	RecapWritten bool   `json:"recap_written"`
	Recap        string `json:"recap,omitempty"`
	// RecapUsage is what the model call that wrote the Recap used, so a team
	// sees whether it read the cache.
	RecapUsage   *gotato.Usage `json:"recap_usage,omitempty"`
	TokensBefore int           `json:"tokens_before"`
	TokensAfter  int           `json:"tokens_after"`
}

// RunRecapCheck plays the check: it runs the conversation on a Session, asks
// the questions, compacts the Session with the Recap written by the Agent's
// model, and asks the same questions again. Each question runs on the Session
// without being saved, and with the tools switched off, so the model answers
// from the conversation or from the Recap and cannot ask a tool again. It
// returns an error only when the conversation or the compaction cannot run; a
// wrong answer is a result.
//
// This check turns the model's Recap on for its own Agent. The model's Recap
// stays off everywhere else until a team has run this check on its gateway.
func RunRecapCheck(ctx context.Context, cfg RecapCheckConfig, check reftasks.RecapCheck) (RecapReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	model := check.Model
	if cfg.Model != nil {
		model = cfg.Model
	}
	if model == nil {
		return RecapReport{}, errors.New("workload: the Recap check has no Model")
	}
	st := cfg.Store
	if st.Records == nil || st.Liveness == nil {
		st = memoryStore()
	}
	runner, questions, err := newRecapRunner(cfg, check, model, st)
	if err != nil {
		return RecapReport{}, err
	}

	r := recapRun{runner: runner, check: check, questions: questions}
	r.report = RecapReport{Check: check.Name, Model: cfg.ModelName, Questions: len(check.Questions)}
	if r.report.Model == "" && cfg.Model == nil {
		r.report.Model = "fake"
	}
	err = r.play(ctx, st)
	return r.report, err
}

// play runs the conversation, asks the questions, compacts the Session and
// asks them again, and scores the answers.
func (r *recapRun) play(ctx context.Context, st store.Store) error {
	if err := r.converse(ctx); err != nil {
		return err
	}
	r.report.Answers = make([]RecapAnswer, len(r.check.Questions))
	if err := r.ask(ctx, func(a *RecapAnswer, text string, ok bool) { a.Before, a.BeforeCorrect = text, ok }); err != nil {
		return err
	}
	if err := r.compact(ctx, st); err != nil {
		return err
	}
	if err := r.ask(ctx, func(a *RecapAnswer, text string, ok bool) { a.After, a.AfterCorrect = text, ok }); err != nil {
		return err
	}
	r.report.score()
	return nil
}

// newRecapRunner builds the Runner of the check, with the conversation's
// Agent and the questions' Agent, and returns the questions' Agent name.
func newRecapRunner(cfg RecapCheckConfig, check reftasks.RecapCheck, model gotato.Model, st store.Store) (*host.Runner, string, error) {
	switchedOff := make([]gotato.Tool, len(check.Tools))
	for i, tool := range check.Tools {
		switchedOff[i] = toolOff{tool}
	}
	conversation := profile.AgentProfile{
		Name: check.Name, Model: model, ModelName: cfg.ModelName, Instruction: check.Instruction, Tools: check.Tools,
		Compact: record.Compactor{Recap: record.ModelRecap{MaxTokens: cfg.MaxTokens}},
	}
	// Same model, instruction and tool definitions, so the questions read the
	// conversation's prefix from the cache; only the tools' results differ.
	questions := conversation
	questions.Name, questions.Tools, questions.Compact = check.Name+"-questions", switchedOff, record.Compactor{}
	runner, err := host.New(withAgents(host.Config{Store: st}, []profile.AgentProfile{conversation, questions}...))
	return runner, questions.Name, err
}

// recapRun is one play of the Recap check: its Runner, the Session it plays
// on and the report it fills.
type recapRun struct {
	runner    *host.Runner
	check     reftasks.RecapCheck
	questions string
	id        string
	report    RecapReport
}

// converse runs the conversation's prompts on a new Session.
func (r *recapRun) converse(ctx context.Context) error {
	for _, prompt := range r.check.Prompts {
		result, err := r.runner.Run(ctx, host.RunRequest{SessionID: r.id, Agent: r.check.Name, Prompt: prompt})
		if err != nil {
			return fmt.Errorf("workload: %s: the conversation failed: %w", r.check.Name, err)
		}
		if result.Result.Status != gotato.RunStatusCompleted {
			return fmt.Errorf("workload: %s: a Run of the conversation settled as %s", r.check.Name, result.Result.Status)
		}
		r.id = result.SessionID
	}
	return nil
}

// ask asks every question without saving, and sets each answer with set.
func (r *recapRun) ask(ctx context.Context, set func(*RecapAnswer, string, bool)) error {
	for i, question := range r.check.Questions {
		result, err := r.runner.Run(ctx, host.RunRequest{SessionID: r.id, Agent: r.questions, SkipSave: true, Prompt: reftasks.QuestionPrefix + question.Ask})
		if err != nil {
			return fmt.Errorf("workload: %s: question %d failed: %w", r.check.Name, i+1, err)
		}
		r.report.Answers[i].Ask, r.report.Answers[i].Want = question.Ask, question.Want
		set(&r.report.Answers[i], result.FinalText, answered(result.FinalText, question.Want))
	}
	return nil
}

// compact compacts the Session and reports what the compaction did and the
// Recap it wrote.
func (r *recapRun) compact(ctx context.Context, st store.Store) error {
	compacted, err := r.runner.Compact(ctx, r.id, record.CompactConfig{})
	if err != nil {
		return fmt.Errorf("workload: %s: the compaction failed: %w", r.check.Name, err)
	}
	r.report.Replaced = compacted.Replaced
	r.report.TokensBefore, r.report.TokensAfter = compacted.TokensBefore, compacted.TokensAfter
	if compacted.Compaction != nil {
		r.report.RecapWritten = compacted.Compaction.Recap != "none"
		r.report.RecapUsage = compacted.Compaction.RecapUsage
	}
	if rec, err := store.View(ctx, st.Records, r.id); err == nil {
		if segments := rec.Segments(); len(segments) > 0 {
			r.report.Recap = segments[len(segments)-1].Recap
		}
	}
	return nil
}

// score counts the right answers before and after the compaction.
func (report *RecapReport) score() {
	for _, a := range report.Answers {
		if a.BeforeCorrect {
			report.Before.Correct++
		}
		if a.AfterCorrect {
			report.After.Correct++
		}
	}
	if report.Questions > 0 {
		report.Before.Share = float64(report.Before.Correct) / float64(report.Questions)
		report.After.Share = float64(report.After.Correct) / float64(report.Questions)
	}
}

// answered reports whether an answer holds the fact, without case or spaces.
func answered(answer, want string) bool {
	squash := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), "")) }
	return want != "" && strings.Contains(squash(answer), squash(want))
}

// toolOff is a Tool with the same definition whose calls fail, for the
// questions: the model cannot ask a tool again what the compaction dropped.
type toolOff struct{ gotato.Tool }

// Execute implements gotato.Tool.
func (toolOff) Execute(context.Context, gotato.ToolUse, gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	return gotato.ToolResult{}, errors.New("the tools are switched off for this question")
}
