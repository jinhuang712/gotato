package workload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/fakemodel"
	"github.com/jinhuang712/gotato/internal/reftasks"
	itext "github.com/jinhuang712/gotato/internal/text"
	"github.com/jinhuang712/gotato/model"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/ref"
	"github.com/jinhuang712/gotato/store"
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

func (m windowedLogging) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	b, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	m.log.add(b)
	return m.inner.Stream(ctx, request)
}

// Config states the window, so the Runner takes the default ceiling from it.
func (m windowedLogging) Config() gotato.ModelConfig {
	return gotato.ModelConfig{Facts: gotato.ModelFacts{Window: gotato.WindowTokens(m.window)}}
}

func longRunner(t *testing.T, st store.Store, task reftasks.Task, model gotato.Model, instance string) *host.Runner {
	t.Helper()
	agent := profile.AgentProfile{Name: task.Name, Model: model, Instruction: task.Instruction, Tools: task.Tools, ToolSets: task.ToolSets, Limits: task.Limits}
	if task.Recap {
		agent.Compact.Recap = record.ModelRecap{}
	}
	runner, err := host.New(withAgents(host.Config{Store: st, Instance: instance}, []profile.AgentProfile{agent}...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// playLongAcross plays a compacting task as Runs that move between instances
// and reloads (a, a after a reload, b, a after a reload, and so on), and hands
// each Run's end to check. It returns the Session's ID and the requests.
func playLongAcross(t *testing.T, st store.Store, task reftasks.Task, afterRun func(runner *host.Runner, id string)) (string, [][]byte) {
	t.Helper()
	ctx := context.Background()
	log := &requestLog{}
	model := windowedLogging{inner: task.Model, window: task.Window, log: log}
	stages := []string{"a", "a", "b", "a"}
	runner := longRunner(t, st, task, model, "a")
	s, err := runner.CreateSession(ctx, host.CreateSessionRequest{Agent: task.Name})
	if err != nil {
		t.Fatal(err)
	}
	for i, prompt := range task.Prompts {
		if i%len(stages) != 0 {
			if err := runner.Close(ctx); err != nil {
				t.Fatal(err)
			}
			runner = longRunner(t, st, task, model, stages[i%len(stages)])
		}
		result, err := runner.Run(ctx, host.RunRequest{SessionID: s.ID, Prompt: prompt})
		if err != nil || result.Result.Status != gotato.RunStatusCompleted {
			t.Fatalf("Run %d on %s: %v (%s)", i+1, runner.Instance(), err, result.Result.Status)
		}
		if afterRun != nil {
			afterRun(runner, s.ID)
		}
	}
	return s.ID, log.all()
}

// segmentTexts collects, as JSON, the Messages of every Segment the log holds,
// merged and merged away alike: the bytes a request may carry as a Segment's.
func segmentTexts(t *testing.T, st store.Store, id string) map[string]bool {
	t.Helper()
	events, err := store.StoredEvents(context.Background(), st.Records, id, 0, 0)
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

// TestRequestOnlyGrowsAtItsEndAcrossCompactions checks GOALS.md O3 where
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
			st := memStore()
			id, sent := playLongAcross(t, st, task, nil)
			final, err := store.Open(context.Background(), st.Records, id)
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
			segments := segmentTexts(t, st, id)
			// The requests that ask for a Recap are not Turns: they are the Context
			// with an ask after it, and their own options.
			var requests []gotato.ModelRequest
			for _, request := range decodeRequests(t, sent) {
				last := gotato.TextOf(request.Messages[len(request.Messages)-1])
				if !itext.IsRecapAsk(last) {
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
				if !bytes.Equal(jsonOf(t, prev.Sampling), jsonOf(t, next.Sampling)) || prev.CacheKey != next.CacheKey {
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
		if spec.ID != ref.ReadSegment {
			rest = append(rest, spec)
		}
	}
	return bytes.Equal(jsonOf2(rest), jsonOf2(prev))
}

func jsonOf2(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TestSameRecordSameRequestAcrossCompactions checks GOALS.md O2 where the
// Session compacts. At the end of every Run of the long_session task, the
// Session is inspected by the instance that ran it, by the other instance, and
// by a new Runner after a reload; the three requests are the same bytes, whether
// the Record's last Checkpoint is a compaction, a merge, or neither.
func TestSameRecordSameRequestAcrossCompactions(t *testing.T) {
	for _, recap := range []bool{false, true} {
		t.Run(fmt.Sprintf("recap=%t", recap), func(t *testing.T) {
			task := reftasks.LongSession(compactingTurns, 10, 4096, 32_000, recap)
			st := memStore()
			model := windowedLogging{inner: task.Model, window: task.Window, log: &requestLog{}}
			compared := 0
			check := func(runner *host.Runner, id string) {
				ctx := context.Background()
				live, err := runner.Inspect(ctx, id, host.InspectConfig{})
				if err != nil {
					t.Fatal(err)
				}
				want := jsonOf(t, live.Request)
				views := map[string]*host.Runner{
					"the other instance":      longRunner(t, st, task, model, "other"),
					"a new Runner (a reload)": longRunner(t, st, task, model, runner.Instance()),
				}
				for name, view := range views {
					report, err := view.Inspect(ctx, id, host.InspectConfig{})
					if err != nil {
						t.Fatal(err)
					}
					if got := jsonOf(t, report.Request); !bytes.Equal(got, want) {
						t.Fatalf("Run %d: %s builds another request\n got: %.300s\nwant: %.300s", compared+1, name, got, want)
					}
				}
				compared++
			}
			id, _ := playLongAcross(t, st, task, check)
			final, err := store.Open(context.Background(), st.Records, id)
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

// This file checks O3 on the bytes the model bridge sends where the Session
// compacts and merges (GOALS.md → O3). It plays the long_session task as
// TestRequestOnlyGrowsAtItsEndAcrossCompactions does, and encodes every request
// with the model bridge for each wire API, with the facts of a Claude model, an
// OpenAI model and a DeepSeek model.

// wireEncoder encodes requests with a model.Client, as it would send them.
type wireEncoder struct {
	api  wireName
	mu   sync.Mutex
	last map[string]any
	key  string
	send func(gotato.ModelRequest)
}

// wireName is the name of a wire API as the fake model endpoint spells it.
type wireName string

const (
	apiAnthropic wireName = fakemodel.APIAnthropic
	apiResponses wireName = fakemodel.APIResponses
	apiChat      wireName = fakemodel.APIChat
)

// wireKind is a wire API with the model that speaks it: the facts are what a
// provider's rules used to be.
type wireKind struct {
	api   wireName
	wire  model.WireAPI
	path  string
	model gotato.ModelConfig
}

var wireKinds = []wireKind{
	{apiAnthropic, model.AnthropicMessages{}, "/v1/messages", gotato.ModelConfig{Name: "claude-sonnet-5-5", Facts: gotato.ModelFacts{
		Window: gotato.WindowUnknown(), CacheRetention: time.Hour, RoundReasoning: true, ThinkingBinding: true}}},
	{apiResponses, model.Responses{}, "/v1/responses", gotato.ModelConfig{Name: "gpt-6.1-sol", Facts: gotato.ModelFacts{
		Window: gotato.WindowUnknown(), CacheRetention: 30 * time.Minute, RoundReasoning: true}}},
	{apiChat, model.ChatCompletions{CacheKeyField: "prompt_cache_key"}, "/v1/chat/completions", gotato.ModelConfig{Name: "deepseek-reasoner", Facts: gotato.ModelFacts{
		Window: gotato.WindowUnknown()}}},
}

func newWireEncoder(t *testing.T, kind wireKind) *wireEncoder {
	t.Helper()
	e := &wireEncoder{api: kind.api}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		e.last, e.key = body, r.Header.Get("X-Cache-Key")
		e.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	t.Cleanup(server.Close)
	client, err := model.New(kind.wire, model.Connection{Endpoint: server.URL + kind.path, APIKey: "k", CacheKeyHeader: "X-Cache-Key"}, kind.model)
	if err != nil {
		t.Fatal(err)
	}
	e.send = func(request gotato.ModelRequest) {
		stream, err := client.Stream(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
	}
	return e
}

// wireRequest is one encoded request.
type wireRequest struct {
	body        map[string]any
	cacheKey    string
	instruction string
	// instructionMarked says the instruction carries a cache_control mark.
	instructionMarked bool
	tools             []string
	// toolMarks are the indexes of the tools with a cache_control mark.
	toolMarks []int
	elements  []string
	// marks are the indexes of the elements with a cache_control mark.
	marks []int
}

func (e *wireEncoder) encode(t *testing.T, request gotato.ModelRequest) wireRequest {
	t.Helper()
	e.send(request)
	e.mu.Lock()
	defer e.mu.Unlock()
	body := e.last
	w := wireRequest{body: body, cacheKey: e.key}
	add := func(v any) {
		isMarked := marked(v)
		b, err := json.Marshal(stripCacheControl(v))
		if err != nil {
			t.Fatal(err)
		}
		w.elements = append(w.elements, string(b))
		if isMarked {
			w.marks = append(w.marks, len(w.elements)-1)
		}
	}
	switch e.api {
	case apiResponses:
		w.instruction, _ = body["instructions"].(string)
		items, _ := body["input"].([]any)
		for _, item := range items {
			add(item)
		}
	case apiChat:
		messages, _ := body["messages"].([]any)
		for i, m := range messages {
			if message, _ := m.(map[string]any); i == 0 && message["role"] == "system" {
				w.instruction, _ = message["content"].(string)
				continue
			}
			add(m)
		}
	default:
		system, _ := body["system"].([]any)
		var texts []string
		for _, block := range system {
			b, _ := block.(map[string]any)
			text, _ := b["text"].(string)
			texts = append(texts, text)
			w.instructionMarked = marked(block)
		}
		w.instruction = strings.Join(texts, "\n")
		messages, _ := body["messages"].([]any)
		for _, m := range messages {
			message, _ := m.(map[string]any)
			blocks, _ := message["content"].([]any)
			for _, block := range blocks {
				// The mark belongs to the block, the role to the message.
				var mark any
				if b, ok := block.(map[string]any); ok {
					mark = b["cache_control"]
				}
				add(map[string]any{"role": message["role"], "block": block, "cache_control": mark})
			}
		}
	}
	tools, _ := body["tools"].([]any)
	for i, tool := range tools {
		isMarked := marked(tool)
		b, err := json.Marshal(stripCacheControl(tool))
		if err != nil {
			t.Fatal(err)
		}
		w.tools = append(w.tools, string(b))
		if isMarked {
			w.toolMarks = append(w.toolMarks, i)
		}
	}
	return w
}

func marked(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	c, ok := m["cache_control"]
	return ok && c != nil
}

// blocks counts the elements of the first n Messages of a request on their own.
// An element is a content block (Anthropic) or an item (the others), so the
// count does not depend on how the wire API joins neighbouring Messages.
func (e *wireEncoder) blocks(t *testing.T, request gotato.ModelRequest, n int) int {
	t.Helper()
	if n == 0 {
		return 0
	}
	request.Messages = request.Messages[:n]
	return len(e.encode(t, request).elements)
}

// pairKind says how request next follows request prev at the level of the
// ModelRequest: it extends it, or a compaction closed a Segment between them
// (the Segments before keep their Messages), or a merge did.
type pairKind struct {
	kind string // "extends", "closing" or "merge"
	// before and after are the Messages the two requests open with that belong
	// to a Segment; kept is where in before the Segments left after a merge
	// begin.
	before, after, kept int
}

func classifyPair(t *testing.T, prev, next gotato.ModelRequest, segments map[string]bool) pairKind {
	t.Helper()
	extends := len(next.Messages) >= len(prev.Messages)
	for i := 0; extends && i < len(prev.Messages); i++ {
		extends = bytes.Equal(jsonOf(t, gotato.ForModel(prev.Messages[i:i+1])), jsonOf(t, gotato.ForModel(next.Messages[i:i+1])))
	}
	if extends {
		return pairKind{kind: "extends"}
	}
	before, after := leadingSegments(t, prev, segments), leadingSegments(t, next, segments)
	if startsWith(after, before) {
		return pairKind{kind: "closing", before: len(before), after: len(after)}
	}
	kept := len(before) + 1
	for j := len(before); j >= 1 && len(after) > 0; j-- {
		if startsWith(after[1:], before[j:]) {
			kept = j
		}
	}
	if len(after) == 0 || !strings.Contains(after[0], `segment n=\"1-`) || kept >= len(before) {
		t.Fatalf("the Segments before did not keep their Messages: %d before, %d after", len(before), len(after))
	}
	return pairKind{kind: "merge", before: len(before), after: len(after), kept: kept}
}

// withoutReadSegment is the tool list less the read_segment tool.
func withoutReadSegment(tools []string) []string {
	return slices.DeleteFunc(slices.Clone(tools), func(tool string) bool { return strings.Contains(tool, `"name":"`+ref.ReadSegment+`"`) })
}

// TestWireRequestOnlyGrowsAtItsEndAcrossCompactions checks GOALS.md O3 on
// the model bridge's bytes where the Session compacts and merges. The long_session
// task is played as in TestRequestOnlyGrowsAtItsEndAcrossCompactions, with and
// without the model's Recap, and the model bridge encodes every request for each wire
// API (Claude, OpenAI and DeepSeek rules). In each pair of consecutive requests:
//
//   - the cache key and every option keep their bytes, and the instruction
//     keeps its own, apart from the closing sentence about the tags;
//   - the tool list keeps its bytes, or gains read_segment and nothing else;
//   - with no compaction between them, the earlier request's elements come first
//     in the later one, byte for byte, cache marks aside;
//   - across a compaction that closed a Segment, the elements of the Segments
//     before come first, byte for byte;
//   - across a merge, the merged Segment comes first, and the elements of the
//     Segments that were left follow it, byte for byte;
//   - on Anthropic, the cache_control marks on the instruction and the tools stay,
//     and each breakpoint of the request lands on the end of the Message it
//     names, and no more than four marks go out.
//
// Gotato places no cache mark at the end of the last Segment (specs/compaction.md), so there is none to check.
func TestWireRequestOnlyGrowsAtItsEndAcrossCompactions(t *testing.T) {
	for _, recap := range []bool{false, true} {
		t.Run(fmt.Sprintf("recap=%t", recap), func(t *testing.T) {
			task := reftasks.LongSession(compactingTurns, 10, 4096, 32_000, recap)
			store := memStore()
			id, sent := playLongAcross(t, store, task, nil)
			segments := segmentTexts(t, store, id)
			var requests []gotato.ModelRequest
			for _, request := range decodeRequests(t, sent) {
				last := gotato.TextOf(request.Messages[len(request.Messages)-1])
				if !itext.IsRecapAsk(last) {
					requests = append(requests, request)
				}
			}
			kinds := make([]pairKind, len(requests))
			closings, merges := 0, 0
			for n := 1; n < len(requests); n++ {
				kinds[n] = classifyPair(t, requests[n-1], requests[n], segments)
				switch kinds[n].kind {
				case "closing":
					closings++
				case "merge":
					merges++
				}
			}
			if closings < 10 || merges < 1 {
				t.Fatalf("%d closings and %d merges: the play must compact and merge", closings, merges)
			}
			for _, kind := range wireKinds {
				t.Run(string(kind.api), func(t *testing.T) {
					enc := newWireEncoder(t, kind)
					wires := make([]wireRequest, len(requests))
					for i, request := range requests {
						wires[i] = enc.encode(t, request)
					}
					for n := 1; n < len(requests); n++ {
						checkWirePair(t, enc, fmt.Sprintf("requests %d and %d", n, n+1), requests[n-1], requests[n], wires[n-1], wires[n], kinds[n])
					}
					t.Logf("%d request pairs, %d across a compaction and %d across a merge", len(requests)-1, closings, merges)
				})
			}
		})
	}
}

func checkWirePair(t *testing.T, enc *wireEncoder, pair string, prevReq, nextReq gotato.ModelRequest, prev, next wireRequest, kind pairKind) {
	t.Helper()
	// The options and the cache key.
	if prev.cacheKey == "" || prev.cacheKey != next.cacheKey {
		t.Errorf("%s: cache key %q, then %q", pair, prev.cacheKey, next.cacheKey)
	}
	for key, value := range prev.body {
		switch key {
		case "messages", "input", "system", "instructions", "tools":
			continue
		}
		if a, b := jsonOf(t, value), jsonOf(t, next.body[key]); !bytes.Equal(a, b) {
			t.Errorf("%s: %q changed\nbefore: %.200s\n after: %.200s", pair, key, a, b)
		}
	}
	// The instruction.
	if prev.instruction != next.instruction && next.instruction != prev.instruction+"\n\n"+gotato.TagsSentence && next.instruction != prev.instruction+gotato.TagsSentence {
		t.Errorf("%s: the instruction changed beyond the sentence about the tags", pair)
	}
	if prev.instructionMarked != next.instructionMarked {
		t.Errorf("%s: the cache mark on the instruction changed", pair)
	}
	// The tool list.
	if !slices.Equal(prev.tools, next.tools) && !slices.Equal(prev.tools, withoutReadSegment(next.tools)) {
		t.Errorf("%s: the tool list changed beyond read_segment joining it\nbefore: %v\n after: %v", pair, prev.tools, next.tools)
	}
	if len(next.tools) > 0 && enc.api == apiAnthropic && len(next.toolMarks) != 1 {
		t.Errorf("%s: %d cache marks on the tools", pair, len(next.toolMarks))
	}
	// The conversation.
	p, n := prev.elements, next.elements
	switch kind.kind {
	case "extends":
		if len(n) < len(p) || !slices.Equal(p, n[:len(p)]) {
			t.Errorf("%s: the earlier elements do not come first, byte for byte", pair)
		}
	case "closing":
		segment := enc.blocks(t, prevReq, kind.before)
		if (segment == 0 && kind.before > 0) || len(n) < segment || !slices.Equal(p[:segment], n[:segment]) {
			t.Errorf("%s: the elements of the Segments before do not come first, byte for byte (%d of them)", pair, segment)
		}
		if enc.blocks(t, nextReq, kind.after) <= segment {
			t.Errorf("%s: the new Segment adds no element", pair)
		}
	case "merge":
		merged := enc.blocks(t, nextReq, 1)
		from, to := enc.blocks(t, prevReq, kind.kept), enc.blocks(t, prevReq, kind.before)
		if to <= from || len(n) < merged+to-from || !slices.Equal(p[from:to], n[merged:merged+to-from]) {
			t.Errorf("%s: the elements of the Segments left do not follow the merged Segment, byte for byte (%d of them)", pair, to-from)
		}
	}
	// The breakpoints on Anthropic: each lands on the end of the Message it names.
	if enc.api == apiAnthropic {
		for _, point := range nextReq.CacheBreakpoints {
			if point.After != gotato.CacheAnchorAfterMessage {
				continue
			}
			end := enc.blocks(t, nextReq, point.Index+1) - 1
			if !slices.Contains(next.marks, end) {
				t.Errorf("%s: the breakpoint after Message %d (element %d) has no cache mark; marks at %v", pair, point.Index, end, next.marks)
			}
		}
		if len(next.marks)+len(next.toolMarks)+btoi(next.instructionMarked) > 4 {
			t.Errorf("%s: more than four cache marks", pair)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
