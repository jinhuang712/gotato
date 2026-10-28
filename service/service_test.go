package service_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

type callerKey struct{}

// refusingStore refuses the reads of the caller "mallory", as a store wrapper
// that authorizes by the request's context does, and pauses the first Load of
// anyone else.
type refusingStore struct {
	session.Store
	entered, proceed chan struct{}
	block            atomic.Bool
}

func (s *refusingStore) refused(ctx context.Context) bool { return ctx.Value(callerKey{}) == "mallory" }

func (s *refusingStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	if s.refused(ctx) {
		return session.Loaded{}, session.ErrNotFound
	}
	if s.block.CompareAndSwap(true, false) {
		close(s.entered)
		select {
		case <-s.proceed:
		case <-ctx.Done():
			return session.Loaded{}, ctx.Err()
		}
	}
	return s.Store.Load(ctx, id)
}

func (s *refusingStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if s.refused(ctx) {
		return nil, session.ErrNotFound
	}
	return s.Store.Log(ctx, id, after, limit)
}

// AbortSession asks the store whatever stage the Run is in: a caller the store
// refuses cannot stop a Run that is still loading its Record, as it cannot
// stop one that has a live Session.
func TestAbortSessionAuthorizesWhileTheRunIsLoading(t *testing.T) {
	ctx := context.Background()
	store := &refusingStore{Store: session.NewMemoryStore(), entered: make(chan struct{}), proceed: make(chan struct{})}
	r, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(ctx) })
	rec, err := r.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	store.block.Store(true)
	done := make(chan error, 1)
	go func() {
		_, runErr := r.Run(ctx, service.RunRequest{SessionID: rec.ID(), Prompt: "owner request"})
		done <- runErr
	}()
	<-store.entered
	abortErr := r.AbortSession(context.WithValue(ctx, callerKey{}, "mallory"), rec.ID())
	close(store.proceed)
	if runErr := <-done; runErr != nil {
		t.Fatalf("the owner's Run was stopped by a refused caller: %v", runErr)
	}
	if !errors.Is(abortErr, session.ErrNotFound) {
		t.Fatalf("AbortSession by a refused caller = %v, want not_found", abortErr)
	}
}

// resolvingSet is a ToolSet that blocks while it resolves, as one that dials a
// remote server does, and hands the context it was given to the test.
type resolvingSet struct {
	entered chan context.Context
	release chan struct{}
}

func (resolvingSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "resolving"} }

func (s resolvingSet) Tools(ctx context.Context) ([]gotato.Tool, error) {
	s.entered <- ctx
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return nil, nil
	}
}

// startResolving starts a Run whose ToolSet blocks and returns once the
// ToolSet is resolving.
func startResolving(t *testing.T) (*service.Runner, string, context.Context, chan error, resolvingSet) {
	t.Helper()
	set := resolvingSet{entered: make(chan context.Context, 1), release: make(chan struct{})}
	r, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "slow", Model: testkit.EchoModel{}, Options: []gotato.SessionOption{gotato.WithToolSet(set)}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	rec, err := r.CreateSession(context.Background(), "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, e := r.Run(context.Background(), service.RunRequest{SessionID: rec.ID(), Prompt: "start"})
		done <- e
	}()
	return r, rec.ID(), <-set.entered, done, set
}

