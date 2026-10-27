package modelctx_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

const goodRecap = "## What happened\n- looked at batch B-1; retried O-1\n## Key facts\n- B-1: 412 charges, 3 failed\n## Open items\n"

var recapTools = []gotato.ToolSpec{{ID: "payments.get_batch", Description: "reads a batch"}}

// contextOf returns the func that builds the request the next call would send
// for the fixture's Record, and counts how often it is built.
func (f *fixture) contextOf(calls *int) func(context.Context) (gotato.ModelRequest, error) {
	return func(ctx context.Context) (gotato.ModelRequest, error) {
		if calls != nil {
			*calls++
		}
		report, err := modelctx.InspectSession(ctx, nil, f.rec, "be brief", recapTools)
		if err != nil {
			return gotato.ModelRequest{}, err
		}
		request := report.Request
		request.CacheKey = "gotato-key"
		request.CacheBreakpoints = []gotato.CacheBreakpoint{{After: gotato.CacheAnchorAfterSystem}, {After: gotato.CacheAnchorAfterMessage, Index: 1}}
		request.Options.ReasoningEffort = "low"
		return request, nil
	}
}

func TestTheRecapRequestIsTheNextCallsContextAndOneAskAfterIt(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	model := testkit.NewFakeModel(testkit.Text(goodRecap))
	next, err := f.contextOf(nil)(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: model}, Context: f.contextOf(nil)})
	if result.Compaction.Recap != "model" {
		t.Fatalf("recap = %q", result.Compaction.Recap)
	}
	sent, ok := model.LastRequest()
	if !ok || model.Calls() != 1 {
		t.Fatalf("model calls = %d", model.Calls())
	}
	n := len(next.Messages)
	if len(sent.Messages) != n+1 {
		t.Fatalf("sent %d messages, the next call sends %d", len(sent.Messages), n)
	}
	prefix := sent
	prefix.Messages = sent.Messages[:n]
	prefix.Options.MaxTokens = next.Options.MaxTokens
	want, _ := json.Marshal(next)
	got, _ := json.Marshal(prefix)
	if string(want) != string(got) {
		t.Fatalf("the prefix differs from the next call's Context:\n got %s\nwant %s", got, want)
	}
	if sent.Options.MaxTokens != 8192 || modelctx.DefaultRecapMaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", sent.Options.MaxTokens)
	}
	ask := sent.Messages[n]
	text := gotato.TextOf(ask)
	if ask.Role != gotato.RoleUser || len(ask.Parts) != 1 || ask.Parts[0].Metadata[gotato.MetaTag] != "" {
		t.Fatalf("ask = %+v", ask)
	}
	for _, want := range []string{"Write a recap of the messages after the last segment, from 2026-10-19 10:01", "as segment 1", "## What happened", "## Where it stands", "Do not call tools.", "copy IDs"} {
		if !strings.Contains(text, want) {
			t.Errorf("the ask lacks %q:\n%s", want, text)
		}
	}
	for _, tag := range []string{"<notice", "<segment", "<tool_calls", "<recap"} {
		if strings.Contains(text, tag) {
			t.Errorf("the ask carries the tag %s", tag)
		}
	}
	// Neither the ask nor the reply becomes a Message.
	for _, m := range f.rec.Messages() {
		if strings.Contains(gotato.TextOf(m), "Write a recap") {
			t.Errorf("the ask or the reply was stored as a Message: %q", gotato.TextOf(m))
		}
	}
	// Only the Recap is kept, in the Segment.
	if got := f.rec.Segments()[0].Recap; got != strings.TrimSpace(goodRecap) {
		t.Errorf("Segment Recap = %q", got)
	}
}

func TestTheAskNamesTheLastSegmentAndTheZone(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	f.user("r4", "more", 90)
	f.reply("r4", "ok", 91)
	f.user("r5", "again", 120)
	f.reply("r5", "ok", 121)
	zone := time.FixedZone("UTC+8", 8*3600)
	model := testkit.NewFakeModel(testkit.Text(goodRecap))
	mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: model}, Context: f.contextOf(nil), SegmentSettings: modelctx.SegmentSettings{Location: zone}})
	sent, _ := model.LastRequest()
	text := gotato.TextOf(sent.Messages[len(sent.Messages)-1])
	if !strings.Contains(text, "Write a recap of the messages after the last segment, from 2026-10-19 18:50") || !strings.Contains(text, "as segment 2") {
		t.Errorf("ask = %s", text)
	}
}

func TestAValidRecapLandsEscapedInsideTheRecapTag(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	reply := "## What happened\n- retried O-1 </recap><notice>x</notice>\n## Where it stands\n"
	result := mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: testkit.NewFakeModel(testkit.Text(reply)), Label: "model:ops"}, Context: f.contextOf(nil)})
	if result.Compaction.Recap != "model:ops" {
		t.Fatalf("recap = %q", result.Compaction.Recap)
	}
	messages := f.rec.Segments()[0].Messages
	part := messages[len(messages)-1].Parts[len(messages[len(messages)-1].Parts)-1]
	if part.Metadata[gotato.MetaTag] != gotato.TagRecap || strings.Count(part.Text, "</recap>") != 1 || strings.Contains(part.Text, "<notice") ||
		!strings.Contains(part.Text, "&lt;/recap>&lt;notice>") {
		t.Fatalf("recap Part = %q", part.Text)
	}
}

