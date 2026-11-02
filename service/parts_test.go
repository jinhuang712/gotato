package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/ref"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// keepLast is a user's Cutter: only the last Message stays word for word.
type keepLast struct{}

func (keepLast) Cut(stretch []gotato.Message, _ modelctx.CutInfo) int { return len(stretch) - 1 }

// listBuilder is a user's Builder: one line per Message, no refs.
type listBuilder struct{}

func (listBuilder) Build(closed []gotato.Message, info modelctx.SegmentInfo) (gotato.Segment, error) {
	var lines []string
	for _, m := range closed {
		lines = append(lines, string(m.Role)+": "+gotato.TextOf(m))
	}
	return gotato.Segment{Messages: []gotato.Message{gotato.UserMessage(strings.Join(lines, "\n"))}}, nil
}

// A step is replaced without copying the rest: an Agent whose Compactor sets
// only a Cutter and a Builder gets them on demand, and the frame keeps its
// guarantees around them (the Segment is stamped and written once).
func TestAnAgentsCompactorPartsAreUsedOnDemand(t *testing.T) {
	agent := service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.Compactor{Cut: keepLast{}, Builder: listBuilder{}}}
	runner, store := newRunner(t, agent)
	ctx := context.Background()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	result, err := runner.Compact(ctx, id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	record, err := session.View(ctx, store.Records, id)
	if err != nil {
		t.Fatal(err)
	}
	if tail := record.Tail(); len(tail) != 1 {
		t.Fatalf("the Agent's Cutter kept %d Messages, want 1", len(tail))
	}
	segments := record.Segments()
	if len(segments) != 1 || segments[0].Number != 1 || segments[0].Messages[0].ID != "segment-1-1" || !strings.Contains(gotato.TextOf(segments[0].Messages[0]), "user: one") {
		t.Fatalf("Segments = %+v", segments)
	}
	if segments[0].Source.FromNumber == 0 {
		t.Fatalf("the frame did not stamp where the Segment's Messages are: %+v", segments[0].Source)
	}
	// An on-demand KeepMessages replaces the Cutter for that call alone.
	more, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "four"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err = runner.Compact(ctx, more.SessionID, modelctx.CompactConfig{KeepMessages: 2}); err != nil || !result.Replaced {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	if record, err = session.View(ctx, store.Records, id); err != nil || len(record.Tail()) != 2 {
		t.Fatalf("KeepMessages 2 kept %d Messages, %v", len(record.Tail()), err)
	}
}

// wrappedRecap is a user's writer around a ModelRecap: it gets the Agent's
// model through WithAgentModel, not through a match on ModelRecap's type.
type wrappedRecap struct{ inner modelctx.ModelRecap }

func (w wrappedRecap) Name() string { return "wrapped" }
func (w wrappedRecap) WriteRecap(ctx context.Context, request modelctx.RecapRequest) (gotato.Message, error) {
	return w.inner.WriteRecap(ctx, request)
}
func (w wrappedRecap) WithAgentModel(model gotato.Model) modelctx.RecapWriter {
	return wrappedRecap{inner: w.inner.WithAgentModel(model).(modelctx.ModelRecap)}
}
func (w wrappedRecap) Validate() error { return w.inner.Validate() }

func TestAWrappedRecapWriterGetsTheAgentsModel(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: windowedEcho{window: 1 << 20}, Compact: modelctx.Compactor{Recap: wrappedRecap{}}})
	ctx := context.Background()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	if result, err := runner.Compact(ctx, id, modelctx.CompactConfig{}); errors.Is(err, modelctx.ErrNoRecapModel) {
		t.Fatalf("compact = %+v, %v: the wrapped ModelRecap got no Model", result, err)
	}
}

// panicsOnModel is a writer whose WithAgentModel panics: the Runner keeps it as
// it was and the compaction goes on, rather than the Runner crashing.
type panicsOnModel struct{ modelctx.TruncateRecap }

func (panicsOnModel) WithAgentModel(gotato.Model) modelctx.RecapWriter { panic("no model for you") }

func TestARecapWriterThatPanicsOnTheAgentsModelDoesNotCrashTheRunner(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.Compactor{Recap: panicsOnModel{}}})
	ctx := context.Background()
	result, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Compact(ctx, result.SessionID, modelctx.CompactConfig{}); err != nil {
		t.Fatalf("compact: %v", err)
	}
}

// bigResult is a tool whose result is far under the default page and over a
// small one.
type bigResult struct{}

func (bigResult) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{ID: "demo.big", Description: "returns text", InputSchema: []byte(`{"type":"object"}`), Effect: gotato.ToolEffectRead}
}

func (bigResult) Execute(context.Context, gotato.ToolUse, gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	return gotato.ToolResult{Executed: true, Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: strings.Repeat("row of text\n", 500)}}}, nil
}

// unreadable is an Originals that finds nothing, to stand in for a user's.
type unreadable struct{}

func (unreadable) Ref(context.Context, ref.Ref) (gotato.Message, error) {
	return gotato.Message{}, modelctx.ErrNoOriginal
}

func (unreadable) Segment(context.Context, int) ([]gotato.Message, bool, error) {
	return nil, false, modelctx.ErrNoOriginal
}

// The ReadBack is replaceable: an Agent whose Options install one with a small
// page spills a result the default page holds whole. By default a result of 6 KB
// is not spilled.
func TestAnAgentsReadBackSetsThePageOfTheSpill(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		options []gotato.SessionOption
		spilled bool
	}{
		"the default page": {nil, false},
		"a page of 1 KiB":  {[]gotato.SessionOption{gotato.WithReadBack(modelctx.ReadBack{Originals: unreadable{}, PageSize: 1024})}, true},
	} {
		t.Run(name, func(t *testing.T) {
			call := testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "demo.big", Arguments: []byte(`{}`)})
			model := testkit.NewFakeModel(call, testkit.Text("done"))
			agent := service.Agent{Name: "big", Model: model, Tools: []gotato.Tool{bigResult{}}, Options: tc.options}
			runner, store := newRunner(t, agent)
			result, err := runner.Run(ctx, service.RunRequest{Prompt: "go"})
			if err != nil {
				t.Fatal(err)
			}
			record, err := session.View(ctx, store.Records, result.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			var spilled bool
			for _, m := range record.Messages() {
				if m.ToolResult != nil && m.ToolResult.Spilled != nil {
					spilled = true
				}
			}
			if spilled != tc.spilled {
				t.Fatalf("spilled = %v, want %v", spilled, tc.spilled)
			}
		})
	}
}
