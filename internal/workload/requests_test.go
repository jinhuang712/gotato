package workload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// This file checks two goals of GOALS.md on the reference workload, from the
// requests a model receives:
//
//   - G-G09, the same Record gives the same request: TestSameRecordSameRequest.
//   - G-G07, no unplanned change to the prefix: TestRequestOnlyGrowsAtItsEnd.
//
// A "request" is the JSON of the gotato.ModelRequest the model got, the
// value every provider encoder starts from. It holds the system instruction,
// the Messages as they go to the model (Message IDs, usage and the
// Executed flag are runtime fields and are not in it), the Tool specs, the
// options, the cache breakpoints and the cache key.

// requestLog keeps, in order, the bytes of every ModelRequest a model received.
type requestLog struct {
	mu   sync.Mutex
	sent [][]byte
}

func (l *requestLog) add(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, b)
}

func (l *requestLog) all() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte(nil), l.sent...)
}

// loggingModel passes every request to inner, and keeps its JSON first.
type loggingModel struct {
	inner gotato.Model
	log   *requestLog
}

func (m loggingModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	b, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	m.log.add(b)
	return m.inner.Stream(ctx, request)
}

// reasoningModel makes every reply of inner carry reasoning, with an artifact
// as a provider would sign it, so a Session holds reasoning in every Round.
type reasoningModel struct{ inner gotato.Model }

func (m reasoningModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	stream, err := m.inner.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	events := testkit.Script{
		{Kind: gotato.ModelEventKindReasoningDelta, Text: fmt.Sprintf("thinking about %d messages", len(request.Messages))},
		{Kind: gotato.ModelEventKindReasoningDone, ReasoningArtifact: fmt.Appendf(nil, "signature-%d", len(request.Messages))},
	}
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			break
		}
		events = append(events, event)
	}
	return testkit.NewReplayModel(events).Stream(ctx, request)
}

// playOptions change how playAcross plays a task.
type playOptions struct {
	// reasoning makes every reply carry reasoning.
	reasoning bool
	// compactAfter, when above 0, compacts the Session explicitly after that
	// many Runs, keeping the last two Messages (modelctx.Compact).
	compactAfter int
}

// extraPrompts are added to a task with fewer Runs than the schedule has
// stages, so that every task is played as several Runs. The fake model
// answers all three from the history.
var extraPrompts = []string{"Thanks. And where is order 17?", "Which order numbers did I ask about?", "Thank you."}

