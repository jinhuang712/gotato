package modelctx_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/session"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
)

// read runs read_result on ref against the fixture's Record and store.
func (f *fixture) read(ref string, page int) (string, error) {
	f.t.Helper()
	tool := resultToolOf(f.rec, f.store, 0)
	return readResult(f.t, tool, fmt.Sprintf(`{"ref":%q,"page":%d}`, ref, page))
}

// A Segment names each tool result and each kept message by a ref, and the
// read tool returns the original text, also of a result that was never
// spilled and of a reply the Segment cut.
func TestASegmentCarriesRefsThatReadBackTheOriginals(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	text := dump(f.rec.Segments()[0].Messages)
	for _, want := range []string{"→ ok [result:c1]", "→ error failed [result:c2]", "ref msg:m01"} {
		if !strings.Contains(text, want) {
			t.Errorf("the Segment lacks %q:\n%s", want, text)
		}
	}
	// The Segment has no tool result, yet the ref reads it back from the Record.
	got, err := f.read("result:c1", 0)
	if err != nil || !strings.Contains(got, "TOOLRESULT-c1") || !strings.HasPrefix(got, "Result result:c1,") {
		t.Errorf("result:c1 = %q, %v", got, err)
	}
	got, err = f.read("msg:m01", 0)
	if err != nil || !strings.Contains(got, "Some charges in last night's batch failed. Why?") || !strings.Contains(got, "Message (user)") {
		t.Errorf("msg:m01 = %q, %v", got, err)
	}
	// Reasoning is not read back.
	if got, _ := f.read("msg:m02", 0); strings.Contains(got, "THINKING") {
		t.Errorf("msg:m02 = %q", got)
	}
	if _, err := f.read("msg:nope", 0); err == nil {
		t.Error("a ref of no message did not fail")
	}
}

// A result under one page is never spilled; its ref still reads it exactly,
// page by page, and a message cut in the Segment reads back whole.
func TestARefReadsAnUnspilledResultAndACutMessageWhole(t *testing.T) {
	f := newFixture(t)
	f.user("r1", "go", 1)
	call := gotato.Message{Role: gotato.RoleAssistant, RunID: "r1", StopReason: gotato.StopReasonToolCalls,
		ToolCalls: []gotato.ToolCall{{ID: "big1", ToolID: "demo.read", Arguments: []byte(`{}`)}}}
	f.commit(call, 2)
	body := bigBody(modelctx.DefaultPageBytes - 1000) // under one page: not spilled
	f.commit(gotato.Message{Role: gotato.RoleToolResult, RunID: "r1", ToolResult: &gotato.ToolResult{CallID: "big1", Status: gotato.ToolResultStatusOK},
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: body}}}, 3)
	long := strings.Repeat("word ", 5000) // 25000 bytes, past the 8000 a Segment keeps
	f.commit(func() gotato.Message {
		m := gotato.AssistantMessage(long)
		m.RunID, m.StopReason, m.ID = "r1", gotato.StopReasonEndTurn, "final1"
		return m
	}(), 4)
	f.user("r2", "next", 5)
	f.reply("r2", "ok", 6)
	f.user("r3", "last", 7)
	f.reply("r3", "bye", 8)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	if text := dump(f.rec.Segments()[0].Messages); !strings.Contains(text, "[result:big1]") || !strings.Contains(text, "ref msg:final1") {
		t.Fatalf("Segment = %s", text)
	}
	var got strings.Builder
	for page := 1; ; page++ {
		out, err := f.read("result:big1", page)
		if err != nil {
			t.Fatal(err)
		}
		head, rest, _ := strings.Cut(out, "\n")
		if !strings.HasPrefix(head, fmt.Sprintf("Result result:big1, %d bytes.", len(body))) {
			t.Fatalf("head = %q", head)
		}
		more := strings.LastIndex(rest, "\nMore: ")
		if more < 0 {
			got.WriteString(strings.TrimSuffix(rest, "\nEnd of result:big1."))
			break
		}
		got.WriteString(rest[:more] + "\n")
	}
	if strings.TrimRight(got.String(), "\n") != strings.TrimRight(body, "\n") {
		t.Errorf("the pages hold %d bytes, the result %d", got.Len(), len(body))
	}
	out, err := f.read("msg:final1", 1)
	if err != nil || !strings.Contains(out, "Message (assistant)") || !strings.Contains(out, strings.Repeat("word ", 100)) {
		t.Errorf("msg:final1 = %.100q, %v", out, err)
	}
}

