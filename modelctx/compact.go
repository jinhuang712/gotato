package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// Summarizer turns a run of Messages into one summary Message. The Message it
// returns should be user-role text; Compact and SummaryRecent tag it as a
// compaction summary.
type Summarizer interface {
	Name() string
	Summarize(context.Context, []gotato.Message) (gotato.Message, error)
}

// TruncateSummarizer is the deterministic default: it concatenates the text of
// the Messages, labels each by role, and truncates to MaxChars (default 2000).
// It needs no Model, so it is safe in tests and CI.
type TruncateSummarizer struct {
	MaxChars int
}

// Name implements Summarizer.
func (TruncateSummarizer) Name() string { return "truncate" }

// Summarize implements Summarizer.
func (t TruncateSummarizer) Summarize(_ context.Context, messages []gotato.Message) (gotato.Message, error) {
	limit := t.MaxChars
	if limit <= 0 {
		limit = 2000
	}
	var b strings.Builder
	b.WriteString("Summary of earlier conversation (" + fmt.Sprint(len(messages)) + " messages):\n")
	for _, message := range messages {
		line := strings.TrimSpace(gotato.TextOf(message))
		if line == "" && message.ToolResult != nil {
			line = "[tool " + string(message.ToolResult.Status) + "]"
		}
		if len(message.ToolCalls) > 0 {
			names := make([]string, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				names = append(names, call.ToolID)
			}
			line = strings.TrimSpace(line + " [called " + strings.Join(names, ", ") + "]")
		}
		if line == "" {
			continue
		}
		b.WriteString("- " + string(message.Role) + ": " + line + "\n")
	}
	text := b.String()
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	return gotato.UserMessage(text), nil
}

// ModelSummarizer asks a Model to summarize. Instruction defaults to a
// concise, faithful summary prompt.
type ModelSummarizer struct {
	Model       gotato.Model
	Instruction string
	Name_       string
}

// Name implements Summarizer.
func (m ModelSummarizer) Name() string {
	if m.Name_ != "" {
		return m.Name_
	}
	return "model"
}

// Summarize implements Summarizer.
func (m ModelSummarizer) Summarize(ctx context.Context, messages []gotato.Message) (gotato.Message, error) {
	if m.Model == nil {
		return gotato.Message{}, errors.New("modelctx: ModelSummarizer has no Model")
	}
	instruction := m.Instruction
	if instruction == "" {
		instruction = "Summarize the conversation so far for your own future reference. Preserve decisions, facts, open questions, and tool outcomes. Be concise and faithful."
	}
	// The transcript is presented as one user Message so the request is
	// valid for any provider regardless of tool-call adjacency rules.
	encoded, _ := json.Marshal(messages)
	request := gotato.ModelRequest{
		SystemInstructions: instruction,
		Messages:           []gotato.Message{gotato.UserMessage("Conversation to summarize (JSON):\n" + string(encoded))},
	}
	stream, err := m.Model.Stream(ctx, request)
	if err != nil {
		return gotato.Message{}, err
	}
	defer stream.Close()
	var b strings.Builder
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return gotato.Message{}, err
		}
		if event.Kind == gotato.ModelTextDelta {
			b.WriteString(event.Text)
		}
		if event.Kind == gotato.ModelDone {
			break
		}
	}
	return gotato.UserMessage("Summary of earlier conversation:\n" + strings.TrimSpace(b.String())), nil
}

// CompactOptions controls Compact.
type CompactOptions struct {
	// Keep is the number of most recent Messages to retain verbatim. The cut
	// moves forward to a user Message so tool calls stay with their results.
	Keep int
	// Summarizer produces the replacement; nil means TruncateSummarizer.
	Summarizer Summarizer
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Compact permanently replaces the Messages before the last opts.Keep with
// one summary Message, records a session.Compaction, and records a
// session_compacted Event in the Session. It must not run while an Agent Run
// is using the Session. It returns the record, or a zero record with
// Replaced==false when nothing was compacted.
func Compact(ctx context.Context, s *session.Session, opts CompactOptions) (Result, error) {
	if s == nil {
		return Result{}, errors.New("modelctx: session is nil")
	}
	summarizer := opts.Summarizer
	if summarizer == nil {
		summarizer = TruncateSummarizer{}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	messages := s.Messages()
	cut := safeCut(messages, len(messages)-opts.Keep)
	if cut <= 0 {
		return Result{SessionID: s.ID(), MessagesAfter: len(messages)}, nil
	}
	before, _ := json.Marshal(messages)
	summary, err := summarizer.Summarize(ctx, messages[:cut])
	if err != nil {
		return Result{}, err
	}
	summary = tagSummary(summary)
	summary.ID = gotato.MessageID("summary-" + session.NewID()[:12])
	replaced := make([]gotato.Message, 0, len(messages)-cut+1)
	replaced = append(replaced, summary)
	replaced = append(replaced, messages[cut:]...)
	after, _ := json.Marshal(replaced)

	record := session.Compaction{
		At:               now().UTC(),
		ReplacedMessages: cut,
		FromMessageID:    messages[0].ID,
		ToMessageID:      messages[cut-1].ID,
		SummaryMessageID: summary.ID,
		Summarizer:       summarizer.Name(),
		BytesBefore:      len(before),
		BytesAfter:       len(after),
	}
	s.ReplaceMessages(replaced)
	s.RecordCompaction(record)
	s.RecordEvent(gotato.Event{
		Kind:      gotato.EventSessionCompacted,
		Class:     gotato.EventProtected,
		MessageID: summary.ID,
		Timestamp: record.At,
		Payload: map[string]any{
			"session_id":         s.ID(),
			"replaced_messages":  cut,
			"summary_message_id": string(summary.ID),
			"summarizer":         summarizer.Name(),
			"bytes_before":       len(before),
			"bytes_after":        len(after),
		},
	})
	return Result{SessionID: s.ID(), Replaced: true, Compaction: record, MessagesBefore: len(messages), MessagesAfter: len(replaced)}, nil
}

// Result reports a Compact call.
type Result struct {
	SessionID      string             `json:"session_id"`
	Replaced       bool               `json:"replaced"`
	MessagesBefore int                `json:"messages_before"`
	MessagesAfter  int                `json:"messages_after"`
	Compaction     session.Compaction `json:"compaction,omitempty"`
}
