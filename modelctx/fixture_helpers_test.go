package modelctx_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/session"
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
