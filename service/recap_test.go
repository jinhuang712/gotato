package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	itext "github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

const agentRecap = "## What happened\n- the user asked three things\n## Key facts\n- order 42 is shipped\n"

// isAsk reports whether the last message of a request is the Recap's ask.
func isAsk(request gotato.ModelRequest) bool {
	n := len(request.Messages)
	return n > 0 && strings.HasPrefix(gotato.TextOf(request.Messages[n-1]), itext.RecapAskPrefix)
}

// recapModel answers a Recap ask with agentRecap and every other request with
// a short reply.
func recapModel() *testkit.FuncModel {
	return testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		if isAsk(request) {
			return testkit.Text(agentRecap), nil
		}
		return testkit.Text("ok"), nil
	})
}

func asks(model *testkit.FuncModel) []gotato.ModelRequest {
	var out []gotato.ModelRequest
	for _, request := range model.Requests() {
		if isAsk(request) {
			out = append(out, request)
		}
	}
	return out
}

func runThree(t *testing.T, runner *service.Runner) string {
	t.Helper()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	return id
}

func TestTheModelsRecapIsOffUnlessTheAgentTurnsItOn(t *testing.T) {
	model := recapModel()
	runner, _ := newRunner(t, service.Agent{Name: "ops", Model: model, Instruction: "be brief"})
	id := runThree(t, runner)
	result, err := runner.Compact(context.Background(), id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "none" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	if len(asks(model)) != 0 {
		t.Fatalf("the model was asked for a Recap by default")
	}
}

func TestAnAgentTurnsTheModelsRecapOnAndItAsksOnTheNextCallsContext(t *testing.T) {
	model := recapModel()
	agent := service.Agent{Name: "ops", Model: model, Instruction: "be brief", Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}}}
	runner, store := newRunner(t, agent)
	ctx := context.Background()
	id := runThree(t, runner)
	before, err := runner.Inspect(ctx, id, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Compact(ctx, id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "model" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	sent := asks(model)
	if len(sent) != 1 {
		t.Fatalf("asks = %d", len(sent))
	}
	n := len(before.Request.Messages)
	prefix := sent[0]
	prefix.Messages = prefix.Messages[:n]
	prefix.Config.MaxTokens = before.Request.Config.MaxTokens
	want, _ := json.Marshal(before.Request)
	got, _ := json.Marshal(prefix)
	if string(want) != string(got) || len(sent[0].Messages) != n+1 {
		t.Fatalf("the Recap's request does not start with the next call's request:\n got %s\nwant %s", got, want)
	}
	if sent[0].CacheKey == "" || sent[0].CacheKey != before.Request.CacheKey {
		t.Errorf("cache key = %q, want %q", sent[0].CacheKey, before.Request.CacheKey)
	}
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.Segments()[0].Recap; got != strings.TrimSpace(agentRecap) {
		t.Errorf("Segment Recap = %q", got)
	}
	for _, m := range record.Messages() {
		if strings.Contains(gotato.TextOf(m), itext.RecapAskPrefix) {
			t.Errorf("the ask was stored: %q", gotato.TextOf(m))
		}
	}
}

func TestAFailedModelRecapKeepsTheSegmentWithoutOne(t *testing.T) {
	model := testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
		return testkit.Text("Sure, here you go: nothing."), nil
	})
	runner, _ := newRunner(t, service.Agent{Name: "ops", Model: model, Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}}})
	id := runThree(t, runner)
	result, err := runner.Compact(context.Background(), id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "none" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
}

func TestACompactionPastTheCeilingWritesTheRecapWithTheModelWhenTheAgentAsks(t *testing.T) {
	model := recapModel()
	agent := service.Agent{Name: "ops", Model: model, Compact: modelctx.CompactPolicy{CeilingTokens: 20000, Recap: modelctx.ModelRecap{}}}
	runner, store := newRunner(t, agent)
	ctx := context.Background()
	var last service.RunResponse
	for i := 0; i < 8 && !last.Compacted; i++ {
		var err error
		last, err = runner.Run(ctx, service.RunRequest{SessionID: last.SessionID, Prompt: strings.Repeat("word ", 4000)})
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := session.View(ctx, store, last.SessionID)
	if err != nil || len(record.Compactions()) == 0 {
		t.Fatalf("no compaction: %v", err)
	}
	if got := record.Compactions()[0].Recap; got != "model" || len(asks(model)) == 0 {
		t.Fatalf("recap = %q, asks = %d", got, len(asks(model)))
	}
}
