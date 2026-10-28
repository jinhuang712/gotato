package service_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A fork inherits the Messages of its parent, and with them the refs of the
// spilled results. The bodies stay in the parent's log, so the ref names it.
func TestForkKeepsSpilledResultsReadable(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	tool, err := gotato.NewFuncTool("large", "large result", func(context.Context, struct{}) (string, error) {
		return strings.Repeat("payload\n", 10000), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "large-call", ToolID: "large", Arguments: []byte(`{}`)}), testkit.Text("done"))
	r, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "large", Model: model, Tools: []gotato.Tool{tool}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(ctx) }()
	out, err := r.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := r.Fork(ctx, out.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := r.Fork(ctx, child.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]*session.Record{"child": child, "grandchild": grandchild} {
		calls, refs := record.SpilledResults()
		if len(calls) != 1 {
			t.Fatalf("%s: spilled refs=%v", name, calls)
		}
		if got := refs[calls[0]].SessionID; got != gotato.SessionID(out.SessionID) {
			t.Fatalf("%s: ref names %q, want the Session that wrote the body", name, got)
		}
		body, pruned, err := record.SpilledBody(ctx, store, refs[calls[0]])
		if err != nil || pruned || len(body) != 80000 {
			t.Fatalf("%s: body bytes=%d pruned=%v err=%v", name, len(body), pruned, err)
		}
	}
	// The parent's own Message is unchanged.
	parent, err := r.Record(ctx, out.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	pcalls, prefs := parent.SpilledResults()
	if prefs[pcalls[0]].SessionID != "" {
		t.Fatalf("parent ref names %q", prefs[pcalls[0]].SessionID)
	}
	// With the parent deleted the body is gone, which reads as pruned.
	if err := r.DeleteSession(ctx, out.SessionID); err != nil {
		t.Fatal(err)
	}
	calls, refs := child.SpilledResults()
	_, pruned, err := child.SpilledBody(ctx, store, refs[calls[0]])
	if err == nil && !pruned {
		t.Fatal("body still readable after the parent was deleted")
	}
}

// A fork inherits the Segments its parent closed. read_result in the fork
// finds a spilled call inside such a Segment in the log of the Session that
// wrote it, not in the fork's own log.
func TestForkReadsASpilledResultInAnInheritedSegment(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("order-88501,paid,41.20,some more text on the line\n", 2000)
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "big-1", ToolID: "orders.export", Arguments: []byte(`{}`)}), testkit.Text("exported"), // parent Run 1
		testkit.Text("second"), // parent Run 2
		testkit.ToolCalls(gotato.ToolCall{ID: "read-1", ToolID: "read_result", Arguments: []byte(`{"ref":"result:big-1"}`)}), testkit.Text("read"), // fork Run
	)
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "a", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("orders.export", body)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runner.Close(ctx) }()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Compact(ctx, first.SessionID, modelctx.CompactConfig{KeepMessages: 2}); err != nil {
		t.Fatal(err)
	}
	child, err := runner.Fork(ctx, first.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, byCall := child.SpilledResults(); len(byCall) != 0 || len(child.Segments()) == 0 {
		t.Fatalf("the fork should hold the call only inside a Segment: refs %v, %d Segments", byCall, len(child.Segments()))
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: child.ID(), Prompt: "read it"}); err != nil {
		t.Fatal(err)
	}
	last := model.Requests()[len(model.Requests())-1]
	results := testkit.ToolResults(last)
	if got := results[len(results)-1]; !strings.HasPrefix(got, "Result result:big-1, ") || !strings.Contains(got, "order-88501") {
		t.Fatalf("read_result in the fork = %.200q", got)
	}
}

