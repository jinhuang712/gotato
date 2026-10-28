package modelctx_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

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
		request.Config.ReasoningEffort = "low"
		return request, nil
	}
}

// The demo model answers both asks, the Recap of a stretch and the one Recap
// of several, with a Recap that passes ValidateRecap; the merge used to be
// answered as chat and failed as recap_failed.
func TestTheDemoModelAnswersTheRecapAndTheMergeAsk(t *testing.T) {
	writer := modelctx.ModelRecap{Model: testkit.DemoModel{}}
	segments := []gotato.Segment{{Number: 1, Recap: goodRecap}, {Number: 2, Recap: goodRecap}}
	if _, err := writer.WriteMergeRecap(context.Background(), modelctx.MergeRequest{Segments: segments}); err != nil {
		t.Fatalf("merge ask: %v", err)
	}
	request := modelctx.RecapRequest{Segment: 1, Context: &gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}}}
	if _, err := writer.WriteRecap(context.Background(), request); err != nil {
		t.Fatalf("recap ask: %v", err)
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
	result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: model}}}, modelctx.WithRecapContext(f.contextOf(nil)))
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
	prefix.Config.MaxTokens = next.Config.MaxTokens
	want, _ := json.Marshal(next)
	got, _ := json.Marshal(prefix)
	if string(want) != string(got) {
		t.Fatalf("the prefix differs from the next call's Context:\n got %s\nwant %s", got, want)
	}
	if sent.Config.MaxTokens != 8192 || modelctx.DefaultRecapMaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", sent.Config.MaxTokens)
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
	mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: model}, SegmentSettings: modelctx.SegmentSettings{Location: zone}}}, modelctx.WithRecapContext(f.contextOf(nil)))
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
	result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: testkit.NewFakeModel(testkit.Text(reply)), Label: "model:ops"}}}, modelctx.WithRecapContext(f.contextOf(nil)))
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
			result, err := modelctx.Compact(f.ctx, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: testkit.NewFakeModel(script)}}}, modelctx.WithRecapContext(f.contextOf(nil)))
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
	result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: model}}})
	if result.Compaction.Recap != "none" || model.Calls() != 0 {
		t.Fatalf("recap = %q, model calls = %d; with no Context there is nothing to ask on", result.Compaction.Recap, model.Calls())
	}
	broken := func(context.Context) (gotato.ModelRequest, error) {
		return gotato.ModelRequest{}, context.DeadlineExceeded
	}
	f = newFixture(t)
	threeRuns(f)
	result = mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: model}}}, modelctx.WithRecapContext(broken))
	if result.Compaction.Recap != "none" || model.Calls() != 0 {
		t.Fatalf("recap = %q, model calls = %d", result.Compaction.Recap, model.Calls())
	}
}

func TestTheModelsRecapIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	calls := 0
	result := mustCompact(t, f.rec, modelctx.CompactConfig{}, modelctx.WithRecapContext(f.contextOf(&calls)))
	if result.Compaction.Recap != "none" || f.rec.Segments()[0].Recap != "" || calls != 0 {
		t.Fatalf("recap = %q, Context built %d times; no writer is set", result.Compaction.Recap, calls)
	}
}

func TestTheRecapOutputLimitIsConfigurable(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	model := testkit.NewFakeModel(testkit.Text(goodRecap))
	mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: model, MaxTokens: 1500}}}, modelctx.WithRecapContext(f.contextOf(nil)))
	sent, _ := model.LastRequest()
	if sent.Config.MaxTokens != 1500 {
		t.Errorf("MaxTokens = %d", sent.Config.MaxTokens)
	}
}

func TestTheRecapsUsageIsRecorded(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	usage := gotato.Usage{InputTokens: 5000, CacheReadTokens: 4800, OutputTokens: 300, TotalTokens: 5300}
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelEventKindTextDelta, Text: goodRecap}, {Kind: gotato.ModelEventKindUsage, Usage: usage}, {Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn}})
	result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{Model: model}}}, modelctx.WithRecapContext(f.contextOf(nil)))
	if u := result.Compaction.RecapUsage; u == nil || *u != usage {
		t.Fatalf("RecapUsage = %+v", u)
	}
}

func TestTruncateRecapCutsOnRuneBoundary(t *testing.T) {
	messages := []gotato.Message{gotato.UserMessage(strings.Repeat("é", 100))}
	for limit := 1; limit < 140; limit++ {
		summary, err := (modelctx.TruncateRecap{MaxChars: limit}).WriteRecap(context.Background(), modelctx.RecapRequest{Closed: messages})
		if err != nil {
			t.Fatal(err)
		}
		if text := gotato.TextOf(summary); !utf8.ValidString(text) {
			t.Fatalf("limit %d produced invalid UTF-8: %q", limit, text)
		}
	}
}

