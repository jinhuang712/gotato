package modelctx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// countingStore counts the reads of the log, to tell a body read from the
// store from one served by the live Record.
type countingStore struct {
	session.Store
	logs atomic.Int64
}

func (s *countingStore) Log(ctx context.Context, id string, after uint64) ([]session.Entry, error) {
	s.logs.Add(1)
	return s.Store.Log(ctx, id, after)
}

// spillFixture is a stored Session whose Runs call a tool that returns body.
type spillFixture struct {
	t      *testing.T
	store  *countingStore
	writer *session.Writer
}

func newSpillFixture(t *testing.T, id string) *spillFixture {
	t.Helper()
	store := &countingStore{Store: session.NewMemoryStore()}
	writer, err := session.CreateWriter(context.Background(), store, session.New(session.WithID(id)), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	return &spillFixture{t: t, store: store, writer: writer}
}

// call runs one prompt in which the model calls demo.read once (call ID id) and
// then answers. The tool returns body.
func (f *spillFixture) call(id, body string) []gotato.ModelRequest {
	f.t.Helper()
	model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		if len(testkit.ToolResults(request)) > 0 && request.Messages[len(request.Messages)-1].Role == gotato.RoleToolResult {
			return testkit.Text("done"), nil
		}
		return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: "demo.read", Arguments: []byte(`{}`)}), nil
	})
	session, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(f.writer),
		gotato.WithTools(testkit.NewFakeTool("demo.read", body)),
		gotato.WithTools(modelctx.ReadResultTool(f.writer.Record(), f.store, modelctx.ReadResultConfig{})))
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

func resultOf(t *testing.T, record *session.Record, call string) gotato.ToolResult {
	t.Helper()
	for _, message := range record.Messages() {
		if message.ToolResult != nil && string(message.ToolResult.CallID) == call {
			return *message.ToolResult
		}
	}
	t.Fatalf("no Tool Result of call %s", call)
	return gotato.ToolResult{}
}

func textOf(result gotato.ToolResult) string {
	var b strings.Builder
	for _, part := range result.Content {
		b.WriteString(part.Text)
	}
	return b.String()
}

func bigBody(size int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "row-%07d,paid,41.20,some more text to make the line longer\n", i)
	}
	return b.String()[:size-1] + "\n"
}

func readResult(t *testing.T, tool gotato.Tool, arguments string) (string, error) {
	t.Helper()
	result, err := tool.Execute(context.Background(), gotato.ToolUse{ArgumentsJSON: []byte(arguments)}, nil)
	if err != nil {
		return "", err
	}
	return textOf(result), nil
}