// TestAbortCancelsToolSetResolution: an Abort that arrives while the Run
// builds its Session reaches the ToolSet that is resolving.
func TestAbortCancelsToolSetResolution(t *testing.T) {
	r, id, resolving, done, set := startResolving(t)
	if err := r.AbortSession(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	canceled := resolving.Err()
	close(set.release)
	<-done
	if canceled == nil {
		t.Fatal("Abort returned while the ToolSet's context was still live")
	}
}

// TestDrainCancelsToolSetResolution: a Drain whose grace ends while a Run
// builds its Session stops the ToolSet that is resolving.
func TestDrainCancelsToolSetResolution(t *testing.T) {
	r, _, resolving, done, set := startResolving(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = r.Drain(ctx)
	canceled := resolving.Err()
	close(set.release)
	<-done
	if canceled == nil {
		t.Fatal("Drain ended while the ToolSet's context was still live")
	}
}

// These tests run a Runner with a gateway Agent against internal/fakegateway: the
// Phase 10 exit item "an overflow in each provider's form compacts and sends
// again", and the same for a Session past its ceiling. The fake refuses a
// request past its window in each provider's own words and status.

var fakeGatewayWires = []struct {
	name   string
	config gateway.Config
}{
	{fakegateway.APIAnthropic, gateway.Config{API: gateway.APIAnthropicMessages, Provider: gateway.ProviderAnthropic, Model: "claude-sonnet-5-5"}},
	{fakegateway.APIResponses, gateway.Config{API: gateway.APIResponses, Provider: gateway.ProviderOpenAI, Model: "gpt-6.1-sol"}},
	{fakegateway.APIChat, gateway.Config{API: gateway.APIChatCompletions, Provider: gateway.ProviderOther, Model: "chat-model"}},
}

func gatewayRunner(t *testing.T, config gateway.Config, fake *fakegateway.Gateway, policy modelctx.CompactPolicy) *service.Runner {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	config.BaseURL = server.URL
	config.APIKey = "k"
	config.RetryBackoff = time.Millisecond
	client, err := gateway.New(config)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "fake", Model: client, Compact: policy}}})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// compactions returns why each compaction of a Session ran.
func compactions(t *testing.T, runner *service.Runner, id string) []gotato.CompactionWhy {
	t.Helper()
	events, err := runner.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []gotato.CompactionWhy
	for _, event := range events {
		if ended, ok := event.(gotato.CompactionEnded); ok {
			out = append(out, ended.Why)
		}
	}
	return out
}

// describe lists what the fake answered, without the bodies.
func describe(requests []fakegateway.Request) string {
	var out []string
	for _, r := range requests {
		out = append(out, fmt.Sprintf("%s/%d/%dB", r.Outcome, r.Status, len(r.Body)))
	}
	return strings.Join(out, " ")
}

// prompt is a user Message of about 20 KB, past the 2K tokens a Segment keeps
// of one, so a compaction makes the Context smaller.
func prompt(word string) string { return word + " " + strings.Repeat(word+" ", 3000) }

func TestOverflowOnEachWireAPICompactsAndSendsAgainOnce(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			// A window that holds three exchanges of this size and not four, and no
			// ceiling: the refusal is the only thing that says the Context is too long.
			fake := fakegateway.New(fakegateway.WithWindow(33_000))
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{CeilingTokens: -1})
			ctx := context.Background()
			id := ""
			for _, word := range []string{"alpha", "bravo", "charlie"} {
				result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt(word)})
				if err != nil || result.Result.Status != gotato.RunStatusCompleted {
					t.Fatalf("%s: %+v, %v", word, result, err)
				}
				id = result.SessionID
			}
			before := len(fake.Requests())
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt("delta")})
			if err != nil || result.Result.Status != gotato.RunStatusCompleted || !strings.HasPrefix(result.FinalText, "fake: delta") {
				t.Fatalf("delta: %+v, %v", result, err)
			}
			requests := fake.Requests()[before:]
			if len(requests) != 2 || requests[0].Outcome != "refused" || requests[0].Status != 400 || requests[1].Outcome != "ok" {
				t.Fatalf("requests of the Run: %s", describe(requests))
			}
			if len(requests[1].Body) >= len(requests[0].Body) {
				t.Errorf("the request sent again is not smaller: %d bytes after %d", len(requests[1].Body), len(requests[0].Body))
			}
			if got := compactions(t, runner, id); len(got) != 1 || got[0] != gotato.CompactionWhyOverflow {
				t.Fatalf("compactions = %v, want one overflow", got)
			}
			if !strings.Contains(string(requests[1].Body), "delta") {
				t.Error("the prompt of the Run is not in the request sent again")
			}
		})
	}
}

