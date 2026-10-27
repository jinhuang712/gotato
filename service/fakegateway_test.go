package service_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// These tests run a Runner with a gateway Agent against internal/fakegateway: the
// Phase 10 exit item "an overflow in each provider's form compacts and sends
// again", and the same for a Session past its ceiling. The fake refuses a
// request past its window in each provider's own words and status.

var fakeGatewayWires = []struct {
	name   string
	config gateway.Config
}{
	{fakegateway.APIAnthropic, gateway.Config{API: gateway.APIAnthropicMessages, Provider: gateway.ProviderAnthropic, Model: "claude-sonnet-5-5"}},
	{fakegateway.APIResponses, gateway.Config{API: gateway.APIResponses, Provider: gateway.ProviderOpenAI, Model: "gpt-6.1-sol"}},
	{fakegateway.APIChat, gateway.Config{API: gateway.APIChatCompletions, Provider: gateway.ProviderOther, Model: "chat-model"}},
}

func gatewayRunner(t *testing.T, config gateway.Config, fake *fakegateway.Gateway, policy modelctx.CompactPolicy) *service.Runner {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	config.BaseURL = server.URL
	config.APIKey = "k"
	config.RetryBackoff = time.Millisecond
	client, err := gateway.New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "fake", Model: client, Compact: policy}}})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// compactions returns why each compaction of a Session ran.
func compactions(t *testing.T, runner *service.Runner, id string) []gotato.CompactionWhy {
	t.Helper()
	events, err := runner.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []gotato.CompactionWhy
	for _, event := range events {
		if ended, ok := event.(gotato.CompactionEnded); ok {
			out = append(out, ended.Why)
		}
	}
	return out
}

// describe lists what the fake answered, without the bodies.
func describe(requests []fakegateway.Request) string {
	var out []string
	for _, r := range requests {
		out = append(out, fmt.Sprintf("%s/%d/%dB", r.Outcome, r.Status, len(r.Body)))
	}
	return strings.Join(out, " ")
}

// prompt is a user Message of about 20 KB, past the 2K tokens a Segment keeps
// of one, so a compaction makes the Context smaller.
func prompt(word string) string { return word + " " + strings.Repeat(word+" ", 3000) }

func TestOverflowOnEachWireAPICompactsAndSendsAgainOnce(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			// A window that holds three exchanges of this size and not four, and no
			// ceiling: the refusal is the only thing that says the Context is too long.
			fake := fakegateway.New(fakegateway.WithWindow(33_000))
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{Ceiling: -1})
			ctx := context.Background()
			id := ""
			for _, word := range []string{"alpha", "bravo", "charlie"} {
				result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt(word)})
				if err != nil || result.Result.Status != gotato.RunCompleted {
					t.Fatalf("%s: %+v, %v", word, result, err)
				}
				id = result.SessionID
			}
			before := len(fake.Requests())
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt("delta")})
			if err != nil || result.Result.Status != gotato.RunCompleted || !strings.HasPrefix(result.FinalText, "fake: delta") {
				t.Fatalf("delta: %+v, %v", result, err)
			}
			requests := fake.Requests()[before:]
			if len(requests) != 2 || requests[0].Outcome != "refused" || requests[0].Status != 400 || requests[1].Outcome != "ok" {
				t.Fatalf("requests of the Run: %s", describe(requests))
			}
			if len(requests[1].Body) >= len(requests[0].Body) {
				t.Errorf("the request sent again is not smaller: %d bytes after %d", len(requests[1].Body), len(requests[0].Body))
			}
			if got := compactions(t, runner, id); len(got) != 1 || got[0] != gotato.CompactionOverflow {
				t.Fatalf("compactions = %v, want one overflow", got)
			}
			if !strings.Contains(string(requests[1].Body), "delta") {
				t.Error("the prompt of the Run is not in the request sent again")
			}
		})
	}
}

func TestASecondRefusalInTheSameTurnEndsTheRunWithContextOverflowOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			fake := fakegateway.New()
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{Ceiling: -1})
			ctx := context.Background()
			// Two Runs of history: the compaction keeps the latest Run before the
			// one that starts as its tail, and closes the Run before that.
			first, err := runner.Run(ctx, service.RunRequest{Prompt: prompt("alpha")})
			if err == nil {
				first, err = runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: prompt("bravo")})
			}
			if err != nil {
				t.Fatal(err)
			}
			// The directive makes the fake refuse every time, as a window the history
			// cannot get under would.
			result, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "charlie [quirk:overflow]"})
			if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Result.Status != gotato.RunFailed {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if n := len(fake.Requests()); n != 4 {
				t.Fatalf("%d requests (%s), compactions %v, err %v", n, describe(fake.Requests()), compactions(t, runner, first.SessionID), err)
			}
		})
	}
}

func TestPastTheCeilingCompactsBeforeAnyRequestOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			fake := fakegateway.New()
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{Ceiling: 12_000})
			ctx := context.Background()
			id := ""
			for _, word := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
				result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt(word)})
				if err != nil || result.Result.Status != gotato.RunCompleted {
					t.Fatalf("%s: %+v, %v", word, result, err)
				}
				id = result.SessionID
			}
			got := compactions(t, runner, id)
			if len(got) == 0 {
				t.Fatal("the Session never compacted")
			}
			for _, why := range got {
				if why != gotato.CompactionPastCeiling {
					t.Errorf("compactions = %v, want only past_ceiling", got)
				}
			}
			for i, request := range fake.Requests() {
				if request.Outcome != "ok" {
					t.Errorf("request %d was %s: a compaction past the ceiling comes before the request", i, request.Outcome)
				}
			}
		})
	}
}

// A provider that says the window ran out in the middle of the reply (Claude
// stops with model_context_window_exceeded, OpenAI sends an error event) ends
// the Run with context_overflow today: the loop compacts and sends again only
// for a request refused before any output, because a reply that has begun
// cannot be taken back (TODO.md, "Does every refusal as too long reach the
// Loop before any output?"). The text received stays as a Cut-off reply.
func TestAnOverflowInTheMiddleOfAReplyEndsTheRunAndKeepsTheCutOffTextOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			fake := fakegateway.New()
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{Ceiling: -1})
			ctx := context.Background()
			id := ""
			for _, word := range []string{"alpha", "bravo", "charlie"} {
				result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt(word)})
				if err != nil {
					t.Fatal(err)
				}
				id = result.SessionID
			}
			before := len(fake.Requests())
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "delta [quirk:overflow-midstream]"})
			if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Result.Status != gotato.RunFailed {
				t.Fatalf("delta: %+v, %v", result, err)
			}
			if requests := fake.Requests()[before:]; len(requests) != 1 || requests[0].Outcome != "overflow-midstream" {
				t.Fatalf("requests of the Run: %s", describe(requests))
			}
			events, err := runner.Events(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			cut := 0
			for _, event := range events {
				if _, ok := event.(gotato.ReplyCutOff); ok {
					cut++
				}
			}
			if cut != 1 {
				t.Errorf("the text received before the overflow is not kept as a cut-off reply (%d reply_cut_off Events)", cut)
			}
			if got := compactions(t, runner, id); len(got) != 0 {
				t.Errorf("compactions = %v", got)
			}
		})
	}
}
