package modelctx_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// toolConversation builds: user, assistant(tool_call), tool_result, assistant,
// user, assistant — six Messages with a tool exchange in the middle.
func toolConversation() []gotato.Message {
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}
	call.StopReason = gotato.StopToolCalls
	result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultOK}, Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "r"}}}
	messages := []gotato.Message{
		gotato.UserMessage("u1"), call, result, gotato.AssistantMessage("a1"),
		gotato.UserMessage("u2"), gotato.AssistantMessage("a2"),
	}
	for i := range messages {
		messages[i].ID = gotato.MessageID("m" + string(rune('1'+i)))
	}
	return messages
}

func snapshot(messages []gotato.Message) gotato.ContextSnapshot {
	return gotato.ContextSnapshot{SystemInstructions: "sys", Messages: messages}
}

func TestStaticGoesToSystemAndPanelGoesToTail(t *testing.T) {
	fixed := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	builder := modelctx.WithPanel(
		modelctx.WithStatic(modelctx.FullHistory(), modelctx.Resource("AGENTS.md", "# Rules\nbe nice")),
		func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error) {
			return []gotato.Block{modelctx.Time(fixed), modelctx.JSON("state", map[string]int{"open": 2})}, nil
		},
	)
	report, err := modelctx.Inspect(context.Background(), builder, snapshot(append(toolConversation(), gotato.UserMessage("u3"))), nil)
	if err != nil {
		t.Fatal(err)
	}
	system := report.Request.SystemInstructions
	if !strings.HasPrefix(system, "sys\n\n<resource path=\"AGENTS.md\">\n# Rules\nbe nice\n</resource>") {
		t.Fatalf("system = %q", system)
	}
	last := report.Request.Messages[len(report.Request.Messages)-1]
	text := gotato.TextOf(last)
	if !strings.Contains(text, "<panel>") || !strings.Contains(text, "<time>2026-09-17T12:00:00Z</time>") || !strings.Contains(text, `<state>{"open":2}</state>`) {
		t.Fatalf("tail = %q", text)
	}
	// The panel is on the tail only; earlier Messages are untouched.
	for _, message := range report.Request.Messages[:len(report.Request.Messages)-1] {
		if strings.Contains(gotato.TextOf(message), "<panel>") {
			t.Fatal("panel leaked into the prefix")
		}
	}
	if report.PanelBytes == 0 || report.SystemBytes == 0 || report.Metadata["static_blocks"] != "1" || report.Metadata["panel_blocks"] != "2" {
		t.Fatalf("report = %+v", report)
	}
}

func TestPrefixHashStableAcrossAppendOnlyTurns(t *testing.T) {
	messages := toolConversation()
	builder := modelctx.WithPanel(modelctx.FullHistory(), func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error) {
		return []gotato.Block{modelctx.Time(time.Now())}, nil
	})
	tools := []gotato.ToolSpec{{ID: "t", InputSchema: []byte(`{"type":"object"}`)}}
	first, _ := modelctx.Inspect(context.Background(), builder, snapshot(messages), tools)
	// Next Turn: the tail Message changed (a new assistant answer appended and
	// a new user prompt). Everything before the new tail is identical.
	next := append(append([]gotato.Message{}, messages...), gotato.UserMessage("u3"))
	second, _ := modelctx.Inspect(context.Background(), builder, snapshot(next), tools)
	if first.PrefixHash == second.PrefixHash {
		t.Fatal("prefix must change when a message is appended before the tail")
	}
	// Same history, different panel content (time moved on): prefix identical.
	third, _ := modelctx.Inspect(context.Background(), builder, snapshot(next), tools)
	if second.PrefixHash != third.PrefixHash {
		t.Fatalf("prefix hash changed with only the panel: %s vs %s", second.PrefixHash, third.PrefixHash)
	}
	// Runtime fields never reach the prompt: IDs and usage differ, hash equal.
	altered := make([]gotato.Message, len(next))
	for i, message := range next {
		altered[i] = message.Clone()
		altered[i].ID = gotato.MessageID("other-" + string(rune('a'+i)))
		altered[i].Usage = gotato.Usage{TotalTokens: uint64(i)}
	}
	fourth, _ := modelctx.Inspect(context.Background(), builder, snapshot(altered), tools)
	if fourth.PrefixHash != second.PrefixHash {
		t.Fatal("runtime fields perturbed the prompt bytes")
	}
	wantBreakpoints := []gotato.CacheBreakpoint{{After: gotato.CacheAfterSystem}, {After: gotato.CacheAfterTools}, {After: gotato.CacheAfterMessage, Index: len(next) - 2}}
	if len(second.Request.CacheBreakpoints) != 3 {
		t.Fatalf("breakpoints = %+v", second.Request.CacheBreakpoints)
	}
	for i, want := range wantBreakpoints {
		if second.Request.CacheBreakpoints[i] != want {
			t.Fatalf("breakpoint %d = %+v, want %+v", i, second.Request.CacheBreakpoints[i], want)
		}
	}
}

