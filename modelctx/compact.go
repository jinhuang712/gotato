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
// returns should be user-role text; Compact tags it as a compaction summary.
type Summarizer interface {
	Name() string
	Summarize(context.Context, []gotato.Message) (gotato.Message, error)
}

// TruncateSummarizer is the deterministic default: it lists the text of the
// Messages by role and truncates to MaxChars (default 2000). It needs no
// Model, so it is safe in tests and CI.
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
	Label       string
}

// Name implements Summarizer.
func (m ModelSummarizer) Name() string {
	if m.Label != "" {
		return m.Label
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
	// The transcript is presented as one user Message inside a tagged block
	// so the request is valid for any provider regardless of tool-call
	// adjacency rules.
	encoded, _ := json.Marshal(gotato.ForModel(messages))
	body := gotato.RenderBlocks([]gotato.Block{{Tag: "conversation", Attrs: map[string]string{"format": "json"}, Text: string(encoded)}})
	request := gotato.ModelRequest{
		SystemInstructions: instruction,
		Messages:           []gotato.Message{gotato.UserMessage(body)},
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

// Result reports a Compact call. Compaction is present only when a
// replacement actually happened (Replaced is true).
type Result struct {
	SessionID      string              `json:"session_id"`
	Replaced       bool                `json:"replaced"`
	MessagesBefore int                 `json:"messages_before"`
	MessagesAfter  int                 `json:"messages_after"`
	TokensBefore   int                 `json:"tokens_before"`
	TokensAfter    int                 `json:"tokens_after"`
	Compaction     *session.Compaction `json:"compaction,omitempty"`
}

// Compact permanently replaces the Messages before the last opts.Keep with
// one summary Message, records a session.Compaction, and records a
// session_compacted Event in the Session. It must not run while a Run is
// using the Session (AutoCompact runs it at the sanctioned point). It returns
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
		tokens := EstimateTokens(messages)
		return Result{
			SessionID:      s.ID(),
			MessagesBefore: len(messages),
			MessagesAfter:  len(messages),
			TokensBefore:   tokens,
			TokensAfter:    tokens,
		}, nil
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
	return Result{
		SessionID: s.ID(), Replaced: true, Compaction: &record,
		MessagesBefore: len(messages), MessagesAfter: len(replaced),
		TokensBefore: EstimateTokens(messages), TokensAfter: EstimateTokens(replaced),
	}, nil
}

// CompactPolicy is a token budget: when the Session's history is estimated
// above Ceiling at the start of a Run, it is compacted so that the retained
// tail is at most Floor tokens (plus the summary). Floor defaults to half of
// Ceiling. A zero Ceiling disables the policy.
type CompactPolicy struct {
	Ceiling    int
	Floor      int
	Summarizer Summarizer
	Now        func() time.Time
}

// AutoCompactor is a gotato.RunPreparer that applies a CompactPolicy to one
// Session. Install it with gotato.WithExtension next to WithTranscript(s).
type AutoCompactor struct {
	session *session.Session
	policy  CompactPolicy
	last    Result
	runs    int
}

// AutoCompact creates an AutoCompactor.
func AutoCompact(s *session.Session, policy CompactPolicy) *AutoCompactor {
	if policy.Floor <= 0 || policy.Floor >= policy.Ceiling {
		policy.Floor = policy.Ceiling / 2
	}
	return &AutoCompactor{session: s, policy: policy}
}

// PrepareRun implements gotato.RunPreparer.
func (a *AutoCompactor) PrepareRun(ctx context.Context, _ gotato.Transcript) error {
	if a.session == nil || a.policy.Ceiling <= 0 {
		return nil
	}
	messages := a.session.Messages()
	if EstimateTokens(messages) <= a.policy.Ceiling {
		return nil
	}
	keep := tailWithinBudget(messages, a.policy.Floor)
	result, err := Compact(ctx, a.session, CompactOptions{Keep: keep, Summarizer: a.policy.Summarizer, Now: a.policy.Now})
	if err != nil {
		return err
	}
	if result.Replaced {
		a.last = result
		a.runs++
	}
	return nil
}

// Last returns the most recent automatic compaction and how many happened.
func (a *AutoCompactor) Last() (Result, int) { return a.last, a.runs }

// tailWithinBudget returns how many trailing Messages fit in budget tokens,
// at least one.
func tailWithinBudget(messages []gotato.Message, budget int) int {
	keep := 0
	total := 0
	for i := len(messages) - 1; i >= 0; i-- {
		size := EstimateTokens(messages[i : i+1])
		if keep > 0 && total+size > budget {
			break
		}
		total += size
		keep++
	}
	if keep == 0 {
		keep = 1
	}
	return keep
}

// safeCut returns the first index >= want that starts a user Message, or 0
// when want <= 0. A cut at a user Message never separates a tool call from
// its results. When no user Message follows want, the last user Message before
// it is used so the retained tail is always a valid sequence.
func safeCut(messages []gotato.Message, want int) int {
	if want <= 0 {
		return 0
	}
	if want >= len(messages) {
		want = len(messages)
	}
	for i := want; i < len(messages); i++ {
		if messages[i].Role == gotato.RoleUser && !isSummary(messages[i]) {
			return i
		}
	}
	for i := want - 1; i > 0; i-- {
		if messages[i].Role == gotato.RoleUser && !isSummary(messages[i]) {
			return i
		}
	}
	return 0
}

func isSummary(message gotato.Message) bool {
	for _, part := range message.Parts {
		if part.Metadata[MetadataCompaction] == "summary" {
			return true
		}
	}
	return false
}

func tagSummary(summary gotato.Message) gotato.Message {
	summary = summary.Clone()
	if summary.Role == "" {
		summary.Role = gotato.RoleUser
	}
	if len(summary.Parts) == 0 {
		summary.Parts = []gotato.ContentPart{{Kind: gotato.ContentText, Text: "(empty summary)"}}
	}
	for i := range summary.Parts {
		if summary.Parts[i].Metadata == nil {
			summary.Parts[i].Metadata = map[string]string{}
		}
		summary.Parts[i].Metadata[MetadataCompaction] = "summary"
	}
	return summary
}