func TestASecondRefusalInTheSameTurnEndsTheRunWithContextOverflowOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			fake := fakegateway.New()
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{CeilingTokens: -1})
			ctx := context.Background()
			// Two Runs of history: the compaction keeps the latest Run before the
			// one that starts as its tail, and closes the Run before that.
			first, err := runner.Run(ctx, service.RunRequest{Prompt: prompt("alpha")})
			if err == nil {
				first, err = runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: prompt("bravo")})
			}
			if err != nil {
				t.Fatal(err)
			}
			// The directive makes the fake refuse every time, as a window the history
			// cannot get under would.
			result, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "charlie [quirk:overflow]"})
			if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Result.Status != gotato.RunStatusFailed {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			if n := len(fake.Requests()); n != 4 {
				t.Fatalf("%d requests (%s), compactions %v, err %v", n, describe(fake.Requests()), compactions(t, runner, first.SessionID), err)
			}
		})
	}
}

func TestPastTheCeilingCompactsBeforeAnyRequestOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			fake := fakegateway.New()
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{CeilingTokens: 12_000})
			ctx := context.Background()
			id := ""
			for _, word := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
				result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt(word)})
				if err != nil || result.Result.Status != gotato.RunStatusCompleted {
					t.Fatalf("%s: %+v, %v", word, result, err)
				}
				id = result.SessionID
			}
			got := compactions(t, runner, id)
			if len(got) == 0 {
				t.Fatal("the Session never compacted")
			}
			for _, why := range got {
				if why != gotato.CompactionWhyPastCeiling {
					t.Errorf("compactions = %v, want only past_ceiling", got)
				}
			}
			for i, request := range fake.Requests() {
				if request.Outcome != "ok" {
					t.Errorf("request %d was %s: a compaction past the ceiling comes before the request", i, request.Outcome)
				}
			}
		})
	}
}

// A provider that says the window ran out in the middle of the reply (Claude
// stops with model_context_window_exceeded, OpenAI sends an error event) ends
// the Run with context_overflow today: the loop compacts and sends again only
// for a request refused before any output, because a reply that has begun
// cannot be taken back (TODO.md, "Does every refusal as too long reach the
// Loop before any output?"). The text received stays as a Cut-off reply.
func TestAnOverflowInTheMiddleOfAReplyEndsTheRunAndKeepsTheCutOffTextOnEachWireAPI(t *testing.T) {
	for _, wire := range fakeGatewayWires {
		t.Run(wire.name, func(t *testing.T) {
			fake := fakegateway.New()
			runner := gatewayRunner(t, wire.config, fake, modelctx.CompactPolicy{CeilingTokens: -1})
			ctx := context.Background()
			id := ""
			for _, word := range []string{"alpha", "bravo", "charlie"} {
				result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: prompt(word)})
				if err != nil {
					t.Fatal(err)
				}
				id = result.SessionID
			}
			before := len(fake.Requests())
			result, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "delta [quirk:overflow-midstream]"})
			if !gotato.IsCode(err, gotato.CodeContextOverflow) || result.Result.Status != gotato.RunStatusFailed {
				t.Fatalf("delta: %+v, %v", result, err)
			}
			if requests := fake.Requests()[before:]; len(requests) != 1 || requests[0].Outcome != "overflow-midstream" {
				t.Fatalf("requests of the Run: %s", describe(requests))
			}
			events, err := runner.Events(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			cut := 0
			for _, event := range events {
				if _, ok := event.(gotato.ReplyCutOff); ok {
					cut++
				}
			}
			if cut != 1 {
				t.Errorf("the text received before the overflow is not kept as a cut-off reply (%d reply_cut_off Events)", cut)
			}
			if got := compactions(t, runner, id); len(got) != 0 {
				t.Errorf("compactions = %v", got)
			}
		})
	}
}

