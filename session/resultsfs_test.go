package session_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// logCounter counts the reads of the log, to tell a body read from the store
// from one served by the live Record.
type logCounter struct {
	session.Store
	logs atomic.Int64
}

func (s *logCounter) Log(ctx context.Context, id string, after uint64) ([]session.Entry, error) {
	s.logs.Add(1)
	return s.Store.Log(ctx, id, after)
}

func bigText(size int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "row-%07d,paid,41.20,some more text to make the line longer\n", i)
	}
	return b.String()[:size-1] + "\n"
}

// storedSession runs a stored Session in which the model calls demo.read once
// for each call ID in order, and the tool returns that call's body.
func storedSession(t *testing.T, store *logCounter, id string, bodies map[string]string, order ...string) *session.Writer {
	t.Helper()
	writer, err := session.Create(context.Background(), store, session.New(session.WithID(id)), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range order {
		model := testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
			if request.Messages[len(request.Messages)-1].Role == gotato.RoleToolResult {
				return testkit.Text("done"), nil
			}
			return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(call), ToolID: "demo.read", Arguments: []byte(`{}`)}), nil
		})
		s, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(writer), gotato.WithTool(testkit.NewFakeTool("demo.read", bodies[call])))
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.Prompt(context.Background(), gotato.UserMessage("go "+call))
		if err != nil || result.Status != gotato.RunCompleted {
			t.Fatalf("Run = %+v, err = %v", result, err)
		}
		_ = s.Close(context.Background())
	}
	return writer
}

func newBodies() (map[string]string, []string) {
	return map[string]string{"c1": bigText(100 << 10), "c2": "small result\nline two\n", "c3": "x"}, []string{"c1", "c2", "c3"}
}

func TestResultsFSPassesTheStandardSuite(t *testing.T) {
	bodies, order := newBodies()
	store := &logCounter{Store: session.NewMemoryStore()}
	writer := storedSession(t, store, "fstest", bodies, order...)
	fsys := session.ResultsFS(context.Background(), writer.Record(), store)
	if err := fstest.TestFS(fsys, "results/c1", "results/c2", "results/c3"); err != nil {
		t.Fatal(err)
	}
}

// A listing and a Stat read no body; a body is read when its file is opened,
// from the store when the live Record does not hold it.
func TestResultsFSReadsABodyOnDemand(t *testing.T) {
	bodies, order := newBodies()
	store := &logCounter{Store: session.NewMemoryStore()}
	storedSession(t, store, "demand", bodies, order...)

	// A second instance loads the Record from the store: no body is cached.
	record, err := session.View(context.Background(), store, "demand")
	if err != nil {
		t.Fatal(err)
	}
	fsys := session.ResultsFS(context.Background(), record, store)
	before := store.logs.Load()
	entries, err := fs.ReadDir(fsys, "results")
	if err != nil || len(entries) != 3 {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	info, err := fs.Stat(fsys, "results/c1")
	if err != nil || info.Size() != int64(len(bodies["c1"])) {
		t.Fatalf("Stat = %v, %v", info, err)
	}
	if got := store.logs.Load(); got != before {
		t.Fatalf("a listing and a Stat read the log %d times", got-before)
	}

	if got := readAll(t, fsys, "results/c1"); got != bodies["c1"] {
		t.Fatalf("c1 differs: %d bytes, want %d", len(got), len(bodies["c1"]))
	}
	if got := store.logs.Load(); got != before+1 {
		t.Fatalf("opening the spilled result read the log %d times, want 1", got-before)
	}
	// Read again, the body is cached on the Record.
	readAll(t, fsys, "results/c1")
	if got := store.logs.Load(); got != before+1 {
		t.Fatalf("a second read hit the log again")
	}
	// A result under one page is not spilled, and is read from the Record.
	if got := readAll(t, fsys, "results/c2"); got != bodies["c2"] {
		t.Fatalf("c2 = %q", got)
	}
	if got := store.logs.Load(); got != before+1 {
		t.Fatalf("a small result read the log")
	}
	// Nothing reads the body of a file that is never opened.
	file, err := fsys.Open("results/c3")
	if err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 1)
	if n, _ := io.ReadFull(file, part); n != 1 || string(part) != "x" {
		t.Fatalf("part = %q", part)
	}
	_ = file.Close()
}

// The same Record gives the same listing, sizes, times and bytes on two
// instances, one live and one loaded from the store.
func TestResultsFSIsTheSameOnEveryInstance(t *testing.T) {
	bodies, order := newBodies()
	store := &logCounter{Store: session.NewMemoryStore()}
	writer := storedSession(t, store, "same", bodies, order...)
	other, err := session.View(context.Background(), store, "same")
	if err != nil {
		t.Fatal(err)
	}
	dump := func(fsys fs.FS) string {
		var b strings.Builder
		err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "%s %v %d %s\n", name, info.Mode(), info.Size(), info.ModTime().Format("2006-01-02T15:04:05Z07:00"))
			if !d.IsDir() {
				data, err := fs.ReadFile(fsys, name)
				if err != nil {
					return err
				}
				fmt.Fprintf(&b, "%x\n", data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	a := dump(session.ResultsFS(context.Background(), writer.Record(), store))
	b := dump(session.ResultsFS(context.Background(), other, store))
	if a != b {
		t.Fatalf("two instances differ:\n%s\n%s", a[:min(len(a), 400)], b[:min(len(b), 400)])
	}
	if !strings.Contains(a, "results/c1 -r--r--r-- 102400 1970-01-01T00:00:00Z") {
		t.Fatalf("listing = %.300s", a)
	}
}

// Another Session's call is not there, whether or not its ID is the same.
func TestResultsFSReachesOnlyItsOwnSession(t *testing.T) {
	store := &logCounter{Store: session.NewMemoryStore()}
	a := storedSession(t, store, "a", map[string]string{"ca": "from a"}, "ca")
	b := storedSession(t, store, "b", map[string]string{"cb": bigText(100 << 10)}, "cb")
	fsys := session.ResultsFS(context.Background(), a.Record(), store)
	for _, name := range []string{"results/cb", "results/../cb", "cb", "results/ca/x", "/results/ca", "results/b/cb"} {
		if _, err := fsys.Open(name); err == nil {
			t.Errorf("%s opened", name)
		}
	}
	if got := readAll(t, fsys, "results/ca"); got != "from a" {
		t.Fatalf("ca = %q", got)
	}
	if got := readAll(t, session.ResultsFS(context.Background(), b.Record(), store), "results/cb"); len(got) != 100<<10 {
		t.Fatalf("cb = %d bytes", len(got))
	}
}

// A cancelled ctx stops a read, and a body the store no longer has is an
// error that says it was removed.
func TestResultsFSHonoursCancellationAndPruning(t *testing.T) {
	bodies, order := newBodies()
	store := &logCounter{Store: session.NewMemoryStore()}
	storedSession(t, store, "cancel", bodies, order...)
	record, err := session.View(context.Background(), store, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fsys := session.ResultsFS(ctx, record, store)
	cancel()
	if _, err := fsys.Open("results/c1"); err == nil {
		t.Fatal("opened after cancel")
	}

	empty := session.ResultsFS(context.Background(), record, emptyLog{})
	_, err = empty.Open("results/c1")
	if err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("pruned open error = %v", err)
	}
}

func readAll(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

type emptyLog struct{}

func (emptyLog) Log(context.Context, string, uint64) ([]session.Entry, error) { return nil, nil }
