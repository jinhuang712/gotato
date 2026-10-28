package modelctx_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
)

func TestCompactBuildsASegmentFromTheConversationAndOneLinePerCall(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	result := mustCompact(t, f.rec, modelctx.CompactConfig{})
	if !result.Replaced || result.Compaction == nil || result.Compaction.Segment != 1 || result.Compaction.Recap != "none" {
		t.Fatalf("result = %+v", result)
	}
	segments := f.rec.Segments()
	if len(segments) != 1 {
		t.Fatalf("segments = %d", len(segments))
	}
	want := `[user]
<segment n="1" from="2026-10-19T10:01:00Z" to="2026-10-19T10:41:00Z">
Segment 1. The messages are word for word; reasoning,
intermediate steps and tool results were left out. read_segment(1) returns
the segment in full.
</segment>
Some charges in last night's batch failed. Why?
[assistant]
<tool_calls>
10:02 payments.get_batch {"batch_id":"B-1"} → ok
10:02 logs.query {"order":"O-1"} → error failed
</tool_calls>
3 of 412 failed.
[user]
Retry O-1
[assistant]
<tool_calls>
10:40 payments.retry {"order":"O-1"} → ok
</tool_calls>
O-1 went through.
`
	if got := dump(segments[0].Messages); got != want {
		t.Fatalf("Segment =\n%s\nwant\n%s", got, want)
	}
	for _, left := range []string{"TOOLRESULT", "THINKING", "let me look"} {
		if strings.Contains(dump(segments[0].Messages), left) {
			t.Errorf("the Segment kept %q, which it leaves out", left)
		}
	}
	// The tag text is Parts marked MetaTag; the user's words are not.
	tagged := 0
	for _, m := range segments[0].Messages {
		for _, part := range m.Parts {
			if part.Metadata[gotato.MetaTag] != "" {
				tagged++
			}
		}
	}
	if tagged != 3 {
		t.Errorf("tagged Parts = %d, want the header and two tool_calls blocks", tagged)
	}
	// The kept tail is the latest Run, word for word, after the Segment.
	if tail := f.rec.Tail(); len(tail) != 2 || gotato.TextOf(tail[0]) != "thanks" || gotato.TextOf(tail[1]) != "welcome" {
		t.Fatalf("tail = %+v", tail)
	}
	if segments[0].Source.FromNumber == 0 || segments[0].Source.ToNumber <= segments[0].Source.FromNumber {
		t.Errorf("source = %+v, want the Entry range the Segment covers", segments[0].Source)
	}
	if len(segments[0].ToolCalls) != 3 {
		t.Errorf("tool call lines = %+v", segments[0].ToolCalls)
	}
	if result.TokensAfter >= result.TokensBefore && result.MessagesAfter >= result.MessagesBefore {
		t.Errorf("the compaction did not shrink the Context: %+v", result)
	}
}