// A Tool Result of 1 MB is spilled: the Context holds a preview and a ref, a
// load reads no body, read_result pages through all of it, and two instances
// build the same request from the same Record.
func TestALargeToolResultIsSpilledAndReadInPages(t *testing.T) {
	f := newSpillFixture(t, "big")
	body := bigBody(1 << 20)
	requests := f.call("c1", body)

	// The model's second request carries the preview and the ref, not the body.
	last := requests[len(requests)-1]
	results := testkit.ToolResults(last)
	if len(results) != 1 || len(results[0]) > gotato.SpillPreviewBytes+300 {
		t.Fatalf("%d results, the first %d bytes: the Context should hold a preview", len(results), len(results[0]))
	}
	preview := results[0]
	if !strings.HasPrefix(preview, body[:200]) || !strings.Contains(preview, `read_result(ref="result:c1", page=2)`) || !strings.Contains(preview, "1.0 MiB") {
		t.Fatalf("preview = %q...", preview[:min(len(preview), 400)])
	}
	pages, _ := strconv.Atoi(regexp.MustCompile(`(\d+) pages`).FindStringSubmatch(preview)[1])

	record := f.writer.Record()
	spilled := resultOf(t, record, "c1")
	if spilled.Spilled == nil || spilled.Spilled.Bytes != len(body) || spilled.Spilled.Seq == 0 {
		t.Fatalf("Spilled = %+v", spilled.Spilled)
	}
	// The live Record serves the body from memory: the store's log is not read.
	reads := f.store.logs.Load()
	tool := modelctx.ReadResultTool(record, f.store, modelctx.ReadResultConfig{})
	first, err := readResult(t, tool, `{"ref":"result:c1"}`)
	if err != nil || f.store.logs.Load() != reads || !strings.HasPrefix(first, fmt.Sprintf("Result result:c1, %d bytes. Page 1 of %d.\n", len(body), pages)) {
		t.Fatalf("first page (err %v, %d log reads after %d): %.120q", err, f.store.logs.Load(), reads, first)
	}

	// A load reads facts only: no spilled body, and the Message is small.
	loaded, err := f.store.Load(context.Background(), "big")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range loaded.Facts {
		if entry.Kind == gotato.EventKindToolResultSpilled || len(entry.Data) > 64<<10 {
			t.Fatalf("a load read entry %d (%s, %d bytes)", entry.Seq, entry.Kind, len(entry.Data))
		}
	}
	a, err := session.Open(context.Background(), f.store, "big")
	if err != nil {
		t.Fatal(err)
	}
	b, err := session.Open(context.Background(), f.store, "big")
	if err != nil {
		t.Fatal(err)
	}
	encode := func(r *session.Record) string {
		out, err := json.Marshal(gotato.ForModel(r.Messages()))
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if encode(a) != encode(b) || encode(a) != encode(record) || len(encode(a)) > 40<<10 {
		t.Fatalf("two instances disagree, or the request is large (%d bytes)", len(encode(a)))
	}

	// The reloaded instance reads the body from the store, page by page, and
	// gets all of it back.
	reloaded := modelctx.ReadResultTool(a, f.store, modelctx.ReadResultConfig{})
	var got []string
	for page := 1; ; page++ {
		text, err := readResult(t, reloaded, fmt.Sprintf(`{"ref":"result:c1","page":%d}`, page))
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		head, rest, _ := strings.Cut(text, "\n")
		if want := fmt.Sprintf("Page %d of %d.", page, pages); !strings.HasSuffix(head, want) {
			t.Fatalf("page %d starts %q, want it to end with %q", page, head, want)
		}
		more := fmt.Sprintf("\nMore: read_result(ref=%q, page=%d).", "result:c1", page+1)
		if page < pages {
			if !strings.HasSuffix(rest, more) {
				t.Fatalf("page %d does not say how to read the next: %.80q", page, rest[max(0, len(rest)-120):])
			}
			got = append(got, strings.TrimSuffix(rest, more))
			continue
		}
		if !strings.HasSuffix(rest, "\nEnd of result:c1.") {
			t.Fatalf("last page does not end the result")
		}
		got = append(got, strings.TrimSuffix(rest, "\nEnd of result:c1."))
		break
	}
	if joined := strings.Join(got, "\n") + "\n"; joined != body {
		t.Fatalf("the pages give %d bytes, want the %d of the body", len(joined), len(body))
	}
	if _, err := readResult(t, reloaded, fmt.Sprintf(`{"ref":"result:c1","page":%d}`, pages+1)); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("has %d pages", pages)) {
		t.Errorf("a page past the end: %v", err)
	}
}

// The line is one page of text: a result of one page is committed whole, one
// byte more is spilled.
func TestAResultJustUnderThePageIsNotSpilled(t *testing.T) {
	f := newSpillFixture(t, "edge")
	f.call("c1", strings.Repeat("a", gotato.SpillPageBytes))
	f.call("c2", strings.Repeat("b", gotato.SpillPageBytes+1))
	record := f.writer.Record()
	if r := resultOf(t, record, "c1"); r.Spilled != nil || len(textOf(r)) != gotato.SpillPageBytes {
		t.Errorf("a result of one page was spilled: %+v", r.Spilled)
	}
	if r := resultOf(t, record, "c2"); r.Spilled == nil {
		t.Error("a result of one page and a byte was not spilled")
	}
}

// A preview is cut on a rune boundary.
func TestAPreviewIsCutOnARune(t *testing.T) {
	f := newSpillFixture(t, "runes")
	f.call("c1", strings.Repeat("é", gotato.SpillPageBytes))
	preview := textOf(resultOf(t, f.writer.Record(), "c1"))
	head, _, _ := strings.Cut(preview, "\n[Result cut")
	if !strings.HasPrefix(preview, head) || len(head) > gotato.SpillPreviewBytes || len(head)%2 != 0 {
		t.Errorf("the preview is %d bytes: cut in the middle of a rune?", len(head))
	}
}