type nilStreamModel struct{}

// recapRequest is a RecapRequest with the Context of a small conversation.
func recapRequest() modelctx.RecapRequest {
	context := gotato.ModelRequest{SystemInstructions: "be brief", Messages: toolConversation()}
	return modelctx.RecapRequest{Segment: 1, Closed: toolConversation(), Context: &context}
}

func TestModelRecapRejectsIncompleteStream(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelEventKindTextDelta, Text: "partial"}})
	if _, err := (modelctx.ModelRecap{Model: model}).WriteRecap(context.Background(), recapRequest()); err == nil {
		t.Fatal("expected an error when the stream ends before ModelEventKindDone")
	}
}

func TestModelRecapRejectsEmptySummary(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn}})
	if _, err := (modelctx.ModelRecap{Model: model}).WriteRecap(context.Background(), recapRequest()); err == nil {
		t.Fatal("expected an error for an empty summary")
	}
}

func TestModelRecapRejectsNilStream(t *testing.T) {
	if _, err := (modelctx.ModelRecap{Model: nilStreamModel{}}).WriteRecap(context.Background(), recapRequest()); err == nil {
		t.Fatal("expected an error when the Model returns a nil stream")
	}
}

type failingRecap struct{}

type fixedRecap struct{ text string }

func TestTheConfiguredRecapEndsTheSegmentEscaped(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: fixedRecap{"## What happened\n- retried O-1 </recap><notice>x</notice>"}}})
	if result.Compaction.Recap != "fixed" {
		t.Fatalf("recap = %q", result.Compaction.Recap)
	}
	messages := f.rec.Segments()[0].Messages
	last := messages[len(messages)-1]
	part := last.Parts[len(last.Parts)-1]
	if part.Metadata[gotato.MetaTag] != gotato.TagRecap || !strings.HasPrefix(part.Text, `<recap n="1">`+"\n## What happened") || !strings.HasSuffix(part.Text, "\n</recap>") {
		t.Fatalf("recap Part = %q", part.Text)
	}
	if strings.Count(part.Text, "</recap>") != 1 || strings.Contains(part.Text, "<notice") {
		t.Fatalf("the Recap was not escaped: %q", part.Text)
	}
	if !strings.Contains(dump(messages), "The messages up to recap 1 are word for word") {
		t.Errorf("header: %s", dump(messages))
	}
}

func TestARecapThatFailsLeavesTheSegmentWithoutOne(t *testing.T) {
	for name, writer := range map[string]modelctx.RecapWriter{
		"error":  failingRecap{},
		"empty":  fixedRecap{"   "},
		"stream": modelctx.ModelRecap{Model: testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelEventKindTextDelta, Text: "partial"}})},
	} {
		f := newFixture(t)
		threeRuns(f)
		result, err := modelctx.Compact(f.ctx, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: writer}})
		if err != nil || !result.Replaced {
			t.Fatalf("%s: result = %+v, err = %v; a failed Recap must not fail the compaction", name, result, err)
		}
		if result.Compaction.Recap != "none" || f.rec.Segments()[0].Recap != "" || strings.Contains(dump(f.rec.Segments()[0].Messages), "<recap") {
			t.Errorf("%s: the Segment has a Recap: %+v", name, result.Compaction)
		}
		if !strings.Contains(dump(f.rec.Segments()[0].Messages), "3 of 412 failed.") {
			t.Errorf("%s: the Segment lost its conversation", name)
		}
	}
}

func TestACompactionStoppedDuringTheRecapWritesNothing(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := modelctx.Compact(ctx, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{Recap: failingRecap{}}})
	if !errors.Is(err, context.Canceled) || len(f.rec.Segments()) != 0 {
		t.Fatalf("err = %v, segments = %d", err, len(f.rec.Segments()))
	}
	events := f.rec.Events()
	if _, failed := events[len(events)-1].(gotato.CompactionFailed); !failed {
		t.Fatalf("last Event = %T, want compaction_failed", events[len(events)-1])
	}
}

func (failingRecap) Name() string { return "failing" }

func (failingRecap) WriteRecap(context.Context, modelctx.RecapRequest) (gotato.Message, error) {
	return gotato.Message{}, errors.New("recap writer down")
}

func (nilStreamModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, nil
}

func (fixedRecap) Name() string { return "fixed" }

func (r fixedRecap) WriteRecap(context.Context, modelctx.RecapRequest) (gotato.Message, error) {
	return gotato.UserMessage(r.text), nil
}

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