func TestCompactEmitsTheSegmentAndItsCheckpoint(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	var kinds []gotato.EventKind
	var ended gotato.CompactionEnded
	for _, event := range f.rec.Events() {
		switch e := event.(type) {
		case gotato.CompactionStarted:
			kinds = append(kinds, e.Kind())
			if e.Why != gotato.CompactionWhyOnDemand {
				t.Errorf("why = %q", e.Why)
			}
		case gotato.SegmentClosed:
			kinds = append(kinds, e.Kind())
		case gotato.CompactionEnded:
			kinds = append(kinds, e.Kind())
			ended = e
		}
	}
	if !slices.Equal(kinds, []gotato.EventKind{gotato.EventKindCompactionStarted, gotato.EventKindSegmentClosed, gotato.EventKindCompactionEnded}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if !slices.Equal(ended.Segments, []int{1}) || len(ended.Messages) != 2 || ended.Compaction == nil || ended.Compaction.ReplacedMessages != 9 {
		t.Fatalf("compaction_ended = %+v", ended)
	}
}

// A Segment keeps its bytes through the next compaction, and every instance
// that loads the Record from the store sends the same request.
func TestSegmentBytesAreStableAndEveryInstanceBuildsTheSameRequest(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	first, _ := json.Marshal(f.rec.Segments()[0].Messages)

	f.user("r4", "and the other two?", 60)
	f.calls("r4", "", 61, nil, `c4 orders.search {"ids":["O-2"]}`)
	f.reply("r4", "O-2 is waiting.", 62)
	second := mustCompact(t, f.rec, modelctx.CompactConfig{})
	if !second.Replaced || second.Compaction.Segment != 2 {
		t.Fatalf("second compaction = %+v", second)
	}
	segments := f.rec.Segments()
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(segments))
	}
	if again, _ := json.Marshal(segments[0].Messages); string(again) != string(first) {
		t.Fatalf("Segment 1 changed:\n%s\n%s", first, again)
	}
	// The second Segment closed the first one's kept tail, "thanks"/"welcome".
	if text := dump(segments[1].Messages); !strings.Contains(text, "thanks") || !strings.Contains(text, "welcome") {
		t.Fatalf("Segment 2 = %s", text)
	}
	if err := f.writer.Release(f.ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Open(f.ctx, f.store, f.rec.ID())
	if err != nil {
		t.Fatal(err)
	}
	live, _ := json.Marshal(gotato.ForModel(f.rec.Messages()))
	reloaded, _ := json.Marshal(gotato.ForModel(loaded.Messages()))
	if string(live) != string(reloaded) {
		t.Fatalf("a reloaded Record builds another request:\n%s\n%s", live, reloaded)
	}
	if got, _ := json.Marshal(loaded.Segments()[0].Messages); string(got) != string(first) {
		t.Fatalf("Segment 1 after a reload:\n%s", got)
	}
	if !slices.Equal(loaded.Compactions()[0].LoadedTools, f.rec.Compactions()[0].LoadedTools) || len(loaded.Compactions()) != 2 {
		t.Fatalf("compactions after a reload = %+v", loaded.Compactions())
	}
}

func TestKeptTailStartsAtATurnSoACallKeepsItsResult(t *testing.T) {
	for keep := 1; keep <= 8; keep++ {
		f := newFixture(t)
		f.user("r1", "go", 1)
		f.calls("r1", "", 2, nil, `c1 a.x {}`, `c2 a.y {}`)
		f.calls("r1", "", 3, nil, `c3 a.z {}`)
		f.reply("r1", "done", 4)
		result, err := modelctx.Compact(f.ctx, f.rec, modelctx.CompactConfig{KeepMessages: keep})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Replaced {
			continue
		}
		tail := f.rec.Tail()
		if len(tail) > 0 && tail[0].Role == gotato.RoleToolResult {
			t.Fatalf("keep %d: the tail starts on a tool result", keep)
		}
		open := map[gotato.ToolCallID]bool{}
		for _, m := range tail {
			for _, call := range m.ToolCalls {
				open[call.ID] = true
			}
			if m.ToolResult != nil {
				delete(open, m.ToolResult.CallID)
			}
		}
		if len(open) != 0 {
			t.Fatalf("keep %d: calls %v lost their results", keep, open)
		}
	}
}

func TestDefaultTailIsTheLatestRunsTurnsWithinABudget(t *testing.T) {
	build := func() *fixture {
		f := newFixture(t)
		f.user("r1", "first", 1)
		f.reply("r1", "first answer", 2)
		f.user("r2", "second", 3)
		for i := range 4 {
			f.calls("r2", "", 4+i, nil, fmt.Sprintf(`c%d a.x {"pad":"%s"}`, i, strings.Repeat("p", 380)))
		}
		f.reply("r2", "second answer", 9)
		return f
	}
	// Without a window or a ceiling the whole latest Run fits 20K tokens, and
	// the Run before it is all that closes.
	f := build()
	result := mustCompact(t, f.rec, modelctx.CompactConfig{})
	if !result.Replaced || gotato.TextOf(f.rec.Tail()[0]) != "second" || len(f.rec.Tail()) != 10 {
		t.Fatalf("tail = %d Messages starting %q", len(f.rec.Tail()), gotato.TextOf(f.rec.Tail()[0]))
	}
	// A window of 800 tokens caps the tail at 200: only the latest Turns stay,
	// from a Turn boundary.
	f = build()
	mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{WindowTokens: 800}})
	tail := f.rec.Tail()
	if len(tail) == 0 || len(tail) >= 10 || tail[0].Role == gotato.RoleToolResult || gotato.TextOf(tail[len(tail)-1]) != "second answer" {
		t.Fatalf("capped tail = %d Messages: %+v", len(tail), tail)
	}
	if modelctx.EstimateTokens(tail) > 200+modelctx.EstimateTokens(tail[:1]) {
		t.Errorf("the capped tail holds %d tokens, past a quarter of the window", modelctx.EstimateTokens(tail))
	}
	// With no window, the ceiling caps it the same way.
	f = build()
	mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{CeilingTokens: 800}})
	if got := len(f.rec.Tail()); got != len(tail) {
		t.Errorf("tail under a ceiling = %d Messages, want %d as under the window", got, len(tail))
	}
	// KeepMessages and TailTokens override the default.
	f = build()
	mustCompact(t, f.rec, modelctx.CompactConfig{KeepMessages: 1})
	if tail := f.rec.Tail(); len(tail) != 1 || gotato.TextOf(tail[0]) != "second answer" {
		t.Fatalf("KeepMessages: 1 tail = %+v", tail)
	}
	f = build()
	mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{SegmentSettings: modelctx.SegmentSettings{TailTokens: 150}}})
	if tail := f.rec.Tail(); len(tail) == 0 || len(tail) >= 10 || tail[0].Role == gotato.RoleToolResult {
		t.Fatalf("TailTokens tail = %d Messages", len(tail))
	}
}

