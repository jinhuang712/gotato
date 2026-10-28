package modelctx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// fixture is a stored Record that a test fills with Messages, each committed
// at a time, as a Run's Session would.
type fixture struct {
	t      *testing.T
	ctx    context.Context
	store  session.Store
	writer *session.Writer
	rec    *session.Record
	base   time.Time
	n      int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), store: session.NewMemoryStore(), base: time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)}
	f.rec = session.New(session.WithID("fx"))
	writer, err := session.CreateWriter(f.ctx, f.store, f.rec, "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	f.writer = writer
	return f
}

// commit stores m as committed minute minutes after 10:00 UTC.
func (f *fixture) commit(m gotato.Message, minute int) gotato.Message {
	f.t.Helper()
	f.n++
	if m.ID == "" {
		m.ID = gotato.MessageID(fmt.Sprintf("m%02d", f.n))
	}
	if err := f.writer.Append(m); err != nil {
		f.t.Fatal(err)
	}
	header := gotato.Header{Time: f.base.Add(time.Duration(minute) * time.Minute), RunID: m.RunID}
	if _, err := f.writer.Write(f.ctx, []gotato.Event{gotato.MessageCommitted{Header: header, Message: m, Source: gotato.MessageSourcePrompt}}); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) user(run, text string, minute int) gotato.Message {
	m := gotato.UserMessage(text)
	m.RunID = gotato.RunID(run)
	return f.commit(m, minute)
}

func (f *fixture) reply(run, text string, minute int) gotato.Message {
	m := gotato.AssistantMessage(text)
	m.RunID = gotato.RunID(run)
	m.StopReason = gotato.StopReasonEndTurn
	return f.commit(m, minute)
}

// calls commits an assistant Message of one Turn that calls the tools, each
// given as "id tool arguments", and commits one result per call.
func (f *fixture) calls(run, text string, minute int, statuses map[string]gotato.ToolResultStatus, calls ...string) {
	f.t.Helper()
	m := gotato.Message{Role: gotato.RoleAssistant, RunID: gotato.RunID(run), StopReason: gotato.StopReasonToolCalls,
		Parts: []gotato.ContentPart{{Kind: gotato.ContentKindReasoning, Text: "THINKING-" + run}}}
	if text != "" {
		m.Parts = append(m.Parts, gotato.ContentPart{Kind: gotato.ContentKindText, Text: text})
	}
	for _, spec := range calls {
		fields := strings.SplitN(spec, " ", 3)
		m.ToolCalls = append(m.ToolCalls, gotato.ToolCall{ID: gotato.ToolCallID(fields[0]), ToolID: fields[1], Arguments: []byte(fields[2])})
	}
	f.commit(m, minute)
	for _, call := range m.ToolCalls {
		status := gotato.ToolResultStatusOK
		if s, ok := statuses[string(call.ID)]; ok {
			status = s
		}
		result := gotato.Message{Role: gotato.RoleToolResult, RunID: gotato.RunID(run),
			ToolResult: &gotato.ToolResult{CallID: call.ID, Status: status},
			Parts:      []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "TOOLRESULT-" + string(call.ID)}}}
		f.commit(result, minute)
	}
}

func (f *fixture) load(id string) {
	f.t.Helper()
	if _, err := f.writer.Write(f.ctx, []gotato.Event{gotato.ToolLoaded{Header: gotato.Header{Time: f.base}, ToolID: id, Why: gotato.ToolLoadedWhyNames}}); err != nil {
		f.t.Fatal(err)
	}
}

// reload opens the Record as another instance would, from the store.
func (f *fixture) reload() *session.Record {
	f.t.Helper()
	if err := f.writer.Flush(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	rec, err := session.View(f.ctx, f.store, f.rec.ID())
	if err != nil {
		f.t.Fatal(err)
	}
	return rec
}

// threeRuns commits the conversation of the payments example in three Runs:
// the first two are closed by a default compaction, the third is its tail.
func threeRuns(f *fixture) {
	f.user("r1", "Some charges in last night's batch failed. Why?", 1)
	f.calls("r1", "let me look", 2, map[string]gotato.ToolResultStatus{"c2": gotato.ToolResultStatusFailed},
		`c1 payments.get_batch {"batch_id":"B-1"}`, `c2 logs.query {"order":"O-1"}`)
	f.reply("r1", "3 of 412 failed.", 5)
	f.user("r2", "Retry O-1", 40)
	f.calls("r2", "", 40, nil, `c3 payments.retry {"order":"O-1"}`)
	f.reply("r2", "O-1 went through.", 41)
	f.user("r3", "thanks", 50)
	f.reply("r3", "welcome", 51)
}

// dump writes Messages as text, one block per Message and Part.
func dump(messages []gotato.Message) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString("[" + string(m.Role) + "]\n")
		for _, part := range m.Parts {
			b.WriteString(part.Text + "\n")
		}
	}
	return b.String()
}

