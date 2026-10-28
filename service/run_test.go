package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	itext "github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func TestRunDisposesOfTheLiveSession(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hello", Metadata: map[string]string{"tenant": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Result.Status != gotato.RunStatusCompleted || first.FinalText != "echo: hello" || first.Messages != 2 || first.Agent != "echo" {
		t.Fatalf("first = %+v", first)
	}
	// No agent survives the run; the session does.
	if runner.ActiveRuns() != 0 {
		t.Fatalf("active runs after completion = %d", runner.ActiveRuns())
	}
	s, err := session.View(ctx, store, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if agent, _ := s.Get(service.MetaAgent); agent != "echo" {
		t.Fatalf("session agent metadata = %q", agent)
	}
	if tenant, _ := s.Get("tenant"); tenant != "a" {
		t.Fatalf("request metadata not applied: %v", s.Metadata())
	}
	second, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Messages != 4 || second.Result.RunID == first.Result.RunID {
		t.Fatalf("second = %+v", second)
	}
	if runs := s.Runs(); len(runs) != 1 {
		t.Fatalf("stale handle should not see the second run: %d", len(runs))
	}
	fresh, _ := session.View(ctx, store, first.SessionID)
	if len(fresh.Runs()) != 2 {
		t.Fatalf("store has %d runs, want 2", len(fresh.Runs()))
	}
}

func TestSpecsToolsAndSessionOverrides(t *testing.T) {
	model := testkit.NewFakeModel(
		testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"v"}`)}),
		testkit.Text("done"),
	)
	runner, store := newRunner(t,
		service.Agent{Name: "echo", Model: testkit.EchoModel{}},
		service.Agent{Name: "tools", Model: model, Instruction: "spec instruction", Tools: []gotato.Tool{testkit.DemoEchoTool()}},
	)
	ctx := context.Background()
	// A Session created before the panel was removed may still carry its
	// setting; the Runner ignores it.
	s, err := runner.CreateSession(ctx, "tools", map[string]string{
		service.MetaInstruction: "session instruction",
		"gotato.panel":          "time",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Result.Metrics.ToolCalls != 1 || result.Agent != "tools" {
		t.Fatalf("result = %+v", result)
	}
	request, _ := model.LastRequest()
	if request.SystemInstructions != "session instruction" {
		t.Fatalf("instruction override lost: %q", request.SystemInstructions)
	}
	if len(request.Tools) != 2 || request.Tools[0].ID != testkit.DemoToolID || request.Tools[1].ID != "read_result" {
		t.Fatalf("tools = %+v", request.Tools)
	}
	for _, message := range request.Messages {
		if strings.Contains(gotato.TextOf(message), "<panel>") {
			t.Fatalf("a request carries a panel: %+v", message)
		}
	}
	stored, _ := session.View(ctx, store, s.ID())
	if len(stored.Messages()) != 4 {
		t.Fatalf("stored %d messages, want 4", len(stored.Messages()))
	}
	if _, err := runner.Run(ctx, service.RunRequest{Agent: "nope", Prompt: "x"}); !errors.Is(err, service.ErrUnknownAgent) {
		t.Fatalf("unknown agent err = %v", err)
	}
}

func TestStreamRunInspectCompactFork(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	var kinds []gotato.EventKind
	result, err := runner.StreamRun(ctx, service.RunRequest{Prompt: "hi"}, func(event gotato.Event) error {
		kinds = append(kinds, event.Kind())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The Run's stream is the Session's own stored Events, ending at
	// run_ended; the store's timings are Instance Events and are not in it.
	// A new Session's stream opens with the Events that made it.
	last := len(kinds) - 1
	if kinds[0] != gotato.EventKindSessionCreated || !slices.Contains(kinds, gotato.EventKindRunStarted) || kinds[last] != gotato.EventKindRunEnded {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, kind := range kinds {
		if kind.Class() == gotato.EventClassInstance {
			t.Fatalf("the Run's stream carries %s: %v", kind, kinds)
		}
	}
	for range 3 {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: result.SessionID, Prompt: "more"}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := runner.Inspect(ctx, result.SessionID, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if report.SelectedMessages != 8 || report.PrefixHash == "" {
		t.Fatalf("report = %+v", report)
	}
	compact, err := runner.Compact(ctx, result.SessionID, modelctx.CompactConfig{KeepMessages: 2})
	if err != nil || !compact.Replaced || compact.MessagesAfter != 8 || compact.Compaction.Segment != 1 {
		t.Fatalf("compact = %+v err=%v", compact, err)
	}
	child, err := runner.Fork(ctx, result.SessionID, "")
	if err != nil || child.ParentID() != result.SessionID || child.Len() != 8 || len(child.Segments()) != 1 {
		t.Fatalf("fork = %v err=%v", child, err)
	}
	if _, err := session.View(ctx, store, child.ID()); err != nil {
		t.Fatal("fork not saved")
	}
	if _, err := runner.Inspect(ctx, "missing", service.InspectConfig{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("inspect missing err = %v", err)
	}
}

func TestAutoCompactFromSpecAndSession(t *testing.T) {
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: testkit.EchoModel{}, Compact: modelctx.CompactPolicy{CeilingTokens: 20000}})
	ctx := context.Background()
	var last service.RunResponse
	for i := 0; i < 8 && !last.Compacted; i++ {
		var err error
		// A prompt past 2K tokens is cut in a Segment, so a compaction shrinks it.
		last, err = runner.Run(ctx, service.RunRequest{SessionID: last.SessionID, Prompt: strings.Repeat("word ", 4000)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !last.Compacted {
		t.Fatalf("spec compaction never triggered: %+v", last)
	}
	s, _ := session.View(ctx, store, last.SessionID)
	if len(s.Compactions()) == 0 {
		t.Fatal("no compaction recorded")
	}
}

func TestCallerCancelReportsCanceled(t *testing.T) {
	block := make(chan struct{})
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, store := newRunner(t, service.Agent{Name: "slow", Model: model})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		done <- result
	}()
	waitFor(t, func() bool { return model.Calls() == 1 })
	cancel()
	result := <-done
	if result.Result.Status != gotato.RunStatusCanceled {
		t.Fatalf("caller cancel status = %q err=%v, want canceled", result.Result.Status, result.Result.Error)
	}
	stored, err := session.View(context.Background(), store, result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runs := stored.Runs()
	if len(runs) != 1 || runs[0].Status != gotato.RunStatusCanceled {
		t.Fatalf("session run record = %+v, want canceled", runs)
	}
	close(block)
}

// countingStore wraps a Store and counts its loads.
type countingStore struct {
	session.Store
	mu   sync.Mutex
	gets int
}

func (s *countingStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	return s.Store.Load(ctx, id)
}

func TestRunLoadsTheSessionOnce(t *testing.T) {
	store := &countingStore{Store: session.NewMemoryStore()}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if store.gets != 1 {
		t.Fatalf("a Run loaded the Session %d times, want 1", store.gets)
	}
}

// A canceled Run settles inside its live Session, so its result must keep the
// RunID, Turns and metrics the Session reported. Before the fix, canceling
// the Prompt context made Prompt give up on the settled result, and every
// canceled Run came back with an empty RunID.
func TestCanceledRunKeepsItsRunID(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	model := testkit.NewFakeModel(testkit.Text("slow"))
	model.Block = block
	runner, store := newRunner(t, service.Agent{Name: "slow", Model: model})
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan gotato.RunID, 1)
	done := make(chan service.RunResponse, 1)
	go func() {
		result, _ := runner.StreamRun(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "first"}, func(event gotato.Event) error {
			if event.Kind() == gotato.EventKindRunStarted {
				started <- event.Head().RunID
			}
			return nil
		})
		done <- result
	}()
	runID := <-started
	waitFor(t, func() bool { return model.Calls() == 1 })
	if err := runner.AbortSession(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Result.Status != gotato.RunStatusCanceled || result.Result.RunID != runID || result.Result.Metrics.Turns != 1 {
		t.Fatalf("canceled result = %+v, want status canceled, RunID %s, one Turn", result.Result, runID)
	}
	saved, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	runs := saved.Runs()
	if len(runs) != 1 || runs[0].RunID != runID || runs[0].Status != gotato.RunStatusCanceled {
		t.Fatalf("stored Runs = %+v", runs)
	}
}

func TestRunUpdateSetsMetadataUnlessSkipSave(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Update: map[string]string{"team": "orders"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Update: map[string]string{"team": "billing"}, SkipSave: true}); err != nil {
		t.Fatal(err)
	}
	stored, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if team, _ := stored.Get("team"); team != "orders" || stored.Len() != 2 {
		t.Fatalf("team = %q, messages = %d; want the saved Run's update and its two Messages", team, stored.Len())
	}
}

// A Run refused for its Agent writes nothing, the Update included.
func TestRunRefusedForItsAgentDoesNotApplyTheUpdate(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Agent: "nope", Prompt: "hi", Update: map[string]string{"team": "orders"}})
	if !errors.Is(err, service.ErrUnknownAgent) {
		t.Fatalf("err = %v, want ErrUnknownAgent", err)
	}
	stored, err := session.View(ctx, store, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if team, ok := stored.Get("team"); ok {
		t.Fatalf("the stored Record has team = %q from a refused Run", team)
	}
}

// Continue takes no Prompt: a request with both is refused, not run with the
// Prompt dropped.
func TestRunContinueWithAPromptIsRefused(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "hi", Continue: true})
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) || !gotato.NotRun(err) {
		t.Fatalf("err = %v, want invalid_argument that did not run", err)
	}
}

// StreamRunAdmitted calls admitted once the Run is admitted, whether or not
// there is a sink.
func TestStreamRunAdmittedCallsAdmittedWithANilSink(t *testing.T) {
	runner, _ := newRunner(t)
	calls := 0
	result, err := runner.StreamRunAdmitted(context.Background(), service.RunRequest{Prompt: "hi"}, func() { calls++ }, nil)
	if err != nil || result.FinalText != "echo: hi" {
		t.Fatalf("StreamRunAdmitted = %+v, %v", result, err)
	}
	if calls != 1 {
		t.Fatalf("admitted called %d times, want 1", calls)
	}
}

// A Runner keeps each Session in a Store, so any instance that shares the
// Store can run its next message.
func ExampleRunner_Run() {
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(), // session.NewFileStore(dir), or your own session.Store
		Agents: []service.Agent{{Name: "default", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	defer func() { _ = runner.Close(ctx) }()

	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hello"})
	if err != nil {
		panic(err)
	}
	// The same Session goes on, here or on another instance.
	second, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"})
	if err != nil {
		panic(err)
	}
	fmt.Println(first.FinalText)
	fmt.Println(second.FinalText)
	// Output:
	// echo: hello
	// echo: again
}

// roundModel stands for a gateway Client of a recognized Claude or OpenAI
// provider, which is sent only the current Round's reasoning.
type roundModel struct{ *testkit.FakeModel }

func (roundModel) RoundReasoningOnly() bool { return true }

func thinkingReply(text string) testkit.Script {
	return testkit.Script{
		{Kind: gotato.ModelEventKindReasoningDelta, Text: "plan"},
		{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: []byte(`{"type":"thinking","signature":"sig"}`)},
		{Kind: gotato.ModelEventKindTextDelta, Text: text},
		{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonEndTurn},
	}
}

func storedReasoning(t *testing.T, store session.Store, id string) int {
	t.Helper()
	s, err := session.Open(context.Background(), store, id)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range s.Messages() {
		for _, part := range message.Parts {
			if part.Kind == gotato.ContentKindReasoning {
				count++
			}
		}
	}
	return count
}

// The Runner asks the Agent's Model whether its provider is sent only the
// current Round's reasoning, and if so compaction, on demand and automatic,
// leaves the reasoning out of the Messages it keeps. Any other Model keeps it.
func TestCompactionLeavesOutReasoningForAModelSentOnlyTheCurrentRound(t *testing.T) {
	ctx := context.Background()
	for _, round := range []bool{false, true} {
		model := func() gotato.Model {
			fake := testkit.NewFakeModel(thinkingReply("a"))
			if round {
				return roundModel{fake}
			}
			return fake
		}

		runner, store := newRunner(t, service.Agent{Name: "a", Model: model()})
		first, err := runner.Run(ctx, service.RunRequest{Agent: "a", Prompt: "u1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "u2"}); err != nil {
			t.Fatal(err)
		}
		result, err := runner.Compact(ctx, first.SessionID, modelctx.CompactConfig{KeepMessages: 2})
		if err != nil || !result.Replaced {
			t.Fatalf("round=%v: compact = %+v, %v", round, result, err)
		}
		want := 1
		if round {
			want = 0
		}
		if got := storedReasoning(t, store, first.SessionID); got != want {
			t.Errorf("round=%v: Compact kept %d reasoning Parts, want %d", round, got, want)
		}

		runner, store = newRunner(t, service.Agent{Name: "a", Model: model(), Compact: modelctx.CompactPolicy{WindowTokens: 4000, SegmentSettings: modelctx.SegmentSettings{TailTokens: 70}}})
		first, err = runner.Run(ctx, service.RunRequest{Agent: "a", Prompt: "u1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "u2"}); err != nil {
			t.Fatal(err)
		}
		third, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "u3",
			Update: map[string]string{service.MetaCompactCeiling: "1"}})
		// A ceiling of one token can never be met, so the Run ends after its
		// compaction; the compaction is what this checks. The window of 4000
		// tokens leaves the tail its 70.
		if !gotato.IsCode(err, gotato.CodeLimitExceeded) || !third.Compacted {
			t.Fatalf("round=%v: the third Run did not compact first: %+v, %v", round, third, err)
		}
		// The kept tail is u2 and its reply.
		want = 1
		if round {
			want = 0
		}
		if got := storedReasoning(t, store, first.SessionID); got != want {
			t.Errorf("round=%v: AutoCompact kept %d reasoning Parts, want %d", round, got, want)
		}
	}
}

const agentRecap = "## What happened\n- the user asked three things\n## Key facts\n- order 42 is shipped\n"

// isAsk reports whether the last message of a request is the Recap's ask.
func isAsk(request gotato.ModelRequest) bool {
	n := len(request.Messages)
	return n > 0 && strings.HasPrefix(gotato.TextOf(request.Messages[n-1]), itext.RecapAskPrefix)
}

// recapModel answers a Recap ask with agentRecap and every other request with
// a short reply.
func recapModel() *testkit.FuncModel {
	return testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		if isAsk(request) {
			return testkit.Text(agentRecap), nil
		}
		return testkit.Text("ok"), nil
	})
}

func asks(model *testkit.FuncModel) []gotato.ModelRequest {
	var out []gotato.ModelRequest
	for _, request := range model.Requests() {
		if isAsk(request) {
			out = append(out, request)
		}
	}
	return out
}

func runThree(t *testing.T, runner *service.Runner) string {
	t.Helper()
	var id string
	for _, prompt := range []string{"one", "two", "three"} {
		result, err := runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: prompt})
		if err != nil {
			t.Fatal(err)
		}
		id = result.SessionID
	}
	return id
}

func TestTheModelsRecapIsOffUnlessTheAgentTurnsItOn(t *testing.T) {
	model := recapModel()
	runner, _ := newRunner(t, service.Agent{Name: "ops", Model: model, Instruction: "be brief"})
	id := runThree(t, runner)
	result, err := runner.Compact(context.Background(), id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "none" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	if len(asks(model)) != 0 {
		t.Fatalf("the model was asked for a Recap by default")
	}
}

func TestAnAgentTurnsTheModelsRecapOnAndItAsksOnTheNextCallsContext(t *testing.T) {
	model := recapModel()
	agent := service.Agent{Name: "ops", Model: model, Instruction: "be brief", Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}}}
	runner, store := newRunner(t, agent)
	ctx := context.Background()
	id := runThree(t, runner)
	before, err := runner.Inspect(ctx, id, service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Compact(ctx, id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "model" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
	sent := asks(model)
	if len(sent) != 1 {
		t.Fatalf("asks = %d", len(sent))
	}
	n := len(before.Request.Messages)
	prefix := sent[0]
	prefix.Messages = prefix.Messages[:n]
	prefix.Config.MaxTokens = before.Request.Config.MaxTokens
	want, _ := json.Marshal(before.Request)
	got, _ := json.Marshal(prefix)
	if string(want) != string(got) || len(sent[0].Messages) != n+1 {
		t.Fatalf("the Recap's request does not start with the next call's request:\n got %s\nwant %s", got, want)
	}
	if sent[0].CacheKey == "" || sent[0].CacheKey != before.Request.CacheKey {
		t.Errorf("cache key = %q, want %q", sent[0].CacheKey, before.Request.CacheKey)
	}
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := record.Segments()[0].Recap; got != strings.TrimSpace(agentRecap) {
		t.Errorf("Segment Recap = %q", got)
	}
	for _, m := range record.Messages() {
		if strings.Contains(gotato.TextOf(m), itext.RecapAskPrefix) {
			t.Errorf("the ask was stored: %q", gotato.TextOf(m))
		}
	}
}

func TestAFailedModelRecapKeepsTheSegmentWithoutOne(t *testing.T) {
	model := testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
		return testkit.Text("Sure, here you go: nothing."), nil
	})
	runner, _ := newRunner(t, service.Agent{Name: "ops", Model: model, Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}}})
	id := runThree(t, runner)
	result, err := runner.Compact(context.Background(), id, modelctx.CompactConfig{})
	if err != nil || !result.Replaced || result.Compaction.Recap != "none" {
		t.Fatalf("compact = %+v, %v", result, err)
	}
}

func TestACompactionPastTheCeilingWritesTheRecapWithTheModelWhenTheAgentAsks(t *testing.T) {
	model := recapModel()
	agent := service.Agent{Name: "ops", Model: model, Compact: modelctx.CompactPolicy{CeilingTokens: 20000, Recap: modelctx.ModelRecap{}}}
	runner, store := newRunner(t, agent)
	ctx := context.Background()
	var last service.RunResponse
	for i := 0; i < 8 && !last.Compacted; i++ {
		var err error
		last, err = runner.Run(ctx, service.RunRequest{SessionID: last.SessionID, Prompt: strings.Repeat("word ", 4000)})
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := session.View(ctx, store, last.SessionID)
	if err != nil || len(record.Compactions()) == 0 {
		t.Fatalf("no compaction: %v", err)
	}
	if got := record.Compactions()[0].Recap; got != "model" || len(asks(model)) == 0 {
		t.Fatalf("recap = %q, asks = %d", got, len(asks(model)))
	}
}

// RunResult.Events, the CLI's "events" field, still counts the Events a Run
// produced when it is not saved: those of its own stream.
func TestSkipSaveRunReportsItsEvents(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	for _, existing := range []bool{false, true} {
		request := service.RunRequest{Prompt: "hi", SkipSave: true}
		if existing {
			first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
			if err != nil {
				t.Fatal(err)
			}
			request.SessionID = first.SessionID
		}
		var streamed []gotato.Event
		out, err := runner.StreamRun(ctx, request, func(e gotato.Event) error {
			streamed = append(streamed, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		stored := len(storedOnly(streamed))
		if stored == 0 || out.Events != stored {
			t.Fatalf("existing session %v: RunResult.Events = %d, the stream carried %d stored Events", existing, out.Events, stored)
		}
	}
}

// A Run that is not saved leaves no Event in the Record, so its stream does not
// number its Events as Record positions: it numbers them from 1, on its own
// stream, and the next saved Run goes on from the Record's head.
func TestSkipSaveRunDoesNotUseRecordNumbers(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := runner.Events(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	head := before[len(before)-1].Head().Number

	var skipped []gotato.Event
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two", SkipSave: true}, func(e gotato.Event) error {
		skipped = append(skipped, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stored := storedOnly(skipped)
	if len(stored) == 0 || stored[0].Head().Number != 1 {
		t.Fatalf("the unsaved Run's first stored Event is numbered %v, want 1: its numbers are not Record positions", numbered(stored))
	}
	if after, err := session.StoredEvents(ctx, store, first.SessionID, head, 0); err != nil || len(after) != 0 {
		t.Fatalf("the unsaved Run wrote %d Events (%v)", len(after), err)
	}

	var saved []gotato.Event
	if _, err := runner.StreamRun(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "three"}, func(e gotato.Event) error {
		saved = append(saved, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := storedOnly(saved)[0].Head().Number; got != head+1 {
		t.Fatalf("the next saved Run starts at %d, want %d, after the Record's head", got, head+1)
	}
}
