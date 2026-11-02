package gotato

import (
	"context"
	"strings"
	"testing"
)

// runBig runs one Run in which a Tool returns size bytes, over a Journal that
// keeps what is spilled, and returns the Tool Result as it was committed.
func runBig(t *testing.T, size int, options ...SessionOption) ToolResult {
	t.Helper()
	options = append([]SessionOption{WithModel(echoToolModel("call-1")), WithTools(bigTool{size: size}), WithTranscript(&spillCtxJournal{})}, options...)
	session, err := NewSession(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	for _, m := range session.Transcript().Messages() {
		if m.ToolResult != nil {
			return *m.ToolResult
		}
	}
	t.Fatal("no tool result")
	return ToolResult{}
}

func resultText(r ToolResult) string {
	var b strings.Builder
	for _, part := range r.Content {
		b.WriteString(part.Text)
	}
	return b.String()
}

// previewLen is the length of the preview in the text of a spilled result: the
// bytes before the line the core writes.
func previewLen(text string) int { return strings.Index(text, "\n[Result cut:") }

// A Session spills only with a ReadBack installed, so every ref has a reader.
func TestNothingIsSpilledWithoutAReadBack(t *testing.T) {
	r := runBig(t, 200<<10)
	if r.Spilled != nil || len(resultText(r)) != 200<<10 {
		t.Fatalf("spilled with no ReadBack: %+v, %d bytes", r.Spilled, len(resultText(r)))
	}
}

// The ReadBack's page is the spill threshold: a result of one page stays whole,
// one byte more is spilled, whatever the page.
func TestTheSpillThresholdIsTheReadBackPage(t *testing.T) {
	for _, page := range []int{testPage, 4 * testPage, 100 << 10} {
		at := runBig(t, page, WithReadBack(testReadBack{page}))
		if at.Spilled != nil {
			t.Errorf("page %d: a result of exactly one page was spilled", page)
		}
		over := runBig(t, page+1, WithReadBack(testReadBack{page}))
		if over.Spilled == nil {
			t.Errorf("page %d: a result of one page and a byte was not spilled", page)
		}
	}
}

// The preview counts its pages by the ReadBack's page, and is the start of
// page 1: it never passes the page, whatever SpillPreviewBytes says.
func TestThePreviewFollowsTheReadBackPage(t *testing.T) {
	if int(DefaultLimits().SpillPreviewBytes) <= testPage {
		t.Fatalf("this test needs a default preview over the page of %d", testPage)
	}
	r := runBig(t, 5*testPage+10, WithReadBack(testReadBack{testPage}))
	preview := resultText(r)
	if !strings.Contains(preview, "6 pages") || !strings.Contains(preview, `read_result(ref="result:call-1", page=1)`) {
		t.Fatalf("preview ends %q", preview[max(len(preview)-200, 0):])
	}
	if n := previewLen(preview); n < 0 || n > testPage {
		t.Fatalf("the preview is %d bytes, more than the page of %d", n, testPage)
	}
}

// CoreLimits.SpillPreviewBytes sets the preview, up to the page.
func TestSpillPreviewBytesSetsThePreview(t *testing.T) {
	r := runBig(t, 40<<10, WithReadBack(testReadBack{16 << 10}), WithLimits(CoreLimits{SpillPreviewBytes: 100}))
	preview := resultText(r)
	if n := previewLen(preview); n != 100 {
		t.Fatalf("preview of %d bytes, want 100", n)
	}
	if !strings.Contains(preview, "3 pages") {
		t.Fatalf("preview ends %q", preview[100:])
	}
	if n := previewLen(resultText(runBig(t, 40<<10, WithReadBack(testReadBack{16 << 10})))); n != int(DefaultLimits().SpillPreviewBytes) {
		t.Fatalf("default preview of %d bytes, want %d", n, DefaultLimits().SpillPreviewBytes)
	}
}

type panicReadBack struct{ testReadBack }

func (panicReadBack) PageBytes() int { panic("boom") }

// A ReadBack with no page, a nil one and one whose PageBytes panics are refused
// when the Session is built.
func TestAReadBackThatCannotGiveAPageIsRefused(t *testing.T) {
	for name, option := range map[string]SessionOption{
		"zero page": WithReadBack(testReadBack{0}),
		"nil":       WithReadBack(nil),
		"panic":     WithReadBack(panicReadBack{}),
	} {
		_, err := NewSession(context.Background(), WithModel(echoToolModel("call-1")), option)
		if !IsCode(err, CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
}

// A later WithReadBack replaces an earlier one: the page of the last is the
// page of the Session.
func TestTheLastReadBackWins(t *testing.T) {
	r := runBig(t, 3*testPage, WithReadBack(testReadBack{testPage}), WithReadBack(testReadBack{8 * testPage}))
	if r.Spilled != nil {
		t.Fatal("the first ReadBack's page was used")
	}
}
