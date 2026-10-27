package workload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// compactingTurns is how many Turns the compacting plays take: enough for dozens
// of compactions and several merges, fewer under the race detector.
var compactingTurns = func() int {
	if raceEnabled {
		return 200
	}
	return 300
}()

// windowedLogging gives the long_session Model its window, and keeps the JSON
// of every request it received, in order.
type windowedLogging struct {
	inner  gotato.Model
	window int
	log    *requestLog
}

func (m windowedLogging) ContextWindow() int { return m.window }

func (m windowedLogging) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	b, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	m.log.add(b)
	return m.inner.Stream(ctx, request)
}

func longRunner(t *testing.T, store session.Store, task reftasks.Task, model gotato.Model, instance string) *service.Runner {
	t.Helper()
	agent := service.Agent{Name: task.Name, Model: model, Instruction: task.Instruction, Tools: task.Tools, Options: task.Options, Limits: task.Limits}
	if task.Recap {
		agent.Compact.Recap = modelctx.ModelRecap{}
	}
	runner, err := service.New(service.Config{Store: store, Instance: instance, Agents: []service.Agent{agent}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// playLongAcross plays a compacting task as Runs that move between instances
// and reloads (a, a after a reload, b, a after a reload, and so on), and hands
// each Run's end to check. It returns the Session's ID and the requests.
func playLongAcross(t *testing.T, store session.Store, task reftasks.Task, afterRun func(runner *service.Runner, id string)) (string, [][]byte) {
	t.Helper()
	ctx := context.Background()
	log := &requestLog{}
	model := windowedLogging{inner: task.Model, window: task.Window, log: log}
	stages := []string{"a", "a", "b", "a"}
	runner := longRunner(t, store, task, model, "a")
	s, err := runner.CreateSession(ctx, task.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, prompt := range task.Prompts {
		if i%len(stages) != 0 {
			if err := runner.Close(ctx); err != nil {
				t.Fatal(err)
			}
			runner = longRunner(t, store, task, model, stages[i%len(stages)])
		}
		result, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt})
		if err != nil || result.Result.Status != gotato.RunStatusCompleted {
			t.Fatalf("Run %d on %s: %v (%s)", i+1, runner.Instance(), err, result.Result.Status)
		}
		if afterRun != nil {
			afterRun(runner, s.ID())
		}
	}
	return s.ID(), log.all()
}

// segmentTexts collects, as JSON, the Messages of every Segment the log holds,
// merged and merged away alike: the bytes a request may carry as a Segment's.
func segmentTexts(t *testing.T, store session.Store, id string) map[string]bool {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, event := range events {
		if closed, ok := event.(gotato.SegmentClosed); ok {
			for _, message := range gotato.ForModel(closed.Segment.Messages) {
				out[string(jsonOf(t, message))] = true
			}
		}
	}
	return out
}

// leadingSegments returns the JSON of the Messages a request opens with that
// are a Segment's.
func leadingSegments(t *testing.T, request gotato.ModelRequest, segments map[string]bool) []string {
	t.Helper()
	var out []string
	for _, message := range request.Messages {
		b := string(jsonOf(t, message))
		if !segments[b] {
			break
		}
		out = append(out, b)
	}
	return out
}

func startsWith(list, prefix []string) bool {
	if len(prefix) > len(list) {
		return false
	}
	for i := range prefix {
		if list[i] != prefix[i] {
			return false
		}
	}
	return true
}

// TestRequestOnlyGrowsAtItsEndAcrossCompactions checks GOALS.md G-G07 where
// the Session compacts. The long_session task is played as several Runs that
// move between instances and reloads, with compactions mid-Run and merges. Every
// request keeps the instruction, the options and the cache key of the one before
// it, and starts with its Messages, byte for byte, except where a compaction or
// a merge came between them. At such a pair, the Segments the request opened
// with keep their bytes: the new request opens with all of them and one more,
// or, after a merge, with the merged Segment and then the Segments that were
// left, as they were.
func TestRequestOnlyGrowsAtItsEndAcrossCompactions(t *testing.T) {
	for _, recap := range []bool{false, true} {
		t.Run(fmt.Sprintf("recap=%t", recap), func(t *testing.T) {
			task := reftasks.LongSession(compactingTurns, 10, 4096, 32_000, recap)
			store := session.NewMemoryStore()
			id, sent := playLongAcross(t, store, task, nil)
			final, err := session.Open(context.Background(), store, id)
			if err != nil {
				t.Fatal(err)
			}
			closings, merges := 0, 0
			for _, c := range final.Compactions() {
				if len(c.Merged) > 0 {
					merges++
				} else {
					closings++
				}
			}
			if closings < 10 || merges < 1 {
				t.Fatalf("%d closings and %d merges: the play must compact and merge", closings, merges)
			}
			segments := segmentTexts(t, store, id)
			// The requests that ask for a Recap are not Turns: they are the Context
			// with an ask after it, and their own options.
			var requests []gotato.ModelRequest
			for _, request := range decodeRequests(t, sent) {
				last := gotato.TextOf(request.Messages[len(request.Messages)-1])
				if !strings.HasPrefix(last, "Write a recap") && !strings.HasPrefix(last, "Write one recap") {
					requests = append(requests, request)
				}
			}
			breaks, mergedBreaks := 0, 0
			for n := 1; n < len(requests); n++ {
				prev, next := requests[n-1], requests[n]
				pair := fmt.Sprintf("requests %d and %d", n, n+1)
				// The one change is the fixed sentence about the tags, which ends the
				// system part from the request that holds the first Segment on.
				if prev.SystemInstructions != next.SystemInstructions &&
					next.SystemInstructions != prev.SystemInstructions+"\n\n"+gotato.TagsSentence && next.SystemInstructions != prev.SystemInstructions+gotato.TagsSentence {
					t.Errorf("%s: system instruction changed", pair)
				}
				if !bytes.Equal(jsonOf(t, prev.Config), jsonOf(t, next.Config)) || prev.CacheKey != next.CacheKey {
					t.Errorf("%s: options or cache key changed", pair)
				}
				extends := len(next.Messages) >= len(prev.Messages)
				for i := 0; extends && i < len(prev.Messages); i++ {
					extends = bytes.Equal(jsonOf(t, gotato.ForModel(prev.Messages[i:i+1])), jsonOf(t, gotato.ForModel(next.Messages[i:i+1])))
				}
				if extends {
					if !bytes.Equal(jsonOf(t, prev.Tools), jsonOf(t, next.Tools)) && !isReadSegmentAdded(prev.Tools, next.Tools) {
						t.Errorf("%s: Tools changed with no compaction", pair)
					}
					continue
				}
				// A compaction or a merge came between the two.
				breaks++
				before, after := leadingSegments(t, prev, segments), leadingSegments(t, next, segments)
				switch {
				case startsWith(after, before):
					// Closing a Segment: the Segments before keep their bytes.
				default:
					// A merge: the merged Segment first, then the Segments that
					// were left, as they were.
					mergedBreaks++
					// The smallest suffix of the old Segments that the new request
					// opens with, after the merged one: it must not be empty.
					kept := len(before) + 1
					for j := len(before); j >= 1 && len(after) > 0; j-- {
						if startsWith(after[1:], before[j:]) {
							kept = j
						}
					}
					if len(after) == 0 || !strings.Contains(after[0], `segment n=\"1-`) || kept >= len(before) {
						t.Errorf("%s: the Segments before did not keep their bytes\nbefore: %d Segments\n after: %d Segments, first %.120s", pair, len(before), len(after), strings.Join(after[:min(1, len(after))], ""))
					}
				}
			}
			if breaks == 0 || breaks > closings+merges || mergedBreaks == 0 {
				t.Fatalf("%d pairs changed (%d by a merge) for %d closings and %d merges", breaks, mergedBreaks, closings, merges)
			}
			t.Logf("%d request pairs, %d of them across a compaction (%d across a merge)", len(requests)-1, breaks, mergedBreaks)
		})
	}
}

// isReadSegmentAdded says next is prev with read_segment added.
func isReadSegmentAdded(prev, next []gotato.ToolSpec) bool {
	if len(next) != len(prev)+1 {
		return false
	}
	rest := make([]gotato.ToolSpec, 0, len(prev))
	for _, spec := range next {
		if spec.ID != modelctx.ReadSegmentID {
			rest = append(rest, spec)
		}
	}
	return bytes.Equal(jsonOf2(rest), jsonOf2(prev))
}

func jsonOf2(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TestSameRecordSameRequestAcrossCompactions checks GOALS.md G-G09 where the
// Session compacts. At the end of every Run of the long_session task, the
// Session is inspected by the instance that ran it, by the other instance, and
// by a new Runner after a reload; the three requests are the same bytes, whether
// the Record's last Checkpoint is a compaction, a merge, or neither.
func TestSameRecordSameRequestAcrossCompactions(t *testing.T) {
	for _, recap := range []bool{false, true} {
		t.Run(fmt.Sprintf("recap=%t", recap), func(t *testing.T) {
			task := reftasks.LongSession(compactingTurns, 10, 4096, 32_000, recap)
			store := session.NewMemoryStore()
			model := windowedLogging{inner: task.Model, window: task.Window, log: &requestLog{}}
			compared := 0
			check := func(runner *service.Runner, id string) {
				ctx := context.Background()
				live, err := runner.Inspect(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				want := jsonOf(t, live.Request)
				views := map[string]*service.Runner{
					"the other instance":      longRunner(t, store, task, model, "other"),
					"a new Runner (a reload)": longRunner(t, store, task, model, runner.Instance()),
				}
				for name, view := range views {
					report, err := view.Inspect(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					if got := jsonOf(t, report.Request); !bytes.Equal(got, want) {
						t.Fatalf("Run %d: %s builds another request\n got: %.300s\nwant: %.300s", compared+1, name, got, want)
					}
				}
				compared++
			}
			id, _ := playLongAcross(t, store, task, check)
			final, err := session.Open(context.Background(), store, id)
			if err != nil {
				t.Fatal(err)
			}
			merged := false
			for _, c := range final.Compactions() {
				merged = merged || len(c.Merged) > 0
			}
			if compared != len(task.Prompts) || len(final.Compactions()) < 10 || !merged {
				t.Fatalf("compared %d Runs of %d, %d compactions, merged %t", compared, len(task.Prompts), len(final.Compactions()), merged)
			}
		})
	}
}
