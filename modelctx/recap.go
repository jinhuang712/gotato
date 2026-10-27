package modelctx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/gotato"
)

// DefaultRecapMaxTokens is the output limit of the model call that writes a
// Recap. A Recap that is cut off fails, so the limit is high; output not used
// costs nothing.
const DefaultRecapMaxTokens = 8192

// RecapRequest is what a RecapWriter is asked to summarize.
type RecapRequest struct {
	// Segment is the number of the Segment the Recap ends.
	Segment int
	// Closed are the Messages the Segment closes.
	Closed []gotato.Message
	// From is when the closed stretch began, zero when unknown, shown in
	// Location (UTC when nil).
	From     time.Time
	Location *time.Location
	// Context is the request the next model call would send for the Session
	// as it is now: the same instruction, tools, Messages and cache hints. A
	// writer that asks a model builds its request from it, so the provider
	// reads the prefix from its cache. Nil when the caller supplied none
	// (CompactConfig.Context).
	Context *gotato.ModelRequest
}

// RecapWriter writes the Recap that ends a Segment. A writer that fails, or
// returns no text, leaves the Segment without a Recap. The Message it returns
// carries the Recap as its text and, for a model call, the call's Usage.
type RecapWriter interface {
	Name() string
	WriteRecap(context.Context, RecapRequest) (gotato.Message, error)
}

// NoRecap writes none: the Segment is its conversation and its tool call
// lines. It is what a compaction does when no writer is set, so a model's
// Recap is always a choice.
type NoRecap struct{}

// Name implements RecapWriter.
func (NoRecap) Name() string { return "none" }

// WriteRecap implements RecapWriter.
func (NoRecap) WriteRecap(context.Context, RecapRequest) (gotato.Message, error) {
	return gotato.Message{}, nil
}

// TruncateRecap is the deterministic writer for tests and for use without a
// Model: it lists the text of the closed Messages by role and truncates to
// MaxChars (default 2000).
type TruncateRecap struct {
	MaxChars int
}

// Name implements RecapWriter.
func (TruncateRecap) Name() string { return "truncate" }