// A result of several MiB, past what one Message may hold, no longer fails the
// Run: it is spilled.
func TestAMultiMegabyteResultNoLongerFailsTheRun(t *testing.T) {
	f := newSpillFixture(t, "huge")
	f.call("c1", bigBody(6<<20))
	if r := resultOf(t, f.writer.Record(), "c1"); r.Spilled == nil || r.Spilled.Bytes != 6<<20 {
		t.Fatalf("Spilled = %+v", r.Spilled)
	}
}

// imageTool returns a long text and an image.
type imageTool struct{}

func (imageTool) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{ID: "demo.read", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`), Effect: gotato.ToolEffectRead}
}

func (imageTool) Execute(context.Context, gotato.ToolUse, gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	return gotato.ToolResult{Content: []gotato.ContentPart{
		{Kind: gotato.ContentKindText, Text: strings.Repeat("a", gotato.SpillPageBytes+10)},
		{Kind: gotato.ContentKindImage, MIMEType: "image/png", Data: []byte("png")},
	}}, nil
}

// Only the text is spilled; an image stays in the Message where it was.
func TestSpillKeepsThePartsThatAreNotText(t *testing.T) {
	f := newSpillFixture(t, "image")
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "demo.read", Arguments: []byte(`{}`)}), testkit.Text("done"))
	agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(f.writer), gotato.WithTools(imageTool{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	if result, err := agent.Prompt(context.Background(), gotato.UserMessage("go")); err != nil || result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	r := resultOf(t, f.writer.Record(), "c1")
	if r.Spilled == nil || len(r.Content) != 2 || r.Content[1].Kind != gotato.ContentKindImage || string(r.Content[1].Data) != "png" {
		t.Fatalf("result = %+v", r)
	}
}

// A ref that names no spilled result of the Session, also one of another
// Session, fails as a Tool Result: the Run goes on.
func TestARefOfNoResultOfThisSessionFailsAsAToolResult(t *testing.T) {
	other := newSpillFixture(t, "other")
	other.call("x1", bigBody(100<<10))
	f := newSpillFixture(t, "mine")
	f.call("c1", bigBody(100<<10))

	for i, ref := range []string{"result:x1", "result:nope", "garbage"} {
		id := fmt.Sprintf("r%d", i)
		model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
			if len(testkit.ToolResults(request)) > 0 && request.Messages[len(request.Messages)-1].Role == gotato.RoleToolResult {
				return testkit.Text("done"), nil
			}
			return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: "read_result", Arguments: fmt.Appendf(nil, `{"ref":%q}`, ref)}), nil
		})
		agent, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(f.writer),
			gotato.WithTools(modelctx.ReadResultTool(f.writer.Record(), f.store, modelctx.ReadResultConfig{})))
		if err != nil {
			t.Fatal(err)
		}
		result, err := agent.Prompt(context.Background(), gotato.UserMessage("read "+ref))
		_ = agent.Close(context.Background())
		if err != nil || result.Status != gotato.RunStatusCompleted {
			t.Fatalf("%s: Run = %+v, err = %v", ref, result, err)
		}
		failed := resultOf(t, f.writer.Record(), id)
		if failed.Status != gotato.ToolResultStatusFailed || !strings.Contains(failed.SafeError, "result:c1") {
			t.Errorf("%s: status %s, error %q: want a failure that lists result:c1", ref, failed.Status, failed.SafeError)
		}
	}
}

// read_result is a sibling of read_segment: it declares read, and offers both
// the ref and the page.
func TestReadResultDeclaresReadAndItsInput(t *testing.T) {
	tool := modelctx.ReadResultTool(session.New(), session.NewMemoryStore(), modelctx.ReadResultConfig{})
	spec := tool.Spec()
	if spec.ID != "read_result" || spec.Effect != gotato.ToolEffectRead || !strings.Contains(string(spec.InputSchema), `"ref"`) || !strings.Contains(string(spec.InputSchema), `"page"`) {
		t.Fatalf("spec = %+v", spec)
	}
}