func TestCompactRewritesSessionAndRecords(t *testing.T) {
	s := session.New()
	for _, message := range toolConversation() {
		_ = s.Append(message)
	}
	result, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replaced || result.MessagesBefore != 6 || result.MessagesAfter != 3 || result.TokensAfter >= result.TokensBefore {
		t.Fatalf("result = %+v", result)
	}
	messages := s.Messages()
	if messages[0].Parts[0].Metadata[modelctx.MetadataCompaction] != "summary" || gotato.TextOf(messages[1]) != "u2" {
		t.Fatalf("session after compact = %+v", messages)
	}
	if !strings.Contains(gotato.TextOf(messages[0]), "[called t]") {
		t.Fatalf("summary = %q", gotato.TextOf(messages[0]))
	}
	compactions := s.Compactions()
	if len(compactions) != 1 || compactions[0].ReplacedMessages != 4 || compactions[0].FromMessageID != "m1" || compactions[0].ToMessageID != "m4" || compactions[0].SummaryMessageID != messages[0].ID {
		t.Fatalf("compactions = %+v", compactions)
	}
	events := s.Events()
	if len(events) != 1 || events[0].Kind != gotato.EventSessionCompacted || events[0].Payload["replaced_messages"] != 4 {
		t.Fatalf("events = %+v", events)
	}
	again, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 10})
	if err != nil || again.Replaced {
		t.Fatalf("second compact = %+v err=%v", again, err)
	}
	// An Agent continues against the compacted Session and sees the summary,
	// but without the runtime metadata tag.
	model := testkit.NewFakeModel(testkit.Text("ok"))
	agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTranscript(s))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("u3")); err != nil {
		t.Fatal(err)
	}
	request, _ := model.LastRequest()
	if len(request.Messages) != 4 || !strings.HasPrefix(gotato.TextOf(request.Messages[0]), "Summary of earlier conversation") || request.Messages[0].Parts[0].Metadata != nil {
		t.Fatalf("model saw %+v", request.Messages)
	}
}

func TestCompactNeverSplitsToolCallFromResult(t *testing.T) {
	s := session.New()
	for _, message := range toolConversation() {
		_ = s.Append(message)
	}
	// Keep 5 would cut at index 1 (the tool call); the cut must move to u2.
	result, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replaced || result.MessagesAfter != 3 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCompactNoOpReportsFullState(t *testing.T) {
	s := session.New()
	for _, message := range toolConversation()[:4] {
		_ = s.Append(message)
	}
	result, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replaced || result.MessagesBefore != 4 || result.MessagesAfter != 4 || result.Compaction != nil {
		t.Fatalf("no-op result = %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"compaction"`) {
		t.Fatalf("empty compaction record leaked into JSON: %s", encoded)
	}
}

func TestCompactShrinksSinglePromptToolTail(t *testing.T) {
	s := session.New()
	_ = s.Append(gotato.UserMessage("u1"))
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}
	call.StopReason = gotato.StopToolCalls
	_ = s.Append(call)
	_ = s.Append(gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultOK}, Parts: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "r"}}})
	_ = s.Append(gotato.AssistantMessage("final1"))
	_ = s.Append(gotato.AssistantMessage("final2"))
	before := modelctx.EstimateTokens(s.Messages())

	// The only user Message is at index 0 and want lands inside the tail: the
	// fallback must still shrink instead of reporting a no-op.
	result, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replaced || result.MessagesBefore != 5 || result.MessagesAfter != 3 {
		t.Fatalf("result = %+v", result)
	}
	if modelctx.EstimateTokens(s.Messages()) >= before {
		t.Fatalf("tokens did not shrink: before %d after %d", before, modelctx.EstimateTokens(s.Messages()))
	}
	messages := s.Messages()
	if messages[0].Parts[0].Metadata[modelctx.MetadataCompaction] != "summary" || gotato.TextOf(messages[1]) != "final1" {
		t.Fatalf("session after compact = %+v", messages)
	}
}

