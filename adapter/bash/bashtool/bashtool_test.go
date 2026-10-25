package bashtool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/adapter/bash"
	"github.com/jinhuang712/gotato/adapter/bash/bashtool"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/workload"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func bashCall(id, command string) gotato.ToolCall {
	arguments, _ := json.Marshal(map[string]string{"command": command})
	return gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: "bash", Arguments: arguments}
}

func exportCall(id string) gotato.ToolCall {
	return gotato.ToolCall{ID: gotato.ToolCallID(id), ToolID: testkit.BashLookupExportID, Arguments: []byte(`{}`)}
}

func newRunner(t *testing.T, store session.Store, model gotato.Model, extra ...func(*service.Agent)) *service.Runner {
	t.Helper()
	agent := service.Agent{
		Name:         "a",
		Model:        model,
		Tools:        []gotato.Tool{testkit.BashLookupExport()},
		SessionTools: []service.SessionToolsFunc{bashtool.Tools()},
	}
	for _, apply := range extra {
		apply(&agent)
	}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// resultOf returns the Tool Result of a call in a request.
func resultOf(t *testing.T, request gotato.ModelRequest, call string) gotato.ToolResult {
	t.Helper()
	for _, message := range request.Messages {
		if r := message.ToolResult; r != nil && string(r.CallID) == call {
			return *r
		}
	}
	t.Fatalf("no result of call %s", call)
	return gotato.ToolResult{}
}

func textOf(r gotato.ToolResult) string {
	var b strings.Builder
	for _, part := range r.Content {
		b.WriteString(part.Text)
	}
	return b.String()
}

// The reference task, played through a Runner by the workload driver: the
// model finds a line with grep and a field with jq in a 1 MB spilled result and
// answers in a sentence. Nothing fails, and every Turn's Context matches its
// inspection.
func TestBashLookupReferenceTask(t *testing.T) {
	task := testkit.BashLookupTask(bashtool.Tools())
	report, err := workload.Run(context.Background(), workload.Config{Plays: 2}, []testkit.Task{task})
	if err != nil {
		t.Fatal(err)
	}
	got := report.Tasks[0]
	if got.Passed != 2 || got.ToolCalls != 6 || len(got.Failures) != 0 || report.GotatoFailures != 0 || report.ContextMismatches != 0 || got.ContextTurns == 0 {
		t.Fatalf("%+v", report)
	}
}

// The task's answer is short, the 1 MB result is never in a request, and the
// request after each call stays small.
func TestBashLookupKeepsRequestsSmall(t *testing.T) {
	model := testkit.BashLookupTask(bashtool.Tools()).Model
	runner := newRunner(t, session.NewMemoryStore(), model)
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: testkit.BashLookupPrompt})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FinalText) > 200 || !strings.Contains(result.FinalText, testkit.BashLookupOrder) || !strings.Contains(result.FinalText, testkit.BashLookupTotal()) {
		t.Fatalf("answer %q", result.FinalText)
	}
	got, _ := runner.Session(context.Background(), result.SessionID)
	for _, message := range got.Messages() {
		if message.ToolResult != nil && string(message.ToolResult.CallID) == "export-1" && message.ToolResult.Spilled == nil {
			t.Fatal("the export was not spilled")
		}
	}
	if !strings.Contains(result.FinalText, "line ") {
		t.Fatalf("answer %q has no line number", result.FinalText)
	}
}

