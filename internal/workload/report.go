package workload

import (
	"github.com/jinhuang712/gotato/gotato"
)

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
	// Window is present for a task with a window of its own
	// (reftasks.Task.Window): the size of its requests against it, and what
	// its compactions did (G-G10, G-B14, G-B16).
	Window *WindowReport `json:"window,omitempty"`
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