// A Tool Result of 1 MB is spilled by a Runner. read_result is in the tool list
// before the result is spilled and after it, byte for byte the same; the model
// reads the rest through it; and an instance that loads the Session builds the
// same request as the one that ran.
func TestARunnerSpillsALargeResultAndReadsItBack(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	body := strings.Repeat("order-88501,paid,41.20,some more text on the line\n", 21000)
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "big-1", ToolID: "orders.export", Arguments: []byte(`{}`)}), testkit.Text("exported"), // Run 1
		testkit.ToolCalls(gotato.ToolCall{ID: "read-1", ToolID: "read_result", Arguments: []byte(`{"ref":"result:big-1","page":2}`)}), testkit.Text("read"), // Run 2
	)
	runner, err := service.New(service.Config{
		Store:  store,
		Agents: []service.Agent{{Name: "a", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("orders.export", body)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	requests := model.Requests()
	if tools := toolIDsOf(requests[0]); !slices.Contains(tools, "read_result") {
		t.Fatalf("the first request offers %v: read_result should be there before anything is spilled", tools)
	}
	if before, after := toolIDsOf(requests[0]), toolIDsOf(requests[1]); !slices.Equal(before, after) {
		t.Fatalf("the tool list changed on the Turn the result was spilled: %v then %v", before, after)
	}
	results := testkit.ToolResults(requests[1])
	if len(results) != 1 || len(results[0]) > 9000 || !strings.Contains(results[0], `read_result(ref="result:big-1", page=1)`) {
		t.Fatalf("the second request holds %d results, the first of %d bytes", len(results), len(results[0]))
	}

	// Another instance loads the Session and builds the request the first would.
	other, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "a", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("orders.export", body)}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, tools := inspectJSON(t, runner, id)
	b, _ := inspectJSON(t, other, id)
	if a != b || !slices.Contains(tools, "read_result") || len(a) > 40<<10 {
		t.Fatalf("two instances build different requests, or the request is large (%d bytes)", len(a))
	}

	// The model reads page 2 through the other instance, from the store.
	if _, err := other.Run(ctx, service.RunRequest{SessionID: id, Prompt: "read more"}); err != nil {
		t.Fatal(err)
	}
	last := model.Requests()[len(model.Requests())-1]
	page := testkit.ToolResults(last)
	if got := page[len(page)-1]; !strings.HasPrefix(got, "Result result:big-1, ") || !strings.Contains(got, "Page 2 of ") || !strings.Contains(got, "order-88501") {
		t.Fatalf("page 2 = %.200q", got)
	}
}

// An Agent with no tool is offered no read_result: there is nothing to spill.
func TestAnAgentWithNoToolIsOfferedNoReadResult(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "a", Model: testkit.NewFakeModel(testkit.Text("hi"))}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, tools := inspectJSON(t, runner, result.SessionID); slices.Contains(tools, "read_result") {
		t.Fatalf("tools = %v", tools)
	}
}

type bigToolSet struct{ body string }

func (bigToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "orders"} }

func (s bigToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{testkit.NewFakeTool("export", s.body)}, nil
}

// An Agent whose only tools come from a ToolSet spills a large result too, so
// it is offered read_result from its first request, and the tool list does
// not change on the Turn the result is spilled.
func TestAToolSetOnlyAgentIsOfferedReadResult(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("order-88501,paid,41.20,some more text on the line\n", 21000)
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "big-1", ToolID: "orders.export", Arguments: []byte(`{}`)}), testkit.Text("exported"),
	)
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "a", Model: model, Options: []gotato.SessionOption{gotato.WithActiveToolSet(bigToolSet{body})}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx, service.RunRequest{Prompt: "export"})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if tools := toolIDsOf(requests[0]); !slices.Contains(tools, "read_result") {
		t.Fatalf("the first request offers %v: read_result should be there before anything is spilled", tools)
	}
	if before, after := toolIDsOf(requests[0]), toolIDsOf(requests[1]); !slices.Equal(before, after) {
		t.Fatalf("the tool list changed on the Turn the result was spilled: %v then %v", before, after)
	}
	if _, tools := inspectJSON(t, runner, result.SessionID); !slices.Contains(tools, "read_result") {
		t.Fatalf("inspected tools = %v", tools)
	}
}

type gitToolSet struct{}

func (gitToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "git"} }

func (gitToolSet) Tools(context.Context) ([]gotato.Tool, error) {
	return []gotato.Tool{testkit.NewFakeTool("log", "commits")}, nil
}

func load(id string) testkit.Script {
	arguments, _ := json.Marshal(map[string][]string{"names": {id}})
	return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID("load-" + id), ToolID: gotato.SearchToolName, Arguments: arguments})
}