func TestRunnerKeysEachSessionsPromptCache(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"), testkit.Text("c"))
	runner, _ := newRunner(t, service.Agent{Name: "echo", Model: model})
	ctx := context.Background()
	first, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	other, err := runner.Run(ctx, service.RunRequest{Prompt: "three"})
	if err != nil {
		t.Fatal(err)
	}
	requests := model.Requests()
	if len(requests) != 3 {
		t.Fatalf("model calls = %d, want 3", len(requests))
	}
	// One stable key per Session, and never the raw Session ID.
	want := service.CacheKey(first.SessionID)
	if requests[0].CacheKey != want || requests[1].CacheKey != want ||
		requests[2].CacheKey != service.CacheKey(other.SessionID) || requests[2].CacheKey == want ||
		strings.Contains(want, first.SessionID) {
		t.Fatalf("cache keys = %q, %q, %q; want %q twice, then another", requests[0].CacheKey, requests[1].CacheKey, requests[2].CacheKey, want)
	}
}

func TestForkKeepsTheParentsCacheKey(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("a"), testkit.Text("b"), testkit.Text("c"))
	runner, store := newRunner(t, service.Agent{Name: "echo", Model: model})
	ctx := context.Background()
	parent, err := runner.Run(ctx, service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := runner.Fork(ctx, parent.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := runner.Fork(ctx, child.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{child.ID(), grandchild.ID()} {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "branch"}); err != nil {
			t.Fatal(err)
		}
	}
	// A fork starts with its parent's history, so it reads the parent's cached
	// prefix: every generation carries the first parent's key.
	want := service.CacheKey(parent.SessionID)
	for i, request := range model.Requests() {
		if request.CacheKey != want {
			t.Fatalf("request %d cache key = %q, want the parent's %q", i, request.CacheKey, want)
		}
	}
	stored, err := session.View(ctx, store, grandchild.ID())
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := stored.Get(service.MetaCacheKey); key != want {
		t.Fatalf("stored %s = %q, want %q", service.MetaCacheKey, key, want)
	}
}

// deadlineProbe is a PreToolUse Extension that records the deadline of the
// Run's context when the Run reaches its first Tool call.
type deadlineProbe struct {
	mu       sync.Mutex
	deadline time.Time
	has      bool
}

func (p *deadlineProbe) Before(ctx context.Context, _ gotato.ToolUse) (gotato.PreToolDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deadline, p.has = ctx.Deadline()
	return gotato.PreToolDecision{}, nil
}

// runDeadline runs one Run of an Agent with the given RunTimeout under the
// caller's ctx and request, and returns how far ahead of now the Run's
// context deadline was when the Run reached its first Tool call.
func runDeadline(t *testing.T, ctx context.Context, runTimeout, requestTimeout time.Duration) (time.Duration, bool) {
	t.Helper()
	probe := &deadlineProbe{}
	runner, _ := newRunner(t, service.Agent{
		Name:       "probe",
		Model:      testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: "lookup", Arguments: []byte(`{}`)}), testkit.Text("done")),
		Tools:      []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
		Options:    []gotato.SessionOption{gotato.WithExtension(probe)},
		RunTimeout: runTimeout,
	})
	start := time.Now()
	result, err := runner.Run(ctx, service.RunRequest{Prompt: "go", Timeout: requestTimeout})
	if err != nil || result.Result.Status != gotato.RunStatusCompleted {
		t.Fatalf("Run = %+v, %v", result.Result, err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.deadline.Sub(start), probe.has
}

// A Run of an Agent with no RunTimeout has a deadline of 30 minutes.
func TestARunHasADefaultDeadlineOf30Minutes(t *testing.T) {
	ahead, has := runDeadline(t, context.Background(), 0, 0)
	if !has || ahead > 31*time.Minute || ahead < 29*time.Minute {
		t.Fatalf("deadline %v ahead (set %v); want about 30 minutes", ahead, has)
	}
}

// The Agent's RunTimeout replaces the default, the request's Timeout replaces
// the Agent's, and a negative RunTimeout leaves the Run without a deadline.
func TestTheAgentsRunTimeoutAndTheRequestsTimeoutOverrideTheDefault(t *testing.T) {
	ahead, has := runDeadline(t, context.Background(), time.Hour, 0)
	if !has || ahead < 59*time.Minute || ahead > time.Hour+time.Minute {
		t.Fatalf("Agent RunTimeout: deadline %v ahead (set %v); want about 1 hour", ahead, has)
	}
	ahead, has = runDeadline(t, context.Background(), time.Hour, 5*time.Minute)
	if !has || ahead < 4*time.Minute || ahead > 6*time.Minute {
		t.Fatalf("request Timeout: deadline %v ahead (set %v); want about 5 minutes", ahead, has)
	}
	if _, has = runDeadline(t, context.Background(), -1, 0); has {
		t.Fatal("a negative RunTimeout still set a deadline")
	}
}

// A deadline on the caller's context that comes sooner wins over the default.
func TestACallersShorterDeadlineOverridesTheDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ahead, has := runDeadline(t, ctx, 0, 0)
	if !has || ahead > 2*time.Minute+time.Second {
		t.Fatalf("deadline %v ahead (set %v); want the caller's 2 minutes", ahead, has)
	}
}

