package workload

import (
	"context"
	"strconv"
	"strings"
	"sync"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// WindowReport is what a task with a window (testkit.Task.Window) measured:
// the size of every request against the window of its fake Model, and what the
// compactions did (GOALS.md G-G10, G-B14 and G-B16). Sizes are tokens by the
// estimate the fake Model reports as its input (testkit.RequestTokens), not a
// provider's count.
type WindowReport struct {
	// Window is the Model's window, in tokens.
	Window int `json:"window"`
	// Requests counts the Turns' requests the Model accepted, and
	// OverWindow those it refused for being longer than the window (G-G10
	// wants 0). A request that asks for a Recap is counted apart.
	Requests    int `json:"requests"`
	OverWindow  int `json:"over_window"`
	RecapAsks   int `json:"recap_asks"`
	RecapOver   int `json:"recap_over_window"`
	MaxRequest  int `json:"max_request_tokens"`
	Compactions int `json:"compactions"`
	Merges      int `json:"merges"`
	// TokensAtTurn is the size of the request at the Turns G-B14 names, in
	// the first play: it should level off, not grow with the Session's age.
	TokensAtTurn []TurnTokens `json:"tokens_at_turn,omitempty"`
	// Recap is what the model's Recap cost, when the task turned it on
	// (G-B16).
	Recap *RecapCost `json:"recap,omitempty"`
}

// TurnTokens is the size of the request at one Turn.
type TurnTokens struct {
	Turn   int `json:"turn"`
	Tokens int `json:"tokens"`
}

// RecapCost is the cost of the calls that wrote the Recaps of a task's
// compactions and merges, in the tokens the fake Model reported, against what
// the compactions took out of the Context.
type RecapCost struct {
	Calls        int    `json:"calls"`
	InputTokens  uint64 `json:"input_tokens"`
	OutputTokens uint64 `json:"output_tokens"`
	// Closings counts the compactions that closed a Segment, and
	// TakenOutTokens is what they took out of the Context, summed: the
	// tokens before less the tokens after.
	Closings       int `json:"closings"`
	TakenOutTokens int `json:"taken_out_tokens"`
}

// turnsReported are the Turns whose request size G-B14 names.
var turnsReported = []int{1, 10, 100, 1000, 2000}

// windowModel gives a fake Model a window, counts every request against it,
// and refuses one over it as a provider would, before any output.
type windowModel struct {
	gotato.Model
	window int

	mu     sync.Mutex
	series map[string]*windowSeries
}

type windowSeries struct {
	tokens                              []int
	over, recapAsks, recapOver, largest int
}

func newWindowModel(model gotato.Model, window int) *windowModel {
	return &windowModel{Model: model, window: window, series: map[string]*windowSeries{}}
}

// ContextWindow gives the Runner the Model's window, so it takes the default
// ceiling from it.
func (m *windowModel) ContextWindow() int { return m.window }

// Stream implements gotato.Model.
func (m *windowModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	tokens := testkit.RequestTokens(request)
	recap := false
	if n := len(request.Messages); n > 0 {
		text := gotato.TextOf(request.Messages[n-1])
		recap = strings.HasPrefix(text, "Write a recap") || strings.HasPrefix(text, "Write one recap")
	}
	m.mu.Lock()
	s := m.series[request.CacheKey]
	if s == nil {
		s = &windowSeries{}
		m.series[request.CacheKey] = s
	}
	s.largest = max(s.largest, tokens)
	switch {
	case recap && tokens > m.window:
		s.recapAsks++
		s.recapOver++
	case recap:
		s.recapAsks++
	case tokens > m.window:
		s.over++
	default:
		s.tokens = append(s.tokens, tokens)
	}
	m.mu.Unlock()
	if tokens > m.window {
		return nil, refusal{limit: m.window, tokens: tokens}
	}
	return m.Model.Stream(ctx, request)
}

// seriesOf returns what the Model saw for the Session whose cache key it is.
func (m *windowModel) seriesOf(cacheKey string) windowSeries {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.series[cacheKey]; s != nil {
		return *s
	}
	return windowSeries{}
}

// refusal is how a provider refuses a request longer than its window.
type refusal struct{ limit, tokens int }

func (r refusal) Error() string {
	return "prompt is too long: " + strconv.Itoa(r.tokens) + " tokens > " + strconv.Itoa(r.limit) + " maximum"
}
func (refusal) ContextOverflow() bool { return true }
func (r refusal) ContextLimit() int   { return r.limit }

// add adds what one play's Model series and final Record show to
// the task's report.
func (r *WindowReport) add(s windowSeries, record *session.Record, first bool) {
	r.Requests += len(s.tokens)
	r.OverWindow += s.over
	r.RecapAsks += s.recapAsks
	r.RecapOver += s.recapOver
	r.MaxRequest = max(r.MaxRequest, s.largest)
	if first {
		for _, turn := range turnsReported {
			if turn <= len(s.tokens) {
				r.TokensAtTurn = append(r.TokensAtTurn, TurnTokens{Turn: turn, Tokens: s.tokens[turn-1]})
			}
		}
	}
	if record == nil {
		return
	}
	var cost RecapCost
	for _, c := range record.Compactions() {
		r.Compactions++
		if len(c.Merged) > 0 {
			r.Merges++
		}
		if c.RecapUsage != nil {
			cost.Calls++
			cost.InputTokens += c.RecapUsage.InputTokens
			cost.OutputTokens += c.RecapUsage.OutputTokens
		}
		if len(c.Merged) == 0 && c.Segment > 0 {
			cost.Closings++
			cost.TakenOutTokens += max(c.TokensBefore-c.TokensAfter, 0)
		}
	}
	if cost.Calls > 0 {
		if r.Recap == nil {
			r.Recap = &RecapCost{}
		}
		r.Recap.Calls += cost.Calls
		r.Recap.InputTokens += cost.InputTokens
		r.Recap.OutputTokens += cost.OutputTokens
		r.Recap.Closings += cost.Closings
		r.Recap.TakenOutTokens += cost.TakenOutTokens
	}
}