func TestTruncateSummarizerCutsOnRuneBoundary(t *testing.T) {
	messages := []gotato.Message{gotato.UserMessage(strings.Repeat("é", 100))}
	for limit := 1; limit < 140; limit++ {
		summary, err := (modelctx.TruncateSummarizer{MaxChars: limit}).Summarize(context.Background(), messages)
		if err != nil {
			t.Fatal(err)
		}
		if text := gotato.TextOf(summary); !utf8.ValidString(text) {
			t.Fatalf("limit %d produced invalid UTF-8: %q", limit, text)
		}
	}
}

func TestAutoCompactAppliesBudgetAtRunStart(t *testing.T) {
	s := session.New()
	for i := 0; i < 20; i++ {
		_ = s.Append(gotato.UserMessage(strings.Repeat("question ", 30)))
		_ = s.Append(gotato.AssistantMessage(strings.Repeat("answer ", 30)))
	}
	before := modelctx.EstimateTokens(s.Messages())
	auto := modelctx.AutoCompact(s, modelctx.CompactPolicy{Ceiling: before / 2, Floor: before / 4})
	model := testkit.NewFakeModel(testkit.Text("ok"))
	agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTranscript(s), gotato.WithExtension(auto))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close(context.Background())
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("next")); err != nil {
		t.Fatal(err)
	}
	last, runs := auto.Last()
	if runs != 1 || !last.Replaced {
		t.Fatalf("auto compaction did not run: %+v %d", last, runs)
	}
	if got := modelctx.EstimateTokens(s.Messages()); got >= before {
		t.Fatalf("tokens after = %d, before = %d", got, before)
	}
	request, _ := model.LastRequest()
	if !strings.HasPrefix(gotato.TextOf(request.Messages[0]), "Summary of earlier conversation") {
		t.Fatalf("model did not see the summary first: %q", gotato.TextOf(request.Messages[0]))
	}
	// Below the ceiling nothing happens on the next Run.
	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("again")); err != nil {
		t.Fatal(err)
	}
	if _, runs := auto.Last(); runs != 1 {
		t.Fatalf("compacted again below ceiling: %d", runs)
	}
}

func TestModelSummarizer(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("they discussed things"))
	summary, err := modelctx.ModelSummarizer{Model: model}.Summarize(context.Background(), toolConversation())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotato.TextOf(summary), "they discussed things") {
		t.Fatalf("summary = %q", gotato.TextOf(summary))
	}
	request, _ := model.LastRequest()
	if request.SystemInstructions == "" || len(request.Messages) != 1 || !strings.Contains(gotato.TextOf(request.Messages[0]), `<conversation format="json">`) {
		t.Fatalf("request = %+v", request)
	}
}

type nilStreamModel struct{}

func (nilStreamModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, nil
}

func TestModelSummarizerRejectsIncompleteStream(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelTextDelta, Text: "partial"}})
	if _, err := (modelctx.ModelSummarizer{Model: model}).Summarize(context.Background(), toolConversation()); err == nil {
		t.Fatal("expected an error when the stream ends before ModelDone")
	}
}

func TestModelSummarizerRejectsEmptySummary(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}})
	if _, err := (modelctx.ModelSummarizer{Model: model}).Summarize(context.Background(), toolConversation()); err == nil {
		t.Fatal("expected an error for an empty summary")
	}
}

func TestModelSummarizerRejectsNilStream(t *testing.T) {
	if _, err := (modelctx.ModelSummarizer{Model: nilStreamModel{}}).Summarize(context.Background(), toolConversation()); err == nil {
		t.Fatal("expected an error when the Model returns a nil stream")
	}
}

func TestCompactKeepsHistoryWhenSummaryStreamIsIncomplete(t *testing.T) {
	s := session.New()
	_ = s.Append(gotato.UserMessage("u1"))
	_ = s.Append(gotato.AssistantMessage("a1"))
	_ = s.Append(gotato.UserMessage("u2"))
	_ = s.Append(gotato.AssistantMessage("a2"))
	before := len(s.Messages())
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelTextDelta, Text: "partial"}})
	if _, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 1, Summarizer: modelctx.ModelSummarizer{Model: model}}); err == nil {
		t.Fatal("expected Compact to fail when the summary stream is incomplete")
	}
	if len(s.Messages()) != before {
		t.Fatalf("history changed on failure: %d -> %d", before, len(s.Messages()))
	}
}
