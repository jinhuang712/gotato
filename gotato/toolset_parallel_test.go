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

func withSets(sets []*barrierSet) []Option {
	var opts []Option
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
	var opts []Option
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