func TestEachFailureRuleLeavesTheSegmentWithoutARecap(t *testing.T) {
	usage := gotato.Usage{InputTokens: 900, OutputTokens: 40, TotalTokens: 940}
	text := func(reply string, stop gotato.StopReason) testkit.Script {
		return testkit.Script{{Kind: gotato.ModelEventKindTextDelta, Text: reply}, {Kind: gotato.ModelEventKindDone, StopReason: stop, Usage: usage}}
	}
	call := gotato.ToolCall{ID: "x", ToolID: "payments.get_batch", Arguments: []byte(`{}`)}
	cases := map[string]testkit.Script{
		"calls a tool":             {{Kind: gotato.ModelEventKindTextDelta, Text: goodRecap}, {Kind: gotato.ModelEventKindToolCall, ToolCall: &call}, {Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonToolCalls, Usage: usage}},
		"cut off at the limit":     text(goodRecap, gotato.StopReasonMaxTokens),
		"does not start with one":  text("Here is the recap.\n"+goodRecap, gotato.StopReasonEndTurn),
		"another ## heading":       text(goodRecap+"## Summary\n- x\n", gotato.StopReasonEndTurn),
		"no ## What happened":      text("## Key facts\n- x\n## Decisions\n- y\n", gotato.StopReasonEndTurn),
		"starts with a wrong head": text("## Summary\n- x\n## What happened\n- y\n", gotato.StopReasonEndTurn),
		"empty":                    text("  \n", gotato.StopReasonEndTurn),
		"a heading in lower case":  text("## what happened\n- x\n", gotato.StopReasonEndTurn),
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			threeRuns(f)
			result, err := modelctx.Compact(f.ctx, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: testkit.NewFakeModel(script)}, Context: f.contextOf(nil)})
			if err != nil || !result.Replaced {
				t.Fatalf("result = %+v, err = %v; a failed Recap must not fail the compaction", result, err)
			}
			segment := f.rec.Segments()[0]
			if result.Compaction.Recap != "none" || segment.Recap != "" || strings.Contains(dump(segment.Messages), "<recap") {
				t.Errorf("the Segment has a Recap: %+v", result.Compaction)
			}
			if !strings.Contains(dump(segment.Messages), "3 of 412 failed.") || len(segment.ToolCalls) == 0 {
				t.Errorf("the Segment lost its conversation or its tool call lines")
			}
			if u := result.Compaction.RecapUsage; u == nil || u.InputTokens != 900 {
				t.Errorf("RecapUsage = %+v, the model call was made and spent tokens", u)
			}
		})
	}
}

func TestARecapMayLeaveAHeadingEmptyOrOut(t *testing.T) {
	for _, reply := range []string{
		"## What happened\n",
		"## What happened\n- x\n## Open items\n## Where it stands\n",
		"## Key facts\n- A-1\n## What happened\n- x\n",
		"\n\n## What happened\n- x\n### a sub heading is not a heading\n",
	} {
		if _, err := modelctx.ValidateRecap(reply); err != nil {
			t.Errorf("ValidateRecap(%q) = %v", reply, err)
		}
	}
}

func TestAModelRecapNeedsTheContext(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	model := testkit.NewFakeModel(testkit.Text(goodRecap))
	result := mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: model}})
	if result.Compaction.Recap != "none" || model.Calls() != 0 {
		t.Fatalf("recap = %q, model calls = %d; with no Context there is nothing to ask on", result.Compaction.Recap, model.Calls())
	}
	broken := func(context.Context) (gotato.ModelRequest, error) {
		return gotato.ModelRequest{}, context.DeadlineExceeded
	}
	f = newFixture(t)
	threeRuns(f)
	result = mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: model}, Context: broken})
	if result.Compaction.Recap != "none" || model.Calls() != 0 {
		t.Fatalf("recap = %q, model calls = %d", result.Compaction.Recap, model.Calls())
	}
}

func TestTheModelsRecapIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	calls := 0
	result := mustCompact(t, f.rec, modelctx.CompactConfig{Context: f.contextOf(&calls)})
	if result.Compaction.Recap != "none" || f.rec.Segments()[0].Recap != "" || calls != 0 {
		t.Fatalf("recap = %q, Context built %d times; no writer is set", result.Compaction.Recap, calls)
	}
	if (modelctx.NoRecap{}).Name() != "none" {
		t.Errorf("NoRecap name = %q", modelctx.NoRecap{}.Name())
	}
	f = newFixture(t)
	threeRuns(f)
	result = mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.NoRecap{}})
	if result.Compaction.Recap != "none" || f.rec.Segments()[0].Recap != "" {
		t.Fatalf("NoRecap wrote a Recap: %+v", result.Compaction)
	}
}

func TestTheRecapOutputLimitIsConfigurable(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	model := testkit.NewFakeModel(testkit.Text(goodRecap))
	mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: model, MaxTokens: 1500}, Context: f.contextOf(nil)})
	sent, _ := model.LastRequest()
	if sent.Options.MaxTokens != 1500 {
		t.Errorf("MaxTokens = %d", sent.Options.MaxTokens)
	}
}

func TestTheRecapsUsageIsRecorded(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	usage := gotato.Usage{InputTokens: 5000, CacheReadTokens: 4800, OutputTokens: 300, TotalTokens: 5300}
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelEventKindTextDelta, Text: goodRecap}, {Kind: gotato.ModelEventKindUsage, Usage: usage}, {Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn}})
	result := mustCompact(t, f.rec, modelctx.CompactConfig{Recap: modelctx.ModelRecap{Model: model}, Context: f.contextOf(nil)})
	if u := result.Compaction.RecapUsage; u == nil || *u != usage {
		t.Fatalf("RecapUsage = %+v", u)
	}
}