// A ref into Entries a team pruned reads as removed, as a Segment does.
func TestARefIntoPrunedEntriesReadsAsRemoved(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	fresh := f.reload()
	empty := session.NewMemoryStore()
	tool := resultToolOf(fresh, empty, 0)
	got, err := readResult(t, tool, `{"ref":"result:c1"}`)
	if err != nil || !strings.Contains(got, "removed from the Record") {
		t.Errorf("got %q, %v", got, err)
	}
}

// A Segment written before refs has none, and still renders; a digest of it
// names the Segment by its number only.
func TestASegmentWithoutRefsStillRendersAndDigests(t *testing.T) {
	old := gotato.Segment{Number: 1, Messages: []gotato.Message{
		{Role: gotato.RoleUser, Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "hello"}}},
		{Role: gotato.RoleAssistant, Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "<tool_calls>\n10:02 a.b {} → ok\n</tool_calls>", Metadata: map[string]string{gotato.MetaTag: gotato.TagToolCalls}}}},
	}}
	digest := modelctx.MergeDigest([]gotato.Segment{old}, nil)
	if !strings.Contains(digest, "Segment 1") || strings.Contains(digest, "Refs:") {
		t.Errorf("digest = %q", digest)
	}
	if got := dump(old.Messages); !strings.Contains(got, "10:02 a.b {} → ok") {
		t.Errorf("dump = %q", got)
	}
}

// A merge digest keeps the refs of what it stands for, and so does the Recap
// writer's input; a merged Segment's refs survive the next merge's digest.
func TestAMergeDigestAndTheMergeAskKeepRefs(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	segment := f.rec.Segments()[0]
	digest := modelctx.MergeDigest([]gotato.Segment{segment}, nil)
	for _, want := range []string{"msg:m01", "msg:m06", "result:c1", "result:c3"} {
		if !strings.Contains(digest, want) {
			t.Errorf("digest lacks %s: %q", want, digest)
		}
	}
	segment.Recap = goodRecap
	ask := modelctx.MergeRecapAsk(modelctx.MergeRequest{Segments: []gotato.Segment{segment}})
	if !strings.Contains(ask, "msg:m01") || !strings.Contains(ask, "result:c2") || !strings.Contains(ask, "Keep a ref next to") {
		t.Errorf("ask = %q", ask)
	}
	// A merged Segment's Recap holds its refs; the cut of its digest line does not drop them.
	merged := gotato.Segment{Number: 3, Merged: []gotato.MergedSegment{{Number: 1}, {Number: 2}},
		Recap: "## What happened\n- " + strings.Repeat("x ", 900) + " see result:late1 and msg:late2"}
	if d := modelctx.MergeDigest([]gotato.Segment{merged}, nil); !strings.Contains(d, "result:late1") || !strings.Contains(d, "msg:late2") {
		t.Errorf("merged digest = %q", d)
	}
}

// A Segment is written once: a later compaction leaves an earlier Segment's
// bytes as they were.
func TestALaterCompactionLeavesAnEarlierSegmentsBytes(t *testing.T) {
	f := newFixture(t)
	threeRuns(f)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	before, _ := json.Marshal(f.rec.Segments()[0])
	f.user("r4", "more", 60)
	f.calls("r4", "", 61, nil, `c9 a.b {}`)
	f.reply("r4", "done", 62)
	f.user("r5", "end", 70)
	f.reply("r5", "bye", 71)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	segments := f.rec.Segments()
	if len(segments) < 2 {
		t.Fatalf("segments = %d", len(segments))
	}
	if after, _ := json.Marshal(segments[0]); string(after) != string(before) {
		t.Errorf("Segment 1 changed:\n%s\n%s", before, after)
	}
}
