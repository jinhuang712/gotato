package gotato

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// awaitArrivals blocks until the named number of Tools are inside their
// executors at the same time. It is the deterministic proof that a batch ran
// in parallel: with one worker it never completes.
func awaitArrivals(t *testing.T, arrive chan string, count int) {
	t.Helper()
	for i := range count {
		select {
		case <-arrive:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d Tools were running at once", i, count)
		}
	}
}

func TestFailedToolSetResolutionDoesNotFailNewSession(t *testing.T) {
	set := &stubToolSet{name: "files", err: errors.New("catalog is down")}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithToolSet(set))
	if err != nil {
		t.Fatalf("a ToolSet that does not resolve failed NewSession: %v", err)
	}
	_ = agent.Close(context.Background())
}

func TestToolSetConstructionValidation(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}
	if _, err := NewSession(context.Background(), WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}}}),
		WithActiveToolSet(&stubToolSet{name: "files", tools: []Tool{&namedTool{id: "write"}}}),
	); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("duplicate ToolSet name = %v", err)
	}
	if _, err := NewSession(context.Background(), WithModel(model),
		WithActiveToolSet(&stubToolSet{name: "files.deep", tools: []Tool{&namedTool{id: "read"}}}),
	); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("dotted ToolSet name = %v", err)
	}
	// A ToolSet that returns a duplicate local Tool name is unavailable.
	dup := &stubToolSet{name: "files", tools: []Tool{&namedTool{id: "read"}, &namedTool{id: "read"}}}
	if agent, err := NewSession(context.Background(), WithModel(model), WithActiveToolSet(dup)); err != nil {
		t.Fatalf("duplicate local Tool name = %v", err)
	} else {
		if agent.registry.sets[0].failure == nil {
			t.Fatal("a ToolSet with a duplicate local Tool name is not marked unavailable")
		}
		_ = agent.Close(context.Background())
	}
	if _, err := NewSession(context.Background(), WithModel(model), WithToolSet(nil)); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("nil ToolSet = %v", err)
	}
}

func parallelCallScript(ids ...string) []ModelEvent {
	events := make([]ModelEvent, 0, len(ids)+1)
	for i, id := range ids {
		events = append(events, ModelEvent{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{
			ID:        ToolCallID("call-" + id),
			ToolID:    id,
			Arguments: []byte(`{}`),
		}})
		_ = i
	}
	return append(events, ModelEvent{Kind: ModelEventKindDone, StopReason: StopReasonToolCalls})
}

func TestParallelToolsCommitInSourceOrder(t *testing.T) {
	tracker := &concurrencyTracker{}
	arrive := make(chan string, 2)
	fastRelease := make(chan struct{})
	slowRelease := make(chan struct{})
	first := &namedTool{id: "slow", arrive: arrive, hold: slowRelease, tracker: tracker}
	second := &namedTool{id: "fast", arrive: arrive, hold: fastRelease, tracker: tracker}
	limits := defaultLimits()
	limits.MaxParallelTools = 2
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("slow", "fast"), finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(first, second), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	events, err := agent.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = events.Close() }()

	// The Events of a step are written together, so a ToolEnded is sent only
	// after the whole group has settled. To keep the completion order
	// deterministic, "slow" is released only after "fast" has left its
	// executor and had time to report; the stored ToolEnded Events must follow
	// that completion order, while the Tool Results commit in source order.
	var mu sync.Mutex
	var ends, commits []ToolCallID
	observed := make(chan error, 1)
	go func() {
		for {
			event, nextErr := events.Next(context.Background())
			if nextErr != nil {
				observed <- nextErr
				return
			}
			mu.Lock()
			switch e := event.(type) {
			case ToolEnded:
				ends = append(ends, e.CallID)
			case ToolResultCommitted:
				commits = append(commits, e.CallID)
			}
			mu.Unlock()
			if event.Kind() == EventKindRunEnded {
				observed <- nil
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("run both"))
		done <- runErr
	}()
	awaitArrivals(t, arrive, 2)
	close(fastRelease)
	deadline := time.Now().Add(10 * time.Second)
	for tracker.activeValue() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the released Tool did not finish while the other was held")
		}
		time.Sleep(time.Millisecond)
	}
	// No Event shows the fast Tool's outcome before the whole group settles,
	// so there is nothing to wait on: the Session goroutine stages it from a
	// channel the worker has already sent on, and this margin covers that hop.
	time.Sleep(50 * time.Millisecond)
	close(slowRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parallel batch did not settle")
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Event stream did not reach run_ended")
	}
	if tracker.peakValue() != 2 {
		t.Fatalf("parallel peak = %d", tracker.peakValue())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ends) != 2 || ends[0] != "call-fast" {
		t.Fatalf("completion Events did not follow completion order: %v", ends)
	}
	if len(commits) != 2 || commits[0] != "call-slow" || commits[1] != "call-fast" {
		t.Fatalf("commitment did not follow source order: %v", commits)
	}
}