// A Run that outlasts its deadline settles as deadline_exceeded.
func TestARunPastItsRunTimeoutSettlesAsDeadlineExceeded(t *testing.T) {
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	runner, _ := newRunner(t, service.Agent{Name: "slow", Model: model, RunTimeout: 50 * time.Millisecond})
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "go"})
	if err == nil || result.Result.Status != gotato.RunStatusDeadlineExceeded {
		t.Fatalf("Run = %+v, %v; want deadline_exceeded", result.Result, err)
	}
}

// A negative Timeout is refused: it must not remove the Agent's RunTimeout,
// which an expired remaining time computed by the caller would do.
func TestANegativeRequestTimeoutIsRefused(t *testing.T) {
	runner, _ := newRunner(t, service.Agent{Name: "a", Model: testkit.EchoModel{}, RunTimeout: time.Minute})
	_, err := runner.Run(context.Background(), service.RunRequest{Prompt: "go", Timeout: -time.Second})
	var re *gotato.RuntimeError
	if !errors.As(err, &re) || re.Code != gotato.CodeInvalidArgument || !re.NotRun {
		t.Fatalf("Run with a negative Timeout = %v, want invalid_argument and not run", err)
	}
}

// peekingModel calls a tool on its first call, and on its second reads the
// store to see what the Run has written so far.
type peekingModel struct {
	store session.Store
	mu    sync.Mutex
	id    string
	calls int
	seen  []gotato.Message
}

func (m *peekingModel) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	m.calls++
	call, id := m.calls, m.id
	m.mu.Unlock()
	if call == 1 {
		return testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: "lookup", Arguments: []byte(`{}`)})).Stream(ctx, gotato.ModelRequest{})
	}
	stored, err := session.Open(ctx, m.store, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.seen = stored.Messages()
	m.mu.Unlock()
	return testkit.NewFakeModel(testkit.Text("done")).Stream(ctx, gotato.ModelRequest{})
}

func TestEveryStepIsStoredBeforeTheNext(t *testing.T) {
	store := session.NewMemoryStore()
	model := &peekingModel{store: store}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{
		Name: "lookup", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "lookup", nil)
	if err != nil {
		t.Fatal(err)
	}
	model.id = s.ID()
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "find it"}); err != nil {
		t.Fatal(err)
	}
	// Before the second model call, the store held the prompt, the reply
	// that called the tool, and the tool's result.
	if len(model.seen) != 3 || model.seen[2].ToolResult == nil || model.seen[2].ToolResult.CallID != "call-1" || gotato.TextOf(model.seen[2]) != "found" {
		t.Fatalf("the store held %+v before the second model call", model.seen)
	}
}

