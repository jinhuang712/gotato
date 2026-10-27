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

	"github.com/jinhuang712/gotato/gateway"
	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// This file checks G-G07 on the bytes the gateway sends where the Session
// compacts and merges (GOALS.md → G-G07). It plays the long_session task as
// TestRequestOnlyGrowsAtItsEndAcrossCompactions does, and encodes every request
// with the gateway for each wire API, with the provider rules of Claude, OpenAI
// and DeepSeek.

// wireEncoder encodes requests with a gateway Client, as it would send them.
type wireEncoder struct {
	api  string
	mu   sync.Mutex
	last map[string]any
	key  string
	send func(gotato.ModelRequest)
}

type wireKind struct {
	api, provider, model string
}

var wireKinds = []wireKind{
	{gateway.APIAnthropicMessages, gateway.ProviderAnthropic, "claude-sonnet-5-5"},
	{gateway.APIResponses, gateway.ProviderOpenAI, "gpt-6.1-sol"},
	{gateway.APIChatCompletions, gateway.ProviderDeepSeek, "deepseek-reasoner"},
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
	config := gateway.Config{API: kind.api, Provider: kind.provider, Model: kind.model, BaseURL: server.URL, APIKey: "k", CacheKeyHeader: "X-Cache-Key"}
	if kind.api == gateway.APIChatCompletions {
		config.CacheKeyField = "prompt_cache_key"
	}
	client, err := gateway.New(config)
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
	case gateway.APIResponses:
		w.instruction, _ = body["instructions"].(string)
		items, _ := body["input"].([]any)
		for _, item := range items {
			add(item)
		}
	case gateway.APIChatCompletions:
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
	return slices.DeleteFunc(slices.Clone(tools), func(tool string) bool { return strings.Contains(tool, `"name":"`+modelctx.ReadSegmentID+`"`) })
}

// TestWireRequestOnlyGrowsAtItsEndAcrossCompactions checks GOALS.md G-G07 on
// the gateway's bytes where the Session compacts and merges. The long_session
// task is played as in TestRequestOnlyGrowsAtItsEndAcrossCompactions, with and
// without the model's Recap, and the gateway encodes every request for each wire
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
// Gotato places no cache mark at the end of the last Segment (FEATURES.md,
// G-F06, Compaction), so there is none to check.
func TestWireRequestOnlyGrowsAtItsEndAcrossCompactions(t *testing.T) {
	for _, recap := range []bool{false, true} {
		t.Run(fmt.Sprintf("recap=%t", recap), func(t *testing.T) {
			task := testkit.LongSession(compactingTurns, 10, 4096, 32_000, recap)
			store := session.NewMemoryStore()
			id, sent := playLongAcross(t, store, task, nil)
			segments := segmentTexts(t, store, id)
			var requests []gotato.ModelRequest
			for _, request := range decodeRequests(t, sent) {
				last := gotato.TextOf(request.Messages[len(request.Messages)-1])
				if !strings.HasPrefix(last, "Write a recap") && !strings.HasPrefix(last, "Write one recap") {
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
				t.Run(kind.api, func(t *testing.T) {
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
	if len(next.tools) > 0 && enc.api == gateway.APIAnthropicMessages && len(next.toolMarks) != 1 {
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
	if enc.api == gateway.APIAnthropicMessages {
		for _, point := range nextReq.CacheBreakpoints {
			if point.After != gotato.CacheAfterMessage {
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
