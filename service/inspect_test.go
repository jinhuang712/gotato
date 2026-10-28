package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// briefNote is a ContextTransformer that puts a note in front of the history.
type briefNote struct{}

func (briefNote) Transform(_ context.Context, snapshot gotato.ContextSnapshot) ([]gotato.Message, error) {
	return append([]gotato.Message{gotato.UserMessage("note: answer briefly")}, snapshot.Messages...), nil
}

// TestInspectMatchesTheNextRun checks GOALS.md G-G03 at the Runner: what
// Inspect reports is what the next Run's first Turn sends, Extensions and
// model options included.
func TestInspectMatchesTheNextRun(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("on its way"))
	maxTokens := gotato.ModelConfig{MaxTokens: 256}
	runner, _ := newRunner(t, service.Agent{
		Name:        "orders",
		Model:       model,
		Instruction: "You track orders.",
		Tools:       []gotato.Tool{testkit.NewFakeTool("orders.lookup", "shipped")},
		Options:     []gotato.SessionOption{gotato.WithExtension(briefNote{}), gotato.WithModelConfig(maxTokens)},
	})
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "orders", nil, session.WithMessages(gotato.UserMessage("where is order 42?")))
	if err != nil {
		t.Fatal(err)
	}

	report, err := runner.Inspect(ctx, s.ID(), service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Request.Config.MaxTokens != 256 || gotato.TextOf(report.Request.Messages[0]) != "note: answer briefly" {
		t.Fatalf("the report left out the Spec's Extensions or model options: %+v", report.Request)
	}
	if report.CompactionPending || report.CompactCeilingTokens != 0 {
		t.Fatalf("no compaction is configured: %+v", report)
	}

	var prefix any
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Continue: true}, func(event gotato.Event) error {
		if built, ok := event.(gotato.ContextBuilt); ok {
			prefix = built.PrefixHash
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if prefix != report.PrefixHash {
		t.Fatalf("context_built prefix %v, report prefix %s", prefix, report.PrefixHash)
	}
	sent, _ := model.LastRequest()
	want, _ := json.Marshal(report.Request)
	got, _ := json.Marshal(sent)
	if string(got) != string(want) {
		t.Fatalf("the Run sent a different request\n got: %s\nwant: %s", got, want)
	}
}

func TestInspectSaysWhenCompactionIsPending(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{CeilingTokens: 20000}})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Inspect(ctx, first.SessionID, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if report.CompactionPending || report.CompactCeilingTokens != 20000 {
		t.Fatalf("a short Session is under the ceiling: %+v", report)
	}
	for range 2 {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: strings.Repeat("word ", 4000)}); err != nil {
			t.Fatal(err)
		}
	}
	report, err = runner.Inspect(ctx, first.SessionID, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.CompactionPending {
		t.Fatalf("a Session past the ceiling must report a pending compaction: approx %d tokens", report.ApproxTokens)
	}
}
