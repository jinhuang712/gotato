package modelctx_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/modelctx"
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

func TestStaticGoesToSystemAndHistoryGoesAsCommitted(t *testing.T) {
	builder := modelctx.WithStatic(modelctx.FullHistory(),
		modelctx.Resource("AGENTS.md", "# Rules\nbe nice"),
		modelctx.JSON("state", map[string]int{"open": 2}),
	)
	history := append(toolConversation(), gotato.UserMessage("u3"))
	report, err := modelctx.Inspect(context.Background(), builder, snapshot(history), nil)
	if err != nil {
		t.Fatal(err)
	}
	system := report.Request.SystemInstructions
	if system != "sys\n\n<resource path=\"AGENTS.md\">\n# Rules\nbe nice\n</resource>\n<state>{\"open\":2}</state>" {
		t.Fatalf("system = %q", system)
	}
	if last := report.Request.Messages[len(report.Request.Messages)-1]; gotato.TextOf(last) != "u3" {
		t.Fatalf("tail = %q; the last Message goes as committed", gotato.TextOf(last))
	}
	if report.SystemBytes == 0 || report.Metadata["static_blocks"] != "2" {
		t.Fatalf("report = %+v", report)
	}
}

func TestPrefixHashStableAcrossAppendOnlyTurns(t *testing.T) {
	messages := toolConversation()
	builder := modelctx.FullHistory()
	tools := []gotato.ToolSpec{{ID: "t", InputSchema: []byte(`{"type":"object"}`)}}
	first, _ := modelctx.Inspect(context.Background(), builder, snapshot(messages), tools)
	// Next Turn: the tail Message changed (a new assistant answer appended and
	// a new user prompt). Everything before the new tail is identical.
	next := append(append([]gotato.Message{}, messages...), gotato.UserMessage("u3"))
	second, _ := modelctx.Inspect(context.Background(), builder, snapshot(next), tools)
	if first.PrefixHash == second.PrefixHash {
		t.Fatal("prefix must change when a message is appended before the tail")
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
	wantBreakpoints := []gotato.CacheBreakpoint{{After: gotato.CacheAfterSystem}, {After: gotato.CacheAfterTools}, {After: gotato.CacheAfterMessage, Index: len(next) - 1}}
	if len(second.Request.CacheBreakpoints) != 3 {
		t.Fatalf("breakpoints = %+v", second.Request.CacheBreakpoints)
	}
	for i, want := range wantBreakpoints {
		if second.Request.CacheBreakpoints[i] != want {
			t.Fatalf("breakpoint %d = %+v, want %+v", i, second.Request.CacheBreakpoints[i], want)
		}
	}
}

func TestTruncateRecapCutsOnRuneBoundary(t *testing.T) {
	messages := []gotato.Message{gotato.UserMessage(strings.Repeat("é", 100))}
	for limit := 1; limit < 140; limit++ {
		summary, err := (modelctx.TruncateRecap{MaxChars: limit}).WriteRecap(context.Background(), modelctx.RecapRequest{Closed: messages})
		if err != nil {
			t.Fatal(err)
		}
		if text := gotato.TextOf(summary); !utf8.ValidString(text) {
			t.Fatalf("limit %d produced invalid UTF-8: %q", limit, text)
		}
	}
}

type nilStreamModel struct{}

func (nilStreamModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, nil
}

// recapRequest is a RecapRequest with the Context of a small conversation.
func recapRequest() modelctx.RecapRequest {
	context := gotato.ModelRequest{SystemInstructions: "be brief", Messages: toolConversation()}
	return modelctx.RecapRequest{Segment: 1, Closed: toolConversation(), Context: &context}
}

func TestModelRecapRejectsIncompleteStream(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelTextDelta, Text: "partial"}})
	if _, err := (modelctx.ModelRecap{Model: model}).WriteRecap(context.Background(), recapRequest()); err == nil {
		t.Fatal("expected an error when the stream ends before ModelDone")
	}
}

func TestModelRecapRejectsEmptySummary(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Script{{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}})
	if _, err := (modelctx.ModelRecap{Model: model}).WriteRecap(context.Background(), recapRequest()); err == nil {
		t.Fatal("expected an error for an empty summary")
	}
}

func TestModelRecapRejectsNilStream(t *testing.T) {
	if _, err := (modelctx.ModelRecap{Model: nilStreamModel{}}).WriteRecap(context.Background(), recapRequest()); err == nil {
		t.Fatal("expected an error when the Model returns a nil stream")
	}
}

type failingRecap struct{}

func (failingRecap) Name() string { return "failing" }
func (failingRecap) WriteRecap(context.Context, modelctx.RecapRequest) (gotato.Message, error) {
	return gotato.Message{}, errors.New("recap writer down")
}