func TestSequentialToolRunsAlone(t *testing.T) {
	tracker := &concurrencyTracker{}
	arrive := make(chan string, 3)
	aRelease := make(chan struct{})
	guardRelease := make(chan struct{})
	bRelease := make(chan struct{})
	limits := defaultLimits()
	limits.MaxParallelTools = 4
	tools := []Tool{
		&namedTool{id: "a", arrive: arrive, hold: aRelease, tracker: tracker},
		&namedTool{id: "guard", arrive: arrive, hold: guardRelease, tracker: tracker, seq: true},
		&namedTool{id: "b", arrive: arrive, hold: bRelease, tracker: tracker},
	}
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("a", "guard", "b"), finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tools...), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()

	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("run"))
		done <- runErr
	}()

	// The Sequential guard splits the batch into single-Tool groups. Each Tool
	// must therefore be observed in its executor alone: a second arrival while
	// one is held would mean the guard ran alongside another Tool.
	expectArrival := func(want string) {
		t.Helper()
		select {
		case got := <-arrive:
			if got != want {
				t.Fatalf("Tool arrival = %q, want %q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Tool %q did not start", want)
		}
	}
	assertAlone := func() {
		t.Helper()
		select {
		case got := <-arrive:
			t.Fatalf("Tool %q started while another was still running", got)
		case <-time.After(100 * time.Millisecond):
		}
	}

	expectArrival("a")
	assertAlone()
	close(aRelease)
	expectArrival("guard")
	assertAlone()
	close(guardRelease)
	expectArrival("b")
	close(bRelease)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sequential batch did not settle")
	}
	if tracker.peakValue() != 1 {
		t.Fatalf("a Sequential Tool ran alongside others: peak = %d", tracker.peakValue())
	}
}

type panicSpecToolSet struct{}

func (panicSpecToolSet) Spec() ToolSetSpec { panic("broken Spec") }

func (panicSpecToolSet) Tools(context.Context) ([]Tool, error) { return nil, nil }

func TestToolSetSpecPanicBecomesRuntimeError(t *testing.T) {
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("x")}}
	_, err := NewSession(context.Background(), WithModel(model), WithActiveToolSet(panicSpecToolSet{}))
	if err == nil {
		t.Fatal("a panicking ToolSet.Spec did not fail NewSession")
	}
	if !IsCode(err, CodeExtensionFailure) {
		t.Fatalf("ToolSet.Spec panic = %v, want extension_failure", err)
	}
}

func TestParallelWorkerBoundIsRespected(t *testing.T) {
	tracker := &concurrencyTracker{}
	arrive := make(chan string, 4)
	release := make(chan struct{})
	limits := defaultLimits()
	limits.MaxParallelTools = 2
	tools := []Tool{
		&namedTool{id: "one", arrive: arrive, hold: release, tracker: tracker},
		&namedTool{id: "two", arrive: arrive, hold: release, tracker: tracker},
		&namedTool{id: "three", arrive: arrive, hold: release, tracker: tracker},
		&namedTool{id: "four", arrive: arrive, hold: release, tracker: tracker},
	}
	model := &recordingModel{scripts: [][]ModelEvent{parallelCallScript("one", "two", "three", "four"), finalScript("done")}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(tools...), WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, runErr := agent.Prompt(context.Background(), UserMessage("run"))
		done <- runErr
	}()

	// Exactly two Tools may be inside their executors; a third must wait for
	// the first group to settle.
	awaitArrivals(t, arrive, 2)
	select {
	case id := <-arrive:
		t.Fatalf("Tool %q started while the worker bound was already full", id)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	awaitArrivals(t, arrive, 2)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bounded batch did not settle")
	}
	if tracker.peakValue() != 2 {
		t.Fatalf("worker bound not exercised: peak = %d", tracker.peakValue())
	}
}

// blockingToolSet resolves only when its context ends.
type blockingToolSet struct{ entered chan struct{} }

func (blockingToolSet) Spec() ToolSetSpec { return ToolSetSpec{Name: "slow"} }