// G-G09: the same Record gives the same request and the same bash output on
// another instance, which reads the body from the store.
func TestSameRecordSameBashOutputOnAnotherInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	commands := []string{testkit.BashLookupGrepCommand(), testkit.BashLookupJQCommand(), "ls -l /results/export-1", "ls /results | grep -c export", "head -c 300 /results/export-1 | wc -c"}
	script := func() gotato.Model {
		var scripts []testkit.Script
		scripts = append(scripts, testkit.ToolCalls(exportCall("export-1")), testkit.Text("exported"))
		var calls []gotato.ToolCall
		for i, command := range commands {
			calls = append(calls, bashCall(fmt.Sprintf("b-%d", i), command))
		}
		scripts = append(scripts, testkit.ToolCalls(calls...), testkit.Text("done"))
		return testkit.NewFakeModel(scripts...)
	}
	first := script().(*testkit.FakeModel)
	runner := newRunner(t, store, first)
	run, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: run.SessionID, Prompt: "query"}); err != nil {
		t.Fatal(err)
	}
	var want []string
	last := first.Requests()[len(first.Requests())-1]
	for i := range commands {
		want = append(want, textOf(resultOf(t, last, fmt.Sprintf("b-%d", i))))
	}
	if !strings.Contains(want[0], "order-88501") || strings.TrimSpace(want[1]) != testkit.BashLookupTotal() {
		t.Fatalf("outputs %q", want)
	}

	// Another instance, with the same store, runs the same commands again.
	again := testkit.NewFakeModel(
		testkit.ToolCalls(func() []gotato.ToolCall {
			var calls []gotato.ToolCall
			for i, command := range commands {
				calls = append(calls, bashCall(fmt.Sprintf("c-%d", i), command))
			}
			return calls
		}()...), testkit.Text("done"))
	other := newRunner(t, store, again)
	if _, err := other.Run(ctx, service.RunRequest{SessionID: run.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	final := again.Requests()[len(again.Requests())-1]
	for i := range commands {
		if got := textOf(resultOf(t, final, fmt.Sprintf("c-%d", i))); got != want[i] {
			t.Errorf("command %q: %q on the other instance, %q on the first", commands[i], got, want[i])
		}
	}

	// The two instances inspect the Session to the same request.
	a, err := runner.Inspect(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := other.Inspect(ctx, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) || !strings.Contains(string(ja), `"bash"`) {
		t.Fatal("two instances build different requests, or the request has no bash tool")
	}
}

// The tool cannot reach another Session's results: a Session that did not make
// the call finds no such file, even on the same Runner and store, and what it
// reads of /results is its own.
func TestBashCannotReachAnotherSession(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	owner := testkit.NewFakeModel(testkit.ToolCalls(exportCall("export-1")), testkit.Text("exported"))
	runner := newRunner(t, store, owner)
	a, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}

	intruder := testkit.NewFakeModel(
		testkit.ToolCalls(
			bashCall("peek-1", "cat /results/export-1 | head -c 50"),
			bashCall("peek-2", "ls /results"),
			bashCall("peek-3", "ls /"),
			bashCall("peek-4", "cat /../results/export-1"),
			bashCall("peek-5", "ls /sessions /tmp /etc"),
		), testkit.Text("done"))
	runner2 := newRunner(t, store, intruder)
	b, err := runner2.Run(ctx, service.RunRequest{Prompt: "look"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID == b.SessionID {
		t.Fatal("the two Runs share a Session")
	}
	last := intruder.Requests()[len(intruder.Requests())-1]
	if got := textOf(resultOf(t, last, "peek-1")); !strings.Contains(got, "No such file") || strings.Contains(got, "orders") {
		t.Errorf("peek-1 = %q", got)
	}
	if got := textOf(resultOf(t, last, "peek-2")); strings.Contains(got, "export-1") {
		t.Errorf("peek-2 lists the other Session's result: %q", got)
	}
	if got := textOf(resultOf(t, last, "peek-3")); strings.TrimSpace(got) != "results" {
		t.Errorf("peek-3 = %q, want only results", got)
	}
	for _, id := range []string{"peek-4", "peek-5"} {
		if got := textOf(resultOf(t, last, id)); strings.Contains(got, `"id"`) || !strings.Contains(got, "[exit code: ") {
			t.Errorf("%s = %q", id, got)
		}
	}
}

// A refused command, an unknown command and a command with no match are Tool
// Results the model reads, with the exit code, and not failed results.
func TestNonZeroExitIsAResultNotAFailure(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(
			bashCall("c-1", "grep -c nothing-like-this /results/none"),
			bashCall("c-2", "for f in /results/*; do cat $f; done"),
			bashCall("c-3", "python3 -c 1"),
			bashCall("c-4", "echo hello"),
			bashCall("c-5", "echo -n"),
			bashCall("c-6", "echo ok; echo oops >&2; exit 3"),
			bashCall("c-7", "echo a | grep -q x"),
		), testkit.Text("done"))
	runner := newRunner(t, session.NewMemoryStore(), model)
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	last := model.Requests()[len(model.Requests())-1]
	for id, want := range map[string]string{
		"c-1": "[exit code: 2]",
		"c-2": "[exit code: 2]",
		"c-3": "[exit code: 127]",
		"c-4": "hello",
		"c-7": "[exit code: 1]",
	} {
		r := resultOf(t, last, id)
		if r.Status != gotato.ToolResultOK || !strings.Contains(textOf(r), want) {
			t.Errorf("%s: status %s, text %q, want %q", id, r.Status, textOf(r), want)
		}
	}
	if got := textOf(resultOf(t, last, "c-2")); !strings.Contains(got, "[stderr]\nbash: ") || !strings.Contains(got, "pipeline") {
		t.Errorf("a refused command says %q", got)
	}
	if got := textOf(resultOf(t, last, "c-5")); got != "(no output)" {
		t.Errorf("true = %q", got)
	}
}

// A bash result over one page is spilled by the core like any Tool Result, and
// the spilled output is itself a file of /results that the tool reads.
func TestSpilledBashOutputIsReadableThroughBash(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(exportCall("export-1")),
		testkit.ToolCalls(bashCall("big-1", "grep -n note /results/export-1 | head -1000")),
		testkit.ToolCalls(bashCall("again-1", "grep -c note /results/big-1"), bashCall("again-2", "tail -n 1 /results/big-1")),
		testkit.Text("done"))
	runner := newRunner(t, session.NewMemoryStore(), model)
	if _, err := runner.Run(ctx, service.RunRequest{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	big := resultOf(t, requests[2], "big-1")
	if len(textOf(big)) > 9000 || !strings.Contains(textOf(big), `read_result(ref="result:big-1"`) {
		t.Fatalf("the bash output was not spilled with a ref: %d bytes", len(textOf(big)))
	}
	last := requests[len(requests)-1]
	if got := strings.TrimSpace(textOf(resultOf(t, last, "again-1"))); got != "1000" {
		t.Errorf("lines of the spilled output = %q, want 1000", got)
	}
	if got := textOf(resultOf(t, last, "again-2")); !strings.HasPrefix(got, "6001:") {
		t.Errorf("last line = %q", got)
	}
}

// The output of one call is bounded; past it the command is stopped and the
// result says so, and the first bytes are kept.
func TestOutputBound(t *testing.T) {
	ctx := context.Background()
	model := testkit.NewFakeModel(
		testkit.ToolCalls(exportCall("export-1")),
		testkit.ToolCalls(bashCall("all-1", "cat /results/export-1")),
		testkit.Text("done"))
	runner := newRunner(t, session.NewMemoryStore(), model)
	run, err := runner.Run(ctx, service.RunRequest{Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	runID := run.SessionID
	last := model.Requests()[len(model.Requests())-1]
	r := resultOf(t, last, "all-1")
	text := textOf(r)
	if len(text) > 9000 || !strings.Contains(text, `read_result(ref="result:all-1"`) {
		t.Fatalf("%d bytes: %.300q", len(text), text)
	}
	// The record holds the bounded output: four pages, then the note that says it was cut.
	record, _ := runner.Session(ctx, runID)
	for _, message := range record.Messages() {
		if r := message.ToolResult; r != nil && r.CallID == "all-1" {
			full := ""
			if r.Spilled != nil {
				full = fmt.Sprint(r.Spilled.Bytes)
			}
			if r.Spilled == nil || r.Spilled.Bytes < bashtool.DefaultMaxOutput || r.Spilled.Bytes > bashtool.DefaultMaxOutput+512 {
				t.Fatalf("spilled body of %s bytes, want about %d", full, bashtool.DefaultMaxOutput)
			}
		}
	}
}

func TestFormat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result bash.Result
		want   string
	}{
		{"stdout only", bash.Result{Stdout: "a\nb\n"}, "a\nb"},
		{"no output", bash.Result{}, "(no output)"},
		{"exit code", bash.Result{ExitCode: 1}, "[exit code: 1]"},
		{"stderr and exit", bash.Result{Stdout: "x", Stderr: "boom\n", ExitCode: 2}, "x\n[stderr]\nboom\n[exit code: 2]"},
		{"cut", bash.Result{Stdout: "abc", Truncated: true, ExitCode: 1}, "abc\n[output cut: more than 3 bytes; narrow the command, for example with grep or head]"},
	} {
		if got := bashtool.Format(tc.result); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The tool's Spec is the same bytes for every Session, its Effect is read, and
// its description tells what the model needs.
func TestSpecIsStable(t *testing.T) {
	a := bashtool.New(session.New(), nil).Spec()
	b := bashtool.New(testkit.NewRecord("q", "a"), nil, bashtool.WithMaxOutput(1<<10)).Spec()
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("the spec depends on the Session")
	}
	if a.ID != "bash" || a.Effect != gotato.EffectRead || !strings.Contains(string(a.InputSchema), `"command"`) {
		t.Fatalf("%+v", a)
	}
	for _, want := range []string{"/results/<call_id>", "result:<call_id>", "--help", "Refused:", "grep", "jq"} {
		if !strings.Contains(a.Description, want) {
			t.Errorf("the description lacks %q", want)
		}
	}
	if len(a.Description) > 1200 {
		t.Errorf("the description is %d bytes", len(a.Description))
	}
	for _, command := range bash.Commands() {
		if !slices.Contains(strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(a.Description)), command) {
			t.Errorf("the description lacks the command %s", command)
		}
	}
}

// A MaxEffect of read keeps the tool; a lower cap cannot be set, since read is
// the lowest.
func TestEffectCapKeepsTheTool(t *testing.T) {
	runner := newRunner(t, session.NewMemoryStore(), testkit.NewFakeModel(testkit.Text("hi")), func(a *service.Agent) { a.MaxEffect = gotato.EffectRead })
	tools, err := runner.Tools(context.Background(), "a", "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range tools {
		ids = append(ids, entry.Spec.ID)
	}
	if !slices.Contains(ids, "bash") {
		t.Fatalf("tools %v", ids)
	}
}