// playAcross plays a task as several Runs on one Session, moving between
// Runs: Run 1 stays on instance a, Run 2 reloads on a new Runner of
// instance a, Run 3 is on instance b, Run 4 is back on a new Runner of
// instance a, and the cycle repeats. A Runner is closed before the next
// takes over. It returns the Session's ID and every request the model got,
// in order, over all Runners.
func playAcross(t *testing.T, store session.Store, task reftasks.Task, options playOptions) (string, [][]byte) {
	t.Helper()
	ctx := context.Background()
	log := &requestLog{}
	logged := task
	logged.Model = loggingModel{inner: task.Model, log: log}
	if options.reasoning {
		logged.Model = loggingModel{inner: reasoningModel{inner: task.Model}, log: log}
	}
	prompts := slices.Clone(task.Prompts)
	for len(prompts) < 4 {
		prompts = append(prompts, extraPrompts[len(prompts)-len(task.Prompts)])
	}
	stages := []string{"a", "a", "b", "a"}
	runner := taskRunner(t, store, logged, "a")
	s, err := runner.CreateSession(ctx, task.Name, nil, session.WithMessages(task.History...))
	if err != nil {
		t.Fatal(err)
	}
	for i, prompt := range prompts {
		if i > 0 {
			if i%len(stages) != 0 {
				// A new Runner is a reload, on instance a or b.
				if err := runner.Close(ctx); err != nil {
					t.Fatal(err)
				}
				runner = taskRunner(t, store, logged, stages[i%len(stages)])
			}
		}
		result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt})
		if err != nil {
			t.Fatalf("%s: Run %d on %s: %v", task.Name, i+1, runner.Instance(), err)
		}
		if result.Result.Status != gotato.RunStatusCompleted || result.Compacted {
			t.Fatalf("%s: Run %d ended %s (compacted %t)", task.Name, i+1, result.Result.Status, result.Compacted)
		}
		if options.compactAfter == i+1 {
			if _, err := runner.Compact(ctx, s.ID(), modelctx.CompactOptions{Keep: 2}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s.ID(), log.all()
}

// decodeRequests reads the logged requests back into values.
func decodeRequests(t *testing.T, sent [][]byte) []gotato.ModelRequest {
	t.Helper()
	requests := make([]gotato.ModelRequest, len(sent))
	for i, b := range sent {
		if err := json.Unmarshal(b, &requests[i]); err != nil {
			t.Fatal(err)
		}
	}
	return requests
}

func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSameRecordSameRequest checks GOALS.md G-G09. Each reference task is
// played as several Runs on one Session, moving between instances and
// reloads. Then, for every request the model got in the play, the Record as
// it stood at that Turn (the stored Messages before the reply the request
// asked for) is written to a new Session, and three views build the request
// from it: instance a, instance b, and a new Runner of instance a (a reload).
// All three must give the bytes the model got in the play.
func TestSameRecordSameRequest(t *testing.T) {
	ctx := context.Background()
	for _, task := range reftasks.ReferenceTasks() {
		t.Run(task.Name, func(t *testing.T) {
			store := session.NewMemoryStore()
			id, sent := playAcross(t, store, task, playOptions{})
			final, err := session.Open(ctx, store, id)
			if err != nil {
				t.Fatal(err)
			}
			cacheKey, ok := final.Get(service.MetaCacheKey)
			if !ok || cacheKey == "" {
				cacheKey = service.CacheKey(id)
			}
			stored := final.Messages()
			if len(final.Compactions()) != 0 || len(final.Segments()) != 0 {
				t.Fatalf("%s compacted; the reference workload is played without compaction", task.Name)
			}

			viewA := taskRunner(t, store, task, "a")
			viewB := taskRunner(t, store, task, "b")
			reloaded := taskRunner(t, store, task, "a")
			views := []struct {
				name   string
				runner *service.Runner
			}{{"instance a", viewA}, {"instance b", viewB}, {"instance a after reload", reloaded}}

			identical := 0
			for turn, want := range sent {
				var request gotato.ModelRequest
				if err := json.Unmarshal(want, &request); err != nil {
					t.Fatal(err)
				}
				n := len(request.Messages)
				if n > len(stored) {
					t.Fatalf("Turn %d: request holds %d Messages, the Record %d", turn+1, n, len(stored))
				}
				// The Record as it stood at this Turn: a copy of its first n
				// Messages under the same cache key, which is the only part of
				// the Record's identity that reaches the request.
				prefix, err := viewA.CreateSession(ctx, task.Name, map[string]string{service.MetaCacheKey: cacheKey}, session.WithMessages(stored[:n]...), reftasks.WithLoadedToolsOf(stored[:n]))
				if err != nil {
					t.Fatal(err)
				}
				for _, view := range views {
					report, err := view.runner.Inspect(ctx, prefix.ID())
					if err != nil {
						t.Fatal(err)
					}
					if got := jsonOf(t, report.Request); !bytes.Equal(got, want) {
						t.Errorf("Turn %d on %s: request differs from the one the model got\n got: %s\nwant: %s", turn+1, view.name, got, want)
					}
				}
				identical++
			}
			t.Logf("%d Turns, each built by %d views", identical, len(views))
			if identical != len(sent) || identical == 0 {
				t.Fatalf("%d of %d Turns compared", identical, len(sent))
			}
		})
	}
}

// TestRequestOnlyGrowsAtItsEnd checks the part of GOALS.md G-G07 that spans
// Runs, reloads and instances. Each reference task is played as several Runs
// on one Session (see playAcross). Every request must start with the bytes of
// the request before it: the same system instruction, the same Tools, the same
// options and cache key, the first two cache breakpoints (system, tools) the
// same, and the Messages of the earlier request first, byte for byte. The pairs
// cross Run boundaries, reloads and the change of instance.
//
// The exceptions DESIGNS.md names do not occur in this play, and the test says
// why for each:
//   - A new Round leaving out the last Round's reasoning happens in the
//     gateway's encoder (gateway.providerRules.roundReasoning), after the
//     request is built. The ModelRequest carries every reasoning artifact, so
//     "with reasoning" plays tasks whose replies all carry reasoning and
//     requires the prefix to hold with it.
//   - A compaction and a merge of the oldest Segments: the play does not
//     compact (the Record holds no Compaction and no Segment), so no pair
//     may change.
func TestRequestOnlyGrowsAtItsEnd(t *testing.T) {
	ctx := context.Background()
	for _, withReasoning := range []bool{false, true} {
		name := "plain"
		if withReasoning {
			name = "with reasoning"
		}
		for _, task := range reftasks.ReferenceTasks() {
			t.Run(name+"/"+task.Name, func(t *testing.T) {
				store := session.NewMemoryStore()
				id, sent := playAcross(t, store, task, playOptions{reasoning: withReasoning})
				final, err := session.Open(ctx, store, id)
				if err != nil {
					t.Fatal(err)
				}
				if len(final.Compactions()) != 0 || len(final.Segments()) != 0 {
					t.Fatalf("%s compacted, so a pair may change; the test allows no exception", task.Name)
				}
				if withReasoning {
					reasoned := 0
					for _, m := range final.Messages() {
						if m.Role == gotato.RoleAssistant && gotato.ReasoningOf(m) != "" {
							reasoned++
						}
					}
					if reasoned == 0 {
						t.Fatal("no stored reply holds reasoning")
					}
				}
				requests := decodeRequests(t, sent)
				t.Logf("%d request pairs", len(requests)-1)
				if len(requests) < 4 {
					t.Fatalf("only %d requests", len(requests))
				}
				for n := 1; n < len(requests); n++ {
					prev, next := requests[n-1], requests[n]
					pair := fmt.Sprintf("requests %d and %d", n, n+1)
					if prev.SystemInstructions != next.SystemInstructions {
						t.Errorf("%s: system instruction changed", pair)
					}
					if hasCatalog(prev) || hasCatalog(next) {
						checkToolLoading(t, pair, prev, next)
					} else if !bytes.Equal(jsonOf(t, prev.Tools), jsonOf(t, next.Tools)) {
						t.Errorf("%s: Tools changed", pair)
					}
					if !bytes.Equal(jsonOf(t, prev.Options), jsonOf(t, next.Options)) || prev.CacheKey != next.CacheKey {
						t.Errorf("%s: options or cache key changed", pair)
					}
					if len(prev.CacheBreakpoints) < 2 || len(next.CacheBreakpoints) < 2 ||
						!bytes.Equal(jsonOf(t, prev.CacheBreakpoints[:2]), jsonOf(t, next.CacheBreakpoints[:2])) {
						t.Errorf("%s: the system or Tools breakpoint changed", pair)
					}
					if len(next.Messages) < len(prev.Messages) {
						t.Errorf("%s: the later request has fewer Messages (%d, then %d)", pair, len(prev.Messages), len(next.Messages))
						continue
					}
					for i, message := range prev.Messages {
						if !bytes.Equal(jsonOf(t, message), jsonOf(t, next.Messages[i])) {
							t.Errorf("%s: Message %d changed\nbefore: %s\n after: %s", pair, i, jsonOf(t, message), jsonOf(t, next.Messages[i]))
							break
						}
					}
				}
			})
		}
	}
}