func (b blockingToolSet) Tools(ctx context.Context) ([]Tool, error) {
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNewSessionCancelsAResolutionInProgress(t *testing.T) {
	set := blockingToolSet{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewSession(ctx, WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}}),
			WithToolSet(set))
		done <- err
	}()
	select {
	case <-set.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the resolution never reached the ToolSet")
	}
	cancel()
	select {
	case err := <-done:
		if !IsCode(err, CodeCanceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NewSession did not return after the context ended")
	}
}

func TestNewSessionRefusesAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewSession(ctx, WithModel(&recordingModel{scripts: [][]ModelEvent{finalScript("x")}})); !IsCode(err, CodeCanceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

// barrier opens when every ToolSet of a group has entered Tools, so the group
// answers only if the ToolSets resolve at the same time. A sequential
// resolution would wait here until the timeout and fail the test. The test
// counts arrivals, not milliseconds, so it is not a timing test.
type barrier struct {
	mu      sync.Mutex
	want    int
	entered int
	open    chan struct{}
	armed   bool
}

func newBarrier(want int) *barrier { return &barrier{want: want, open: make(chan struct{})} }

// arm makes the next resolution wait for the whole group.
func (b *barrier) arm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entered, b.open, b.armed = 0, make(chan struct{}), true
}

func (b *barrier) disarm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armed = false
}

func (b *barrier) arrive() error {
	b.mu.Lock()
	if !b.armed {
		b.mu.Unlock()
		return nil
	}
	b.entered++
	open := b.open
	if b.entered == b.want {
		close(open)
	}
	b.mu.Unlock()
	select {
	case <-open:
		return nil
	case <-time.After(20 * time.Second):
		return errors.New("ToolSets did not resolve at the same time")
	}
}

type barrierSet struct {
	name    string
	barrier *barrier
	panics  bool
}

func (s *barrierSet) Spec() ToolSetSpec {
	return ToolSetSpec{Name: s.name, Description: s.name + " tools"}
}

func (s *barrierSet) Tools(context.Context) ([]Tool, error) {
	if err := s.barrier.arrive(); err != nil {
		return nil, err
	}
	if s.panics {
		panic("boom")
	}
	return []Tool{&docTool{id: "act", description: s.name + " action"}}, nil
}

func barrierSets(n int, b *barrier) []*barrierSet {
	var sets []*barrierSet
	for i := range n {
		sets = append(sets, &barrierSet{name: fmt.Sprintf("srv%02d", i), barrier: b})
	}
	return sets
}

func withSets(sets []*barrierSet) []SessionOption {
	var opts []SessionOption
	for _, set := range sets {
		opts = append(opts, WithToolSet(set))
	}
	return opts
}

func TestToolSetsResolveAtTheSameTimeAtNewSessionAndAtEveryRun(t *testing.T) {
	const n = 12
	b := newBarrier(n)
	b.arm()
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("a"), finalScript("b")}}
	agent := newSearchSession(t, model, withSets(barrierSets(n, b))...)
	if got := unavailableStages(runEvents(t, agent, "one")); len(got) != 0 {
		t.Fatalf("the first Run found unavailable ToolSets: %v", got)
	}
	b.arm()
	if got := unavailableStages(runEvents(t, agent, "two")); len(got) != 0 {
		t.Fatalf("the second Run found unavailable ToolSets: %v", got)
	}
}

func TestParallelResolutionKeepsTheCatalogBytesTheSame(t *testing.T) {
	const n, runs = 8, 4
	b := newBarrier(n)
	var scripts [][]ModelEvent
	for range runs {
		scripts = append(scripts, finalScript("ok"))
	}
	model := &recordingModel{scripts: scripts}
	agent := newSearchSession(t, model, withSets(barrierSets(n, b))...)
	for i := range runs {
		b.arm()
		runEvents(t, agent, fmt.Sprintf("run %d", i))
	}
	want := descriptionOf(model, 0, SearchToolName)
	last := -1
	for i := range n {
		at := strings.Index(want, fmt.Sprintf("srv%02d — ", i))
		if at <= last {
			t.Fatalf("srv%02d is missing or out of order in the catalog:\n%s", i, want)
		}
		last = at
	}
	for i := 1; i < runs; i++ {
		if got := descriptionOf(model, i, SearchToolName); got != want {
			t.Fatalf("Run %d changed the catalog:\n%s\n---\n%s", i, want, got)
		}
	}
}

