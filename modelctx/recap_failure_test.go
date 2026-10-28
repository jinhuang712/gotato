package modelctx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

// emptyRecap is a writer that succeeds with no text.
type emptyRecap struct{}

func (emptyRecap) Name() string { return "empty" }
func (emptyRecap) WriteRecap(context.Context, modelctx.RecapRequest) (gotato.Message, error) {
	return gotato.AssistantMessage("  "), nil
}

// panickingRecap is a writer that panics.
type panickingRecap struct{}

func (panickingRecap) Name() string { return "panicking" }
func (panickingRecap) WriteRecap(context.Context, modelctx.RecapRequest) (gotato.Message, error) {
	panic("writer bug")
}

// A Recap that is missing says why in Compaction.RecapError, and "no writer"
// is told apart from "the writer failed".
func TestAMissingRecapSaysWhy(t *testing.T) {
	broken := func(context.Context) (gotato.ModelRequest, error) {
		return gotato.ModelRequest{}, context.DeadlineExceeded
	}
	failing := testkit.NewFakeModel(testkit.Text("x"))
	failing.Err = errors.New("provider down")
	cases := []struct {
		name    string
		writer  modelctx.RecapWriter
		context func(*fixture) func(context.Context) (gotato.ModelRequest, error)
		want    string
		recap   string
	}{
		{"no writer", nil, nil, "", "none"},
		{"a good recap", modelctx.ModelRecap{Model: testkit.NewFakeModel(testkit.Text(goodRecap))}, nil, "", "model"},
		{"the model fails", modelctx.ModelRecap{Model: failing}, nil, "model_failure", "none"},
		{"the reply is not a Recap", modelctx.ModelRecap{Model: testkit.NewFakeModel(testkit.Text("Sure!"))}, nil, "model_protocol_failure", "none"},
		{"the writer returns nothing", emptyRecap{}, nil, "empty_recap", "none"},
		{"the writer panics", panickingRecap{}, nil, "model_failure", "none"},
		{"the Context cannot be built", modelctx.ModelRecap{Model: testkit.NewFakeModel(testkit.Text(goodRecap))}, func(*fixture) func(context.Context) (gotato.ModelRequest, error) { return broken }, "deadline_exceeded", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			threeRuns(f)
			build := f.contextOf(nil)
			if tc.context != nil {
				build = tc.context(f)
			}
			result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: tc.writer}}, modelctx.WithRecapContext(build))
			if result.Compaction.Recap != tc.recap || result.Compaction.RecapError != tc.want {
				t.Fatalf("Recap = %q, RecapError = %q; want %q, %q", result.Compaction.Recap, result.Compaction.RecapError, tc.recap, tc.want)
			}
			var ended gotato.Compaction
			for _, event := range f.rec.Events() {
				if e, ok := event.(gotato.CompactionEnded); ok {
					ended = *e.Compaction
				}
			}
			if ended.RecapError != tc.want {
				t.Errorf("the compaction_ended Event says RecapError = %q, want %q", ended.RecapError, tc.want)
			}
		})
	}
}

// A ModelRecap with no Model is refused where it is used, with a clear error,
// not swallowed into a Segment that has no Recap.
func TestAModelRecapWithoutAModelIsRefused(t *testing.T) {
	policy := modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}}
	if err := policy.Validate(); !errors.Is(err, modelctx.ErrNoRecapModel) {
		t.Fatalf("Validate = %v", err)
	}
	if err := (modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: testkit.DemoModel{}, Timeout: -1}}).Validate(); err == nil {
		t.Error("a negative Timeout was accepted")
	}
	if err := (modelctx.CompactPolicy{}).Validate(); err != nil {
		t.Errorf("the zero policy is refused: %v", err)
	}
	f := newFixture(t)
	threeRuns(f)
	before := len(f.rec.Events())
	if _, err := modelctx.Compact(f.ctx, f.rec, modelctx.CompactConfig{CompactPolicy: policy}); !errors.Is(err, modelctx.ErrNoRecapModel) {
		t.Fatalf("Compact = %v, want ErrNoRecapModel", err)
	}
	if len(f.rec.Events()) != before || len(f.rec.Segments()) != 0 {
		t.Error("a refused compaction left Events or a Segment behind")
	}
	l := newLive(t, modelctx.CompactPolicy{CeilingTokens: 100, Recap: modelctx.ModelRecap{}})
	result, err := l.run(testkit.NewFakeModel(answering("hi")), "a prompt long enough to pass the ceiling "+string(make([]byte, 600)), "ok")
	if !errors.Is(err, modelctx.ErrNoRecapModel) || result.Status != gotato.RunStatusFailed {
		t.Fatalf("a Run with an unusable Recap: %+v, %v", result, err)
	}
}

// On an overflow the Recap is not asked for on the request just refused plus
// the ask: that one is refused again. The compaction says it skipped it.
func TestAnOverflowCompactionSkipsTheModelRecapAndSaysSo(t *testing.T) {
	recapModel := testkit.NewFakeModel(testkit.Text(goodRecap))
	l := newLive(t, modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: recapModel}})
	for _, prompt := range []string{"one", "two", "three"} {
		l.must(testkit.NewFakeModel(answering("reply to "+prompt)), prompt)
	}
	model := &stepModel{steps: []any{refusal{}, answering("done")}}
	if result, err := l.run(model, "four", "ok"); err != nil || result.Status != gotato.RunStatusCompleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	last, runs := l.auto.Last()
	if runs != 1 || last.Compaction == nil {
		t.Fatalf("compactions = %d", runs)
	}
	if last.Compaction.Recap != "none" || last.Compaction.RecapError != "skipped_overflow" {
		t.Errorf("Recap = %q, RecapError = %q", last.Compaction.Recap, last.Compaction.RecapError)
	}
	if recapModel.Calls() != 0 {
		t.Errorf("the Recap model was called %d times on a request it would refuse", recapModel.Calls())
	}
}

// stalledModel answers with headers and then nothing.
type stalledModel struct{}

func (stalledModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return stalledStream{}, nil
}

type stalledStream struct{}

func (stalledStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}
func (stalledStream) Close() error { return nil }

// A Recap call has a deadline: a stalled model ends the call, and the
// compaction goes on without a Recap.
func TestTheRecapModelCallHasADeadline(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	start := time.Now()
	result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: stalledModel{}, Timeout: 30 * time.Millisecond}}},
		modelctx.WithRecapContext(f.contextOf(nil)))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the compaction waited %v", elapsed)
	}
	if !result.Replaced || result.Compaction.Recap != "none" || result.Compaction.RecapError != "deadline_exceeded" {
		t.Fatalf("result = %+v", result.Compaction)
	}
}
