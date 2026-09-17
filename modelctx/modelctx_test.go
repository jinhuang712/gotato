package modelctx_test

import (
	"context"
	"strings"
	"testing"

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

func TestWindowAlignsToUserBoundary(t *testing.T) {
	messages := toolConversation()
	// Asking for the last 4 would cut at index 2 (tool_result); the window
	// must move to the next user Message at index 4.
	built, err := modelctx.Window(4).Build(context.Background(), snapshot(messages))
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Messages) != 2 || gotato.TextOf(built.Messages[0]) != "u2" {
		t.Fatalf("window = %+v", built.Messages)
	}
	if built.Metadata["strategy"] != modelctx.StrategyWindow || built.Metadata["dropped_messages"] != "4" || built.Metadata["selected_messages"] != "2" {
		t.Fatalf("metadata = %v", built.Metadata)
	}
	// A window larger than history is the whole history.
	built, _ = modelctx.Window(100).Build(context.Background(), snapshot(messages))
	if len(built.Messages) != 6 {
		t.Fatalf("large window = %d", len(built.Messages))
	}
}

func TestSummaryRecentIsProjectionOnly(t *testing.T) {
	messages := toolConversation()
	built, err := modelctx.SummaryRecent(2, nil).Build(context.Background(), snapshot(messages))
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Messages) != 3 {
		t.Fatalf("summary_recent = %d messages, want summary + 2", len(built.Messages))
	}
	summary := built.Messages[0]
	if summary.Role != gotato.RoleUser || summary.Parts[0].Metadata[modelctx.MetadataCompaction] != "summary" {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(gotato.TextOf(summary), "u1") || !strings.Contains(gotato.TextOf(summary), "[called t]") {
		t.Fatalf("summary text = %q", gotato.TextOf(summary))
	}
	if built.Metadata["summarized"] != "4" || built.Metadata["summarizer"] != "truncate" {
		t.Fatalf("metadata = %v", built.Metadata)
	}
	if len(messages) != 6 {
		t.Fatal("source mutated")
	}
}

func TestChainReportsStrategies(t *testing.T) {
	built, err := modelctx.Chain(modelctx.Window(4), modelctx.SummaryRecent(1, nil)).Build(context.Background(), snapshot(toolConversation()))
	if err != nil {
		t.Fatal(err)
	}
	if built.Metadata["strategy"] != modelctx.StrategyChain || built.Metadata["chain"] != "window>summary_recent" {
		t.Fatalf("metadata = %v", built.Metadata)
	}
}

func TestInspectReport(t *testing.T) {
	report, err := modelctx.Inspect(context.Background(), modelctx.Window(2), snapshot(toolConversation()))
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceMessages != 6 || report.SelectedMessages != 2 || report.DroppedMessages != 4 || report.ApproxTokens <= 0 {
		t.Fatalf("report = %+v", report)
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
	if !result.Replaced || result.MessagesBefore != 6 || result.MessagesAfter != 3 {
		t.Fatalf("result = %+v", result)
	}
	messages := s.Messages()
	if messages[0].Parts[0].Metadata[modelctx.MetadataCompaction] != "summary" || gotato.TextOf(messages[1]) != "u2" {
		t.Fatalf("session after compact = %+v", messages)
	}
	compactions := s.Compactions()
	if len(compactions) != 1 || compactions[0].ReplacedMessages != 4 || compactions[0].FromMessageID != "m1" || compactions[0].ToMessageID != "m4" || compactions[0].SummaryMessageID != messages[0].ID {
		t.Fatalf("compactions = %+v", compactions)
	}
	events := s.Events()
	if len(events) != 1 || events[0].Kind != gotato.EventSessionCompacted || events[0].Payload["replaced_messages"] != 4 {
		t.Fatalf("events = %+v", events)
	}
	// Compacting again with nothing to drop is a no-op.
	again, err := modelctx.Compact(context.Background(), s, modelctx.CompactOptions{Keep: 10})
	if err != nil || again.Replaced {
		t.Fatalf("second compact = %+v err=%v", again, err)
	}
	// An Agent continues against the compacted Session and sees the summary.
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
	if len(request.Messages) != 4 || request.Messages[0].Parts[0].Metadata[modelctx.MetadataCompaction] != "summary" {
		t.Fatalf("model saw %d messages: %+v", len(request.Messages), request.Messages)
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
	if request.SystemInstructions == "" || len(request.Messages) != 1 {
		t.Fatalf("request = %+v", request)
	}
}

func TestParse(t *testing.T) {
	for _, spec := range []string{"", "full", "window:3", "summary:2"} {
		if _, err := modelctx.Parse(spec, nil); err != nil {
			t.Fatalf("Parse(%q) = %v", spec, err)
		}
	}
	for _, spec := range []string{"window", "window:0", "summary:x", "magic:3"} {
		if _, err := modelctx.Parse(spec, nil); err == nil {
			t.Fatalf("Parse(%q) accepted", spec)
		}
	}
}