func TestAPanicInOneToolSetLeavesTheOthersResolved(t *testing.T) {
	b := newBarrier(3)
	b.arm()
	sets := barrierSets(3, b)
	sets[1].panics = true
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("a")}}
	agent := newSearchSession(t, model, withSets(sets)...)
	b.disarm()
	if got := unavailableStages(runEvents(t, agent, "one")); !slices.Equal(got, []string{"toolset:srv01"}) {
		t.Fatalf("warnings = %v, want only srv01", got)
	}
	description := descriptionOf(model, 0, SearchToolName)
	if !strings.Contains(description, "srv00.act") || !strings.Contains(description, "srv02.act") {
		t.Fatalf("the other ToolSets are missing:\n%s", description)
	}
	if !strings.Contains(description, "srv01 — srv01 tools (unavailable now; no tools)") {
		t.Fatalf("srv01 is not marked unavailable:\n%s", description)
	}
}

// holdSet blocks in Tools until its context ends, once armed.
type holdSet struct {
	name    string
	armed   *atomic.Bool
	entered chan struct{}
}

func (s *holdSet) Spec() ToolSetSpec { return ToolSetSpec{Name: s.name} }

func (s *holdSet) Tools(ctx context.Context) ([]Tool, error) {
	if !s.armed.Load() {
		return nil, nil
	}
	s.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestACanceledRunStopsAParallelResolution(t *testing.T) {
	const n = 4
	var armed atomic.Bool
	entered := make(chan struct{}, n)
	var opts []SessionOption
	for i := range n {
		opts = append(opts, WithToolSet(&holdSet{name: fmt.Sprintf("hold%d", i), armed: &armed, entered: entered}))
	}
	model := &recordingModel{scripts: [][]ModelEvent{finalScript("a"), finalScript("b")}}
	agent := newSearchSession(t, model, opts...)
	// The first Run uses NewSession's resolution; the second resolves again.
	runEvents(t, agent, "one")
	armed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(ctx, UserMessage("two"))
		done <- err
	}()
	for range n {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the ToolSets did not all start resolving")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a canceled resolution did not fail the Run")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the Run did not return after cancel")
	}
}

// panicSpecTool is a Tool whose Spec panics.
type panicSpecTool struct{}

func (panicSpecTool) Spec() ToolSpec { panic("broken Tool Spec") }

func (panicSpecTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{}, nil
}

// A ToolSet that returns a bad Tool, a typed nil or one whose Spec panics, is
// unavailable as a whole; the process and the Session go on.
func TestABadToolMakesItsToolSetUnavailable(t *testing.T) {
	for name, bad := range map[string]Tool{"typed nil": (*docTool)(nil), "Spec panics": panicSpecTool{}} {
		t.Run(name, func(t *testing.T) {
			set := &stubToolSet{name: "files", tools: []Tool{&docTool{id: "read"}, bad}}
			model := &recordingModel{scripts: [][]ModelEvent{finalScript("done")}}
			agent, err := NewSession(context.Background(), WithModel(model), WithToolSet(set), WithTools(&docTool{id: "root"}))
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			defer func() { _ = agent.Close(context.Background()) }()
			if got := unavailableStages(runEvents(t, agent, "go")); !slices.Equal(got, []string{"toolset:files"}) {
				t.Fatalf("warnings = %v, want toolset:files", got)
			}
			if status := agent.ToolSets(); len(status) != 1 || !status[0].Unavailable || len(status[0].Tools) != 0 {
				t.Fatalf("ToolSets = %+v, want files unavailable with no Tools", status)
			}
		})
	}
}

// specCountingTool counts how often its Spec is read.
type specCountingTool struct {
	id    string
	reads atomic.Int32
}

func (t *specCountingTool) Spec() ToolSpec {
	t.reads.Add(1)
	return ToolSpec{ID: t.id, Effect: ToolEffectRead}
}

func (t *specCountingTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{}, nil
}

// A Tool's Spec is read once, when the Tool is resolved: a root Tool at
// NewSession, a ToolSet's Tool at each resolution.
func TestToolSpecIsReadWhenTheToolIsResolved(t *testing.T) {
	root := &specCountingTool{id: "root"}
	listed := &specCountingTool{id: "read"}
	set := &stubToolSet{name: "files", tools: []Tool{listed}}
	model := &recordingModel{scripts: [][]ModelEvent{
		callTool("c1", "root"), finalScript("one"),
		searchCall("c2", map[string]any{"names": []string{"files.read"}}), finalScript("two"),
	}}
	agent, err := NewSession(context.Background(), WithModel(model), WithTools(root), WithToolSet(set))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	for _, text := range []string{"one", "two"} {
		if _, err := agent.Prompt(context.Background(), UserMessage(text)); err != nil {
			t.Fatal(err)
		}
	}
	if got := root.reads.Load(); got != 1 {
		t.Fatalf("root Spec read %d times, want once", got)
	}
	// NewSession resolves the ToolSet for the first Run, the second Run
	// resolves it again.
	if got := listed.reads.Load(); got != 2 {
		t.Fatalf("ToolSet Tool Spec read %d times, want 2", got)
	}
}