func mustCompact(t *testing.T, rec *session.Record, cfg modelctx.CompactConfig, opts ...modelctx.CompactOption) modelctx.Result {
	t.Helper()
	result, err := modelctx.Compact(context.Background(), rec, cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (l *live) merged() bool {
	segments := l.writer.Record().Segments()
	return len(segments) > 0 && len(segments[0].Merged) > 0
}

func (l *live) segmentCount() int { return len(l.writer.Record().Segments()) }

// segmentTokens is what the Segments of the Context hold.
func (l *live) segmentTokens() int {
	total := 0
	for _, segment := range l.writer.Record().Segments() {
		total += modelctx.EstimateTokens(segment.Messages)
	}
	return total
}

func encode(t *testing.T, messages []gotato.Message) string {
	t.Helper()
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// whyOf lists why each compaction of the Record ran, in order.
func whyOf(record *session.Record) []gotato.CompactionWhy {
	var out []gotato.CompactionWhy
	for _, event := range record.Events() {
		if ended, ok := event.(gotato.CompactionEnded); ok {
			out = append(out, ended.Why)
		}
	}
	return out
}

func (l *live) must(model gotato.Model, prompt string) {
	l.t.Helper()
	if result, err := l.run(model, prompt, "ok"); err != nil || result.Status != gotato.RunStatusCompleted {
		l.t.Fatalf("%q: %+v, %v", prompt, result, err)
	}
}

// run plays one prompt with model and a tool that returns result.
func (l *live) run(model gotato.Model, prompt, result string) (gotato.RunResult, error) {
	l.t.Helper()
	agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTranscript(l.writer),
		gotato.WithTools(testkit.NewFakeTool("demo.read", result)), gotato.WithExtension(l.auto))
	if err != nil {
		l.t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	return agent.Prompt(context.Background(), gotato.UserMessage(prompt))
}

func newLive(t *testing.T, policy modelctx.CompactPolicy) *live {
	t.Helper()
	store := session.NewMemoryStore()
	writer, err := session.CreateWriter(context.Background(), store, session.New(session.WithID("long")), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	return &live{t: t, store: store, writer: writer, auto: modelctx.AutoCompact(writer.Record(), policy)}
}

// live is a stored Session whose Runs a test plays one after another.
type live struct {
	t      *testing.T
	store  session.Store
	writer *session.Writer
	auto   *modelctx.AutoCompactor
}

func answering(text string) testkit.Script {
	script := testkit.Text(text)
	script[len(script)-1].Usage = gotato.Usage{InputTokens: 100, OutputTokens: 10}
	return script
}

func (m *stepModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *stepModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	request.Messages = append([]gotato.Message(nil), request.Messages...)
	m.requests = append(m.requests, request)
	index := len(m.requests) - 1
	if index >= len(m.steps) {
		return nil, errors.New("stepModel: no step left")
	}
	if err, ok := m.steps[index].(error); ok {
		return nil, err
	}
	return testkit.NewFakeModel(m.steps[index].(testkit.Script)).Stream(ctx, request)
}

// stepModel answers each call from steps: a refusal, or a Script. It records
// the requests it received.
type stepModel struct {
	mu       sync.Mutex
	steps    []any
	requests []gotato.ModelRequest
}

func (refusal) ContextOverflow() bool { return true }

func (refusal) Error() string { return "refused: the prompt is too long" }

// refusal is how a provider says a request is longer than its window.
type refusal struct{}

// toolConversation builds: user, assistant(tool_call), tool_result, assistant,
// user, assistant — six Messages with a tool exchange in the middle.
func toolConversation() []gotato.Message {
	call := gotato.AssistantMessage("")
	call.ToolCalls = []gotato.ToolCall{{ID: "c1", ToolID: "t", Arguments: []byte(`{}`)}}
	call.StopReason = gotato.StopReasonToolCalls
	result := gotato.Message{Role: gotato.RoleToolResult, ToolResult: &gotato.ToolResult{CallID: "c1", Status: gotato.ToolResultStatusOK}, Parts: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: "r"}}}
	messages := []gotato.Message{
		gotato.UserMessage("u1"), call, result, gotato.AssistantMessage("a1"),
		gotato.UserMessage("u2"), gotato.AssistantMessage("a2"),
	}
	for i := range messages {
		messages[i].ID = gotato.MessageID("m" + string(rune('1'+i)))
	}
	return messages
}

func (m errModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, m.err
}

type errModel struct{ err error }

func mustMerge(t *testing.T, rec *session.Record, opts modelctx.MergeConfig) modelctx.MergeResult {
	t.Helper()
	result, err := modelctx.MergeOldest(context.Background(), rec, opts)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sizes(segments []gotato.Segment) (out []int, total int) {
	for _, segment := range segments {
		n := modelctx.EstimateTokens(segment.Messages)
		out = append(out, n)
		total += n
	}
	return out, total
}

// segmentsFixture commits n Runs, each a question and an answer of about 450
// tokens together, and closes each into a Segment of its own without ever
// merging, then commits a Message that stays in the tail.
func segmentsFixture(t *testing.T, n int, recap modelctx.RecapWriter) *fixture {
	t.Helper()
	f := newFixture(t)
	for i := 1; i <= n; i++ {
		run := fmt.Sprintf("r%d", i)
		f.user(run, fmt.Sprintf("question %d: %s", i, strings.Repeat("x", 800)), i*10)
		f.reply(run, fmt.Sprintf("answer %d: %s", i, strings.Repeat("y", 800)), i*10+1)
		if result := mustCompact(t, f.rec, modelctx.CompactConfig{CompactPolicy: modelctx.CompactPolicy{SegmentSettings: modelctx.SegmentSettings{TailTokens: 1}, SegmentBudgetTokens: 1 << 30, Recap: recap}}, modelctx.WithRecapContext(f.contextOf(nil))); !result.Replaced {
			t.Fatalf("Segment %d was not closed: %+v", i, result)
		}
	}
	f.user("tail", "the message that stays in the tail", 500)
	return f
}

const goodRecap = "## What happened\n- looked at batch B-1; retried O-1\n## Key facts\n- B-1: 412 charges, 3 failed\n## Open items\n"