func segmentRunner(t *testing.T, store session.Store, model gotato.Model) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store,
		Agents: []service.Agent{{Name: "a", Model: model, Options: []gotato.SessionOption{
			gotato.WithToolSet(filesToolSet{}), gotato.WithToolSet(gitToolSet{}),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func inspectJSON(t *testing.T, runner *service.Runner, id string) (string, []string) {
	t.Helper()
	report, err := runner.Inspect(context.Background(), id, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report.Request)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded), toolIDsOf(report.Request)
}

// The model gets read_segment once the Session has a Segment, reads the
// Segment back through the Runner, and every instance that loads the Session,
// from a Checkpoint or between two, offers the same Tools and
// builds the same request.
func TestReadSegmentIsOfferedAndEveryInstanceOffersTheSameTools(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	readCall := testkit.ToolCalls(gotato.ToolCall{ID: "read-1", ToolID: "read_segment", Arguments: []byte(`{"segment":1}`)})
	model := testkit.NewFakeModel(
		load("files.read"), testkit.ToolCalls(gotato.ToolCall{ID: "files-1", ToolID: "files.read", Arguments: []byte(`{}`)}), testkit.Text("opened"), // Run 1
		testkit.Text("second"),              // Run 2
		readCall, testkit.Text("I read it"), // Run 3
		testkit.Text("fourth"),                     // Run 4
		load("git.log"), testkit.Text("git is on"), // Run 5, after the merge
	)
	runner := segmentRunner(t, store, model)

	first, err := runner.Run(ctx, service.RunRequest{Prompt: "open the files"})
	if err != nil {
		t.Fatal(err)
	}
	id := first.SessionID
	if _, tools := inspectJSON(t, runner, id); slices.Contains(tools, "read_segment") {
		t.Fatalf("read_segment before any Segment: %v", tools)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "again"}); err != nil {
		t.Fatal(err)
	}
	closed, err := runner.Compact(ctx, id, modelctx.CompactConfig{})
	if err != nil || !closed.Replaced {
		t.Fatalf("compact = %+v, %v", closed, err)
	}
	_, tools := inspectJSON(t, runner, id)
	if !slices.Contains(tools, "read_segment") || !slices.Contains(tools, "files.read") {
		t.Fatalf("tools after the first Segment = %v, want read_segment and the tool the Segment called", tools)
	}
	if entries, err := runner.Tools(ctx, "a", id); err != nil || !slices.ContainsFunc(entries, func(e service.ToolEntry) bool { return e.Spec.ID == "read_segment" && e.Active }) {
		t.Fatalf("Runner.Tools of the Session = %+v, %v", entries, err)
	}
	if entries, _ := runner.Tools(ctx, "a", ""); slices.ContainsFunc(entries, func(e service.ToolEntry) bool { return e.Spec.ID == "read_segment" }) {
		t.Error("the Agent's own surface lists read_segment")
	}

	// Run 3: the model reads Segment 1 back, and the Tool Result is the original.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "what did I open first?"}); err != nil {
		t.Fatal(err)
	}
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	var read string
	for _, message := range record.Messages() {
		if message.ToolResult != nil && message.ToolResult.CallID == "read-1" {
			read = gotato.TextOf(gotato.Message{Parts: message.ToolResult.Content})
		}
	}
	for _, want := range []string{"[user] open the files", "[call " + gotato.SearchToolName + "]", "[assistant] opened", "End of segment 1."} {
		if !strings.Contains(read, want) {
			t.Errorf("read_segment(1) lacks %q:\n%s", want, read)
		}
	}

	// A merge on demand: the Segments pass a budget of one token.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "once more"}); err != nil {
		t.Fatal(err)
	}
	if closed, err = runner.Compact(ctx, id, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{SegmentBudgetTokens: 1}}); err != nil || !closed.Replaced || closed.Merge == nil || !closed.Merge.Merged {
		t.Fatalf("compact = %+v, %v", closed, err)
	}
	if record, err = session.View(ctx, store, id); err != nil {
		t.Fatal(err)
	}
	if segments := record.Segments(); len(segments) != 1 || len(segments[0].Merged) < 2 {
		t.Fatalf("Segments after the merge = %+v", segments)
	}

	// Another instance, from the Checkpoint of the merge: the same request, the
	// same Tools, the same ToolSets.
	other := segmentRunner(t, store, testkit.NewFakeModel())
	wantRequest, wantTools := inspectJSON(t, runner, id)
	gotRequest, gotTools := inspectJSON(t, other, id)
	if gotRequest != wantRequest || !slices.Equal(gotTools, wantTools) || !slices.Contains(gotTools, "read_segment") {
		t.Fatalf("another instance builds another request after the merge:\n got %s\nwant %s", gotRequest, wantRequest)
	}
	viewed, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	wantSets := slices.Clone(viewed.LoadedTools())

	// Between two Checkpoints: the model loads a tool after the merge.
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "show the history"}); err != nil {
		t.Fatal(err)
	}
	later := segmentRunner(t, store, testkit.NewFakeModel())
	wantRequest, wantTools = inspectJSON(t, runner, id)
	gotRequest, gotTools = inspectJSON(t, later, id)
	if gotRequest != wantRequest || !slices.Equal(gotTools, wantTools) || !slices.Contains(gotTools, "git.log") {
		t.Fatalf("another instance builds another request between two Checkpoints:\n got %s\nwant %s", gotRequest, wantRequest)
	}
	if viewed, err = session.View(ctx, store, id); err != nil {
		t.Fatal(err)
	}
	if got := viewed.LoadedTools(); !slices.Equal(got, append(wantSets, "git.log")) {
		t.Fatalf("tools between two Checkpoints = %v, want %v and git.log", got, wantSets)
	}
}

// Inspect takes one InspectConfig: its Instruction replaces the Session's for
// that inspection only.
func TestInspectConfigOverridesTheInstructionForOneInspection(t *testing.T) {
	ctx := context.Background()
	runner, _ := newRunner(t, service.Agent{Name: "a", Model: testkit.EchoModel{}, Instruction: "spec instruction"})
	s, err := runner.CreateSession(ctx, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	over, err := runner.Inspect(ctx, s.ID(), service.InspectConfig{Instruction: "once"})
	if err != nil || over.Request.SystemInstructions != "once" {
		t.Fatalf("override = %q, %v", over.Request.SystemInstructions, err)
	}
	plain, err := runner.Inspect(ctx, s.ID(), service.InspectConfig{})
	if err != nil || plain.Request.SystemInstructions != "spec instruction" {
		t.Fatalf("plain = %q, %v", plain.Request.SystemInstructions, err)
	}
}