// A root Tool whose Spec panics fails NewSession with a code, not the process.
func TestARootToolWhoseSpecPanicsFailsNewSession(t *testing.T) {
	_, err := NewSession(context.Background(), WithModel(&testModel{}), WithTools(panicSpecTool{}))
	if !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

// aliasTool is a Tool whose ID and Name differ.
type aliasTool struct{ id, name string }

func (t aliasTool) Spec() ToolSpec { return ToolSpec{ID: t.id, Name: t.name, Effect: ToolEffectRead} }

func (t aliasTool) Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error) {
	return ToolResult{Content: []ContentPart{{Kind: ContentKindText, Text: "ran " + t.id}}}, nil
}

// A root Tool cannot take a name of the catalog: its ID or Name under an
// installed ToolSet's namespace, or search_tools. NewSession refuses it.
func TestARootToolCannotTakeANameOfTheCatalog(t *testing.T) {
	for name, root := range map[string]Tool{
		"ID in a namespace":   aliasTool{id: "files.read"},
		"Name in a namespace": aliasTool{id: "local_read", name: "files.read"},
		"ID search_tools":     aliasTool{id: SearchToolName},
		"Name search_tools":   aliasTool{id: "find", name: SearchToolName},
	} {
		t.Run(name, func(t *testing.T) {
			set := &stubToolSet{name: "files", tools: []Tool{&docTool{id: "read"}}}
			_, err := NewSession(context.Background(), WithModel(&testModel{}), WithTools(root), WithToolSet(set))
			if !IsCode(err, CodeInvalidArgument) {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
	// A dotted ID outside every namespace is a name of your own.
	set := &stubToolSet{name: "files", tools: []Tool{&docTool{id: "read"}}}
	agent, err := NewSession(context.Background(), WithModel(&testModel{}), WithTools(aliasTool{id: "payments.get_batch"}), WithToolSet(set))
	if err != nil {
		t.Fatalf("a dotted ID of your own: %v", err)
	}
	_ = agent.Close(context.Background())
}

// A ToolSource that supplies a name of the catalog keeps its last set, and
// the Runs go on: an active ToolSet that grows the same name later does not
// collide with it.
func TestAToolSourceCannotTakeANameOfTheCatalog(t *testing.T) {
	for name, taken := range map[string]Tool{
		"namespace":    aliasTool{id: "files.read"},
		"search_tools": aliasTool{id: SearchToolName},
	} {
		t.Run(name, func(t *testing.T) {
			active := &flakySet{name: "files", tools: []Tool{&docTool{id: "write"}}}
			listed := &flakySet{name: "docs", tools: []Tool{&docTool{id: "find"}}}
			source := &dynamicSource{}
			model := &recordingModel{scripts: [][]ModelEvent{finalScript("one"), finalScript("two"), finalScript("three")}}
			agent, err := NewSession(context.Background(), WithModel(model), WithToolSource(source), WithActiveToolSet(active), WithToolSet(listed))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = agent.Close(context.Background()) }()
			source.set(taken)
			var stages []string
			for _, event := range runEvents(t, agent, "one") {
				if e, ok := event.(ExtensionFailed); ok {
					stages = append(stages, e.Stage+"/"+string(e.Code))
				}
			}
			if !slices.Equal(stages, []string{"toolsource/invalid_argument"}) {
				t.Fatalf("warnings = %v, want the ToolSource refused", stages)
			}
			active.set([]Tool{&docTool{id: "write"}, &docTool{id: "read"}}, nil)
			for _, text := range []string{"two", "three"} {
				if _, err := agent.Prompt(context.Background(), UserMessage(text)); err != nil {
					t.Fatalf("Run %q: %v", text, err)
				}
			}
			ids := specIDs(agent.Tools())
			if !slices.Equal(ids, []string{"files.read", "files.write", SearchToolName}) {
				t.Fatalf("Tools = %v", ids)
			}
		})
	}
}

func specIDs(specs []ToolSpec) []string {
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	return ids
}
