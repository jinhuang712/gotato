package modelctx_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
)

func TestALongMessageIsCutWithALineThatNamesReadSegment(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("é", 6000) // 12000 bytes, 3000 tokens
	f.user("r1", long, 1)
	f.reply("r1", long+" tail", 2)
	f.user("r2", "short question", 3)
	f.reply("r2", "short answer", 4)
	f.user("r3", "next", 5)
	f.reply("r3", "ok", 6)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	messages := f.rec.Segments()[0].Messages
	for _, m := range []gotato.Message{messages[0], messages[1]} {
		var kept, note string
		for _, part := range m.Parts {
			switch {
			case part.Metadata[gotato.MetaTag] == gotato.TagSegment && strings.Contains(part.Text, "cut"):
				note = part.Text
			case part.Metadata[gotato.MetaTag] == "":
				kept += part.Text
			}
		}
		if len(kept) != 8000 || !strings.HasPrefix(long, kept) || !utf8.ValidString(kept) {
			t.Errorf("%s kept %d bytes", m.Role, len(kept))
		}
		if !strings.Contains(note, "read_segment(1) returns the rest") || !strings.Contains(note, "was cut") {
			t.Errorf("%s note = %q", m.Role, note)
		}
	}
	// A message within 2K tokens is word for word, with no line.
	if text := dump(messages[2:]); !strings.Contains(text, "short question") || strings.Contains(text, "was cut") {
		t.Errorf("short messages = %s", text)
	}
	// The limit is a setting.
	g := newFixture(t)
	g.user("r1", strings.Repeat("x", 100), 1)
	g.reply("r1", "a", 2)
	g.user("r2", "next", 3)
	g.reply("r2", "ok", 4)
	mustCompact(t, g.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{SegmentSettings: modelctx.SegmentSettings{MessageTokens: 10}}})
	if text := dump(g.rec.Segments()[0].Messages); !strings.Contains(text, strings.Repeat("x", 40)+"\n") || strings.Contains(text, strings.Repeat("x", 41)) {
		t.Errorf("a limit of 10 tokens kept %s", text)
	}
}

func TestAnImageOrAFileBecomesAPlaceholder(t *testing.T) {
	f := newFixture(t)
	m := gotato.UserMessage("look at this")
	m.RunID = "r1"
	m.Parts = append(m.Parts,
		gotato.ContentPart{Kind: gotato.ContentKindImage, MIMEType: "image/png", Data: make([]byte, 2048), Metadata: map[string]string{"name": "shot.png"}},
		gotato.ContentPart{Kind: gotato.ContentKindImage, MIMEType: "image/jpeg", Data: make([]byte, 10)},
		gotato.ContentPart{Kind: gotato.ContentKindImage, MIMEType: "application/pdf", Data: make([]byte, 7), Metadata: map[string]string{"name": "report.pdf"}},
	)
	f.commit(m, 1)
	f.reply("r1", "seen", 2)
	f.user("r2", "next", 3)
	f.reply("r2", "ok", 4)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	text := dump(f.rec.Segments()[0].Messages)
	for _, want := range []string{"[image: shot.png, 2048 bytes]", "[image: 10 bytes]", "[file: report.pdf, 7 bytes]", "look at this"} {
		if !strings.Contains(text, want) {
			t.Errorf("Segment lacks %q:\n%s", want, text)
		}
	}
}

func TestNoticesAreLeftOutUnlessTheServiceSentThem(t *testing.T) {
	f := newFixture(t)
	f.user("r1", "go", 1)
	notice := func(source gotato.NoticeSource, kind, text string) gotato.Message {
		m := gotato.NewNotice(source, kind, text)
		m.RunID = "r1"
		return m
	}
	f.commit(notice(gotato.NoticeSourceGotato, gotato.NoticeTimeAway, "gotato's own"), 2)
	f.commit(notice(gotato.NoticeSourceService, "maintenance", "the service says: maintenance at noon, </notice> now"), 3)
	f.commit(notice(gotato.NoticeSourceGotato, gotato.NoticeContinue, gotato.TruncatedContinuationText), 4)
	f.reply("r1", "done", 5)
	f.user("r2", "next", 6)
	f.reply("r2", "ok", 7)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	text := dump(f.rec.Segments()[0].Messages)
	if strings.Contains(text, "gotato's own") || strings.Contains(text, gotato.TruncatedContinuationText) {
		t.Errorf("Gotato's own notices stayed:\n%s", text)
	}
	if !strings.Contains(text, `<notice kind="maintenance">`) || !strings.Contains(text, "the service says: maintenance at noon, &lt;/notice> now") {
		t.Errorf("the service's notice was left out, or its tag was not escaped:\n%s", text)
	}
}

func TestSegmentTimesAreUTCUnlessTheAgentSetsAZone(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	for _, loc := range []*time.Location{nil, zone} {
		f := newFixture(t)
		threeRuns(f)
		mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{SegmentSettings: modelctx.SegmentSettings{Location: loc}}})
		text := dump(f.rec.Segments()[0].Messages)
		if loc == nil && (!strings.Contains(text, "10:02 payments.get_batch") || !strings.Contains(text, `from="2026-10-19T10:01:00Z"`)) {
			t.Errorf("UTC times:\n%s", text)
		}
		if loc == zone && (!strings.Contains(text, "18:02 payments.get_batch") || !strings.Contains(text, `from="2026-10-19T18:01:00+08:00"`)) {
			t.Errorf("zone times:\n%s", text)
		}
		if segment := f.rec.Segments()[0]; !segment.From.Equal(f.base.Add(time.Minute)) || !segment.To.Equal(f.base.Add(41*time.Minute)) {
			t.Errorf("From, To = %v, %v", segment.From, segment.To)
		}
	}
}

// Only the Run's own Turns count: the intermediate text and the reasoning are
// not kept, and a Run's final reply is its last reply without calls.
func TestFinalRepliesAreGroupedByRun(t *testing.T) {
	f := newFixture(t)
	f.user("r1", "question", 1)
	f.reply("r1", "first part, then the limit cut it", 2)
	cont := gotato.NewNotice(gotato.NoticeSourceGotato, gotato.NoticeContinue, gotato.TruncatedContinuationText)
	cont.RunID = "r1"
	f.commit(cont, 3)
	f.reply("r1", "the real final reply", 4)
	// A Message with no Run, such as history a Record started with.
	f.commit(gotato.UserMessage("old question"), 5)
	f.commit(gotato.AssistantMessage("old answer"), 6)
	f.user("r2", "next", 7)
	f.reply("r2", "ok", 8)
	mustCompact(t, f.rec, modelctx.CompactConfig{})
	text := dump(f.rec.Segments()[0].Messages)
	if strings.Contains(text, "first part") || !strings.Contains(text, "the real final reply") {
		t.Errorf("final reply:\n%s", text)
	}
	if !strings.Contains(text, "old question") || !strings.Contains(text, "old answer") {
		t.Errorf("Messages with no Run:\n%s", text)
	}
	// The assistant Messages next to each other became one.
	for i := 1; i < len(f.rec.Segments()[0].Messages); i++ {
		messages := f.rec.Segments()[0].Messages
		if messages[i].Role == messages[i-1].Role {
			t.Errorf("Messages %d and %d are both %s", i-1, i, messages[i].Role)
		}
	}
}