// crashingStore fails every write, and every renewal of the instance's
// Liveness row, once a write has held a tool_started Entry, as the
// instance's death right after a tool starts would.
type crashingStore struct {
	session.Store
	mu      sync.Mutex
	crashed bool
}

func (s *crashingStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	s.mu.Lock()
	crashed := s.crashed
	s.mu.Unlock()
	if crashed {
		return context.Canceled
	}
	return s.Store.Beat(ctx, row, ttl)
}

func (s *crashingStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	s.mu.Lock()
	crashed := s.crashed
	for _, entry := range entries {
		if entry.Kind == gotato.EventKindToolStarted {
			s.crashed = true
		}
	}
	s.mu.Unlock()
	if crashed {
		return context.Canceled
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func TestACrashDuringAToolIsAnsweredOnTheNextRun(t *testing.T) {
	backing := session.NewMemoryStore()
	agent := func(model gotato.Model) service.Agent {
		return service.Agent{Name: "lookup", Model: model, Tools: []gotato.Tool{testkit.NewFakeTool("lookup", "found")}}
	}
	// A short TTL lets the crashed instance's row expire quickly. The instance
	// that takes over is alive, so its TTL is long.
	crashLiveness := service.Liveness{TTL: 100 * time.Millisecond, Wait: 2 * time.Second}
	crashing, err := service.New(service.Config{Store: &crashingStore{Store: backing}, Liveness: crashLiveness, Agents: []service.Agent{
		agent(testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: "lookup", Arguments: []byte(`{}`)}))),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := crashing.CreateSession(ctx, "lookup", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crashing.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "find it"}); err == nil {
		t.Fatal("the Run succeeded on a store that stopped writing")
	}

	// Another instance takes the Session over.
	model := testkit.NewFakeModel(testkit.Text("sorry, let me retry"))
	next, err := service.New(service.Config{Store: backing, Liveness: service.Liveness{TTL: steadyLiveness.TTL, Wait: 2 * time.Second}, Agents: []service.Agent{agent(model)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "did it work?"}); err != nil {
		t.Fatal(err)
	}
	request, _ := model.LastRequest()
	var answer *gotato.ToolResult
	for _, message := range request.Messages {
		if message.ToolResult != nil && message.ToolResult.CallID == "call-1" {
			answer = message.ToolResult
		}
	}
	if answer == nil || answer.SafeError != session.StoppedToolResult {
		t.Fatalf("the next request answered call-1 with %+v, want the stopped answer", answer)
	}
	view, err := next.Record(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	runs := view.Runs()
	if len(runs) != 2 || runs[0].Error != "interrupted" || runs[1].Status != gotato.RunStatusCompleted {
		t.Fatalf("runs = %+v, want the crashed Run ended as interrupted", runs)
	}
}

// A compacted Session that goes on and is loaded by another instance builds
// the same Context as before the reload (ROADMAP Phase 4).
func TestCompactAppendReloadKeepsTheContext(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	first, err := service.New(service.Config{Store: store, Instance: "instance-a", Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := first.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"one", "two", "three"} {
		if _, err := first.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt}); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := first.Compact(ctx, s.ID(), modelctx.CompactConfig{KeepMessages: 2}); err != nil || !result.Replaced {
		t.Fatalf("Compact = %+v, %v", result, err)
	}
	if _, err := first.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "four"}); err != nil {
		t.Fatal(err)
	}
	before, err := first.Inspect(ctx, s.ID(), service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close(ctx)

	second, err := service.New(service.Config{Store: store, Instance: "instance-b", Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := second.Inspect(ctx, s.ID(), service.InspectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if after.PrefixHash != before.PrefixHash || after.SelectedMessages != before.SelectedMessages {
		t.Fatalf("after the reload: prefix %s with %d messages, before %s with %d", after.PrefixHash, after.SelectedMessages, before.PrefixHash, before.SelectedMessages)
	}
	if before.SelectedMessages != 8 {
		t.Fatalf("the compacted Session holds %d messages, want the Segment's four, the kept two, and the last Run's two", before.SelectedMessages)
	}
}