// WriteRecap implements RecapWriter.
func (t TruncateRecap) WriteRecap(_ context.Context, request RecapRequest) (gotato.Message, error) {
	messages := request.Closed
	limit := t.MaxChars
	if limit <= 0 {
		limit = 2000
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Summary of earlier conversation (%d messages):\n", len(messages))
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
		fmt.Fprintf(&b, "- %s: %s\n", message.Role, line)
	}
	text := b.String()
	if len(text) > limit {
		// Cut on a rune boundary: text[:limit] can split a multi-byte rune,
		// and json.Marshal would then turn the invalid bytes into U+FFFD.
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return gotato.UserMessage(text), nil
}

// ModelRecap is the Agent's model writing the Recap. It sends the Context the
// next call would send, so the provider's cache is read, followed by one plain
// user message that asks for the Recap. That message is never stored. The
// reply must pass ValidateRecap. A Recap from a model is a choice a team
// makes: no default turns it on.
type ModelRecap struct {
	// Model writes the Recap. A Runner fills it with the Agent's model when
	// it is nil.
	Model gotato.Model
	// MaxTokens is the output limit; 0 means DefaultRecapMaxTokens.
	MaxTokens uint32
	// Label is the writer's name in the compaction's record; the default is
	// "model".
	Label string
}

// Name implements RecapWriter.
func (m ModelRecap) Name() string {
	if m.Label != "" {
		return m.Label
	}
	return "model"
}

// RecapModelRequest returns the request that asks for a Recap: the Context of
// request, unchanged, and one user message after it. It is a copy; request is
// not modified. Only the output limit differs from the Context, since a Recap
// needs room; the instruction, tools, Messages, cache hints and key are the
// next call's own, byte for byte.
func RecapModelRequest(request RecapRequest, maxTokens uint32) (gotato.ModelRequest, error) {
	if request.Context == nil {
		return gotato.ModelRequest{}, ErrNoRecapContext
	}
	out := *request.Context
	out.Messages = make([]gotato.Message, 0, len(request.Context.Messages)+1)
	for _, message := range request.Context.Messages {
		out.Messages = append(out.Messages, message.Clone())
	}
	out.Messages = append(out.Messages, gotato.UserMessage(RecapAsk(request)))
	if maxTokens == 0 {
		maxTokens = DefaultRecapMaxTokens
	}
	out.Config.MaxTokens = maxTokens
	return out, nil
}

// The five headings of a Recap, in the order the message asks for them.
var recapHeadings = []string{"What happened", "Key facts", "Decisions", "Open items", "Where it stands"}

// RecapAsk is the message that asks for a Recap (FEATURES.md → G-F06). It is a
// request for work, so it carries no tag, and it is never stored.
func RecapAsk(request RecapRequest) string {
	loc := request.Location
	if loc == nil {
		loc = time.UTC
	}
	span := "the messages after the last segment"
	if !request.From.IsZero() {
		span += ", from " + request.From.In(loc).Format("2006-01-02 15:04")
	}
	number := "this segment"
	if request.Segment > 0 {
		number = fmt.Sprintf("segment %d", request.Segment)
	}
	return fmt.Sprintf(`Write a recap of %s
until now. It closes this stretch of the conversation as %s. The
user's messages, your final replies and one line per tool call are kept next
to it; your reasoning, your intermediate steps and all tool results are not.

Write in the conversation's language, as short bullet points, under these
headings. Leave out a heading that has nothing under it.

%s

Leave out the instructions, the tool list and the environment; they are
sent separately. Do not call tools. Do not address the user. Start with the
first heading.`, span, number, recapHeadingBlock)
}

// recapHeadingBlock is the list of the five headings that every ask for a
// Recap carries.
const recapHeadingBlock = `## What happened     what the user asked for, and what was done
## Key facts         what the tools returned that still matters; copy IDs,
                     numbers, names, times and error codes exactly
## Decisions         what the user or you decided, and why
## Open items        what was promised, asked for, or left unfinished
## Where it stands   only while a task is still running: the step it is on
                     and the next one`

// mergeRecapAsk is the message that asks for the Recap of a merge: one Recap of
// the Recaps of the oldest Segments, under the same rules, and which numbers
// read_segment still reads (FEATURES.md → G-F06, Many Segments). The Recaps
// follow it, each with the times it covers. A Segment that has no Recap
// contributes the digest of its conversation and tool calls instead. When the
// first is a merged Segment, its Recap comes first and is the far past. Like
// RecapAsk it is a request for work, so it carries no tag, and it is never
// stored.
func mergeRecapAsk(request MergeRequest) string {
	loc := request.Location
	if loc == nil {
		loc = time.UTC
	}
	label := rangeLabel(request.Numbers())
	var b strings.Builder
	fmt.Fprintf(&b, `Write one recap of the recaps below. They close the oldest stretch of the
conversation, segments %[1]s, and your recap takes their place. The user's
messages, your final replies and the tool results of those segments stay in
the record, and read_segment(n) returns segment n in full, for n in %[1]s.
Only your recap is kept in this conversation, so it must carry what a later
question needs.

Write in the conversation's language, as short bullet points, under these
headings. Leave out a heading that has nothing under it.

%[2]s

Merge what repeats, copy IDs, numbers, names, times and error codes exactly
as the recaps give them, and drop what a later recap says is done or
replaced. Keep what is still open. Do not call tools. Do not address the user.
Start with the first heading.
`, label, recapHeadingBlock)
	for _, segment := range request.Segments {
		name := "segment " + rangeLabel(segment.Numbers())
		if len(segment.Merged) > 0 {
			name += ", merged earlier, the far past"
		}
		if !segment.From.IsZero() {
			name += ", " + segment.From.In(loc).Format("2006-01-02 15:04")
			if !segment.To.IsZero() {
				name += " to " + segment.To.In(loc).Format("2006-01-02 15:04")
			}
		}
		recap := strings.TrimSpace(segment.Recap)
		if recap == "" {
			name += ", no recap: its conversation and tool calls"
			recap = mergeDigest([]gotato.Segment{segment}, loc)
		}
		fmt.Fprintf(&b, "\n--- recap of %s ---\n%s\n--- end ---\n", name, recap)
	}
	return b.String()
}

// ValidateRecap checks a Recap's text: it starts with one of the five
// headings, uses no other `## ` heading, and has `## What happened`. A heading
// with nothing under it is fine. It returns the text trimmed.
func ValidateRecap(text string) (string, error) {
	text = strings.TrimSpace(text)
	heading := func(line string) (string, bool) {
		rest, ok := strings.CutPrefix(line, "## ")
		return strings.TrimSpace(rest), ok
	}
	known := func(name string) bool { return slices.Contains(recapHeadings, name) }
	first, _, _ := strings.Cut(text, "\n")
	if name, ok := heading(first); !ok || !known(name) {
		return "", fmt.Errorf("%w: it does not start with one of the five headings", ErrBadRecap)
	}
	happened := false
	for line := range strings.SplitSeq(text, "\n") {
		name, ok := heading(line)
		if !ok {
			continue
		}
		if !known(name) {
			return "", fmt.Errorf("%w: unknown heading %q", ErrBadRecap, name)
		}
		happened = happened || name == recapHeadings[0]
	}
	if !happened {
		return "", fmt.Errorf("%w: no ## What happened", ErrBadRecap)
	}
	return text, nil
}

// WriteRecap implements RecapWriter. It returns the Recap as an assistant
// Message holding the model call's usage. When the call was made but its reply
// fails the rules, the error comes with a Message that holds the usage only.
func (m ModelRecap) WriteRecap(ctx context.Context, recap RecapRequest) (gotato.Message, error) {
	if m.Model == nil {
		return gotato.Message{}, errors.New("modelctx: ModelRecap has no Model")
	}
	request, err := RecapModelRequest(recap, m.MaxTokens)
	if err != nil {
		return gotato.Message{}, err
	}
	return m.complete(ctx, request)
}

// mergeRecapModelRequest returns the request that asks for the Recap of a
// merge: one user message, the ask with the Recaps of the Segments merged
// below it. It holds no instruction, tools or earlier Messages, so it stays
// small and cannot be refused as too long when the Context was.
func mergeRecapModelRequest(request MergeRequest, maxTokens uint32) gotato.ModelRequest {
	if maxTokens == 0 {
		maxTokens = DefaultRecapMaxTokens
	}
	return gotato.ModelRequest{
		Messages: []gotato.Message{gotato.UserMessage(mergeRecapAsk(request))},
		Config:   gotato.ModelConfig{MaxTokens: maxTokens},
	}
}

// WriteMergeRecap implements MergeRecapWriter: the Agent's model writes one
// Recap from the Recaps of the oldest Segments alone, under the rules of
// ValidateRecap.
func (m ModelRecap) WriteMergeRecap(ctx context.Context, request MergeRequest) (gotato.Message, error) {
	if m.Model == nil {
		return gotato.Message{}, errors.New("modelctx: ModelRecap has no Model")
	}
	return m.complete(ctx, mergeRecapModelRequest(request, m.MaxTokens))
}

// complete sends request and reads the Recap from the reply.
func (m ModelRecap) complete(ctx context.Context, request gotato.ModelRequest) (gotato.Message, error) {
	stream, err := m.Model.Stream(ctx, request)
	if err != nil {
		return gotato.Message{}, err
	}
	if stream == nil {
		return gotato.Message{}, errors.New("modelctx: Model returned a nil stream")
	}
	defer func() { _ = stream.Close() }()
	var (
		b          strings.Builder
		usage      gotato.Usage
		stop       gotato.StopReason
		calledTool bool
		completed  bool
	)
	for !completed {
		event, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return gotato.Message{}, err
		}
		switch event.Kind {
		case gotato.ModelEventKindTextDelta:
			b.WriteString(event.Text)
		case gotato.ModelEventKindToolCall:
			calledTool = true
		case gotato.ModelEventKindUsage:
			usage = event.Usage
		case gotato.ModelEventKindDone:
			completed = true
			stop = event.StopReason
			if event.Usage.TotalTokens != 0 || event.Usage.InputTokens != 0 || event.Usage.OutputTokens != 0 {
				usage = event.Usage
			}
		}
	}
	if !completed {
		return gotato.Message{}, errors.New("modelctx: model stream ended before ModelEventKindDone")
	}
	spent := gotato.Message{Role: gotato.RoleAssistant, Usage: usage}
	switch {
	case calledTool || stop == gotato.StopReasonToolCalls:
		return spent, fmt.Errorf("%w: the reply called a tool", ErrBadRecap)
	case stop == gotato.StopReasonMaxTokens:
		return spent, fmt.Errorf("%w: the reply was cut off at the output limit", ErrBadRecap)
	}
	text, err := ValidateRecap(b.String())
	if err != nil {
		return spent, err
	}
	reply := gotato.AssistantMessage(text)
	reply.Usage = usage
	return reply, nil
}
