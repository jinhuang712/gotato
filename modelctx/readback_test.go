package modelctx_test

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

// callPaged is spillFixture.call with a ReadBack whose page is page bytes.
func (f *spillFixture) callPaged(id, body string, page int) []gotato.ModelRequest {
	f.t.Helper()
	model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		if len(testkit.ToolResults(request)) > 0 && request.Messages[len(request.Messages)-1].Role == gotato.RoleToolResult {
			return testkit.Text("done"), nil
		}
		return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: "demo.read", Arguments: []byte(`{}`)}), nil
	})
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(f.writer),
		gotato.WithTools(testkit.NewFakeTool("demo.read", body)),
		gotato.WithReadBack(readBackOf(f.writer.Record(), f.store, page)))
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	result, err := session.Prompt(context.Background(), gotato.UserMessage("go "+id))
	if err != nil || result.Status != gotato.RunStatusCompleted {
		f.t.Fatalf("Run = %+v, err = %v", result, err)
	}
	return model.Requests()
}

// pagesOf reads "Page n of N" from the head of a read tool's result and
// returns N, and the bytes of the page's text, after the head and before the
// line that ends it.
func pagesOf(t *testing.T, out string) (total, pageBytes int) {
	t.Helper()
	m := regexp.MustCompile(`Page (\d+) of (\d+)\.\n`).FindStringSubmatchIndex(out)
	if m == nil {
		t.Fatalf("no page head in %q", out[:min(len(out), 300)])
	}
	total, _ = strconv.Atoi(out[m[4]:m[5]])
	body := out[m[1]:]
	if end := strings.LastIndex(body, "\nMore: "); end >= 0 {
		body = body[:end]
	} else if end := strings.LastIndex(body, "\nEnd of "); end >= 0 {
		body = body[:end]
	}
	return total, len(body)
}

// The page size has one owner, the ReadBack: with a page that is not the
// default, the spill threshold, the pages the preview counts and the pages of
// read_result and read_segment all use it. Before, the spill used a fixed
// 32 KiB while a read tool could page by another size.
func TestOnePageSizeOwnsTheSpillThePreviewAndBothReadTools(t *testing.T) {
	const page = 2048
	if readBackOf(nil, nil, page).PageBytes() != page {
		t.Fatalf("PageBytes = %d, want %d", readBackOf(nil, nil, page).PageBytes(), page)
	}
	f := newSpillFixture(t, "page")
	record := f.writer.Record()

	// The spill threshold: a result under one page of this size stays whole,
	// and one over it, far under the default page, is spilled.
	f.callPaged("c0", bigBody(page-10), page)
	if r := resultOf(t, record, "c0"); r.Spilled != nil {
		t.Fatalf("a result of %d bytes, under the page of %d, was spilled", len(textOf(r)), page)
	}
	body := bigBody(5*page + 100)
	requests := f.callPaged("c1", body, page)
	if r := resultOf(t, record, "c1"); r.Spilled == nil || r.Spilled.Bytes != len(body) {
		t.Fatalf("a result of %d bytes, over the page of %d, was not spilled: %+v", len(body), page, r.Spilled)
	}

	// The preview: it is the start of page 1, and counts its pages by this page.
	results := testkit.ToolResults(requests[len(requests)-1])
	preview := results[len(results)-1]
	pages, err := strconv.Atoi(regexp.MustCompile(`(\d+) pages`).FindStringSubmatch(preview)[1])
	if err != nil || pages < 5 {
		t.Fatalf("preview counts %d pages of a %d byte result at a page of %d: %q", pages, len(body), page, preview[max(len(preview)-200, 0):])
	}
	if n := strings.Index(preview, "\n[Result cut:"); n < 0 || n > page {
		t.Fatalf("the preview is %d bytes, more than the page of %d", n, page)
	}

	// read_result: the same number of pages, each within the page.
	tool := resultToolOf(record, f.store, page)
	for n := 1; n <= pages; n++ {
		out, err := readResult(t, tool, `{"ref":"result:c1","page":`+strconv.Itoa(n)+`}`)
		if err != nil {
			t.Fatalf("page %d: %v", n, err)
		}
		total, size := pagesOf(t, out)
		if total != pages || size > page {
			t.Fatalf("page %d: read_result says %d pages of %d bytes, the preview says %d pages of at most %d", n, total, size, pages, page)
		}
	}
	if _, err := readResult(t, tool, `{"ref":"result:c1","page":`+strconv.Itoa(pages+1)+`}`); err == nil {
		t.Fatalf("read_result has a page %d, the preview counted %d", pages+1, pages)
	}

	// read_segment: the Segment that closes both Runs (a Message holds the
	// preview of the spilled result, not its body) is read in pages of the same
	// size, which the default page would have held in one.
	if _, err := modelctx.Compact(context.Background(), record, modelctx.Compactor{}, modelctx.CompactConfig{KeepMessages: 1}); err != nil {
		t.Fatal(err)
	}
	out, err := readSegment(t, segmentToolOf(record, f.store, page), `{"segment":1}`)
	if err != nil {
		t.Fatal(err)
	}
	total, size := pagesOf(t, out)
	if total < 3 || size > page {
		t.Fatalf("read_segment says %d pages of %d bytes at a page of %d", total, size, page)
	}
	if whole, _ := readSegment(t, segmentToolOf(record, f.store, 0), `{"segment":1}`); !strings.Contains(whole, "Page 1 of 1.") {
		t.Fatalf("at the default page the Segment is one page: %q", whole[:min(len(whole), 200)])
	}
}

// With a page above the default, a result between the two is not spilled: the
// default page no longer decides.
func TestAPageAboveTheDefaultSpillsLater(t *testing.T) {
	f := newSpillFixture(t, "wide")
	size := modelctx.DefaultPageBytes + 4096
	f.callPaged("c1", bigBody(size), 4*modelctx.DefaultPageBytes)
	if r := resultOf(t, f.writer.Record(), "c1"); r.Spilled != nil {
		t.Fatalf("a result of %d bytes was spilled under a page of %d", size, 4*modelctx.DefaultPageBytes)
	}
}

// A page below 256 bytes is 256, and none is the default; the ReadBack says
// which, since it owns the value.
func TestReadBackPageBytes(t *testing.T) {
	for page, want := range map[int]int{0: modelctx.DefaultPageBytes, -5: modelctx.DefaultPageBytes, 1: 256, 255: 256, 256: 256, 5000: 5000} {
		if got := (modelctx.ReadBack{PageSize: page}).PageBytes(); got != want {
			t.Errorf("PageSize %d: PageBytes = %d, want %d", page, got, want)
		}
	}
}