func TestACompactionWhoseTailCoversTheStretchClosesNothing(t *testing.T) {
	f := newFixture(t)
	f.user("r1", "only", 1)
	f.reply("r1", "run", 2)
	result := mustCompact(t, f.rec, modelctx.CompactConfig{})
	if result.Replaced || result.Reason != modelctx.CompactReasonNothingToClose || result.Compaction != nil || len(f.rec.Segments()) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if encoded, _ := json.Marshal(result); strings.Contains(string(encoded), `"compaction"`) {
		t.Fatalf("an empty compaction record leaked into JSON: %s", encoded)
	}
}

func TestToolsTheSegmentAndTheTailCalledStayLoaded(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"files.read", "mail.send", "db.query", "idle.x"} {
		f.load(id)
	}
	f.user("r1", "go", 1)
	f.calls("r1", "", 2, nil, `c1 files.read {}`)
	f.calls("r1", "", 3, nil, `c2 mail.send {}`)
	f.reply("r1", "done", 4)
	f.user("r2", "next", 5)
	f.calls("r2", "", 6, nil, `c3 db.query {}`)
	f.reply("r2", "ok", 7)
	result := mustCompact(t, f.rec, modelctx.CompactConfig{})
	want := []string{"files.read", "mail.send", "db.query"}
	if !slices.Equal(result.Compaction.LoadedTools, want) || !slices.Equal(f.rec.LoadedTools(), want) {
		t.Fatalf("kept Tools = %v / %v, want %v (idle.x is unloaded)", result.Compaction.LoadedTools, f.rec.LoadedTools(), want)
	}
	if got := f.rec.Segments()[0].LoadedTools; !slices.Equal(got, []string{"files.read", "mail.send"}) {
		t.Errorf("Segment Tools = %v", got)
	}
	// The unload is a fact of the Record, with its reason.
	var unloaded []gotato.ToolUnloaded
	for _, event := range f.rec.Events() {
		if e, ok := event.(gotato.ToolUnloaded); ok {
			unloaded = append(unloaded, e)
		}
	}
	if len(unloaded) != 1 || unloaded[0].ToolID != "idle.x" || unloaded[0].Why != gotato.ToolUnloadedWhyCompaction {
		t.Errorf("tool_unloaded = %+v, want idle.x for compaction", unloaded)
	}
	// Every instance that loads the Record offers the same Tools.
	if got := f.reload().LoadedTools(); !slices.Equal(got, want) {
		t.Errorf("Tools after a load = %v", got)
	}
}

func TestMinTokensSkipsAStretchTooShortToCompact(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	stretch := modelctx.EstimateTokens(f.rec.Tail())
	skipped := mustCompact(t, f.rec, modelctx.CompactConfig{MinTokens: stretch + 1})
	if skipped.Replaced || skipped.Reason != modelctx.CompactReasonBelowMinTokens || len(f.rec.Segments()) != 0 || f.rec.Len() != 11 {
		t.Fatalf("below min_tokens: %+v", skipped)
	}
	for _, event := range f.rec.Events() {
		if _, started := event.(gotato.CompactionStarted); started {
			t.Fatal("a skipped compaction started")
		}
	}
	if done := mustCompact(t, f.rec, modelctx.CompactConfig{MinTokens: stretch}); !done.Replaced {
		t.Fatalf("at min_tokens: %+v", done)
	}
}
