package workload

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// RecapCheckConfig says how to run the Recap check (GOALS.md G-E01).
type RecapCheckConfig struct {
	// Model replaces the check's fake Model: the check on your own gateway.
	Model gotato.Model
	// ModelName names the Model in the report.
	ModelName string
	// Store holds the Session. Nil uses a memory store.
	Store session.Store
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
	store := cfg.Store
	if store == nil {
		store = session.NewMemoryStore()
	}
	switchedOff := make([]gotato.Tool, len(check.Tools))
	for i, tool := range check.Tools {
		switchedOff[i] = toolOff{tool}
	}
	conversation := service.Agent{
		Name: check.Name, Model: model, ModelName: cfg.ModelName, Instruction: check.Instruction, Tools: check.Tools,
		Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{MaxTokens: cfg.MaxTokens}},
	}
	// Same model, instruction and tool definitions, so the questions read the
	// conversation's prefix from the cache; only the tools' results differ.
	questions := conversation
	questions.Name, questions.Tools, questions.Compact = check.Name+"-questions", switchedOff, modelctx.CompactPolicy{}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{conversation, questions}})
	if err != nil {
		return RecapReport{}, err
	}

	report := RecapReport{Check: check.Name, Model: cfg.ModelName, Questions: len(check.Questions)}
	if report.Model == "" && cfg.Model == nil {
		report.Model = "fake"
	}
	var id string
	for _, prompt := range check.Prompts {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: check.Name, Prompt: prompt})
		if err != nil {
			return report, fmt.Errorf("workload: %s: the conversation failed: %w", check.Name, err)
		}
		if result.Result.Status != gotato.RunStatusCompleted {
			return report, fmt.Errorf("workload: %s: a Run of the conversation settled as %s", check.Name, result.Result.Status)
		}
		id = result.SessionID
	}
	report.Answers = make([]RecapAnswer, len(check.Questions))
	ask := func(set func(*RecapAnswer, string, bool)) error {
		for i, question := range check.Questions {
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Agent: questions.Name, SkipSave: true, Prompt: reftasks.QuestionPrefix + question.Ask})
			if err != nil {
				return fmt.Errorf("workload: %s: question %d failed: %w", check.Name, i+1, err)
			}
			report.Answers[i].Ask, report.Answers[i].Want = question.Ask, question.Want
			set(&report.Answers[i], result.FinalText, answered(result.FinalText, question.Want))
		}
		return nil
	}
	if err := ask(func(a *RecapAnswer, text string, ok bool) { a.Before, a.BeforeCorrect = text, ok }); err != nil {
		return report, err
	}
	compacted, err := runner.Compact(ctx, id, modelctx.CompactOptions{})
	if err != nil {
		return report, fmt.Errorf("workload: %s: the compaction failed: %w", check.Name, err)
	}
	report.Replaced = compacted.Replaced
	report.TokensBefore, report.TokensAfter = compacted.TokensBefore, compacted.TokensAfter
	if compacted.Compaction != nil {
		report.RecapWritten = compacted.Compaction.Recap != "none"
		report.RecapUsage = compacted.Compaction.RecapUsage
	}
	if record, err := session.View(ctx, store, id); err == nil {
		if segments := record.Segments(); len(segments) > 0 {
			report.Recap = segments[len(segments)-1].Recap
		}
	}
	if err := ask(func(a *RecapAnswer, text string, ok bool) { a.After, a.AfterCorrect = text, ok }); err != nil {
		return report, err
	}
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
	return report, nil
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
