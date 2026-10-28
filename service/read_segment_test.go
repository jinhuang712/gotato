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
	if closed, err = runner.Compact(ctx, id, modelctx.CompactConfig{SegmentBudgetTokens: 1}); err != nil || !closed.Replaced || closed.Merge == nil || !closed.Merge.Merged {
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
