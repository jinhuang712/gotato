package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

// RecapWriter turns a run of Messages into one summary Message. The Message it
// returns should be user-role text; Compact tags it as a compaction summary.
type RecapWriter interface {
	Name() string
	WriteRecap(context.Context, []gotato.Message) (gotato.Message, error)
}

// TruncateRecap is the deterministic default: it lists the text of the
// Messages by role and truncates to MaxChars (default 2000). It needs no
// Model, so it is safe in tests and CI.
type TruncateRecap struct {
	MaxChars int
}

// Name implements RecapWriter.
func (TruncateRecap) Name() string { return "truncate" }

// WriteRecap implements RecapWriter.
func (t TruncateRecap) WriteRecap(_ context.Context, messages []gotato.Message) (gotato.Message, error) {
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

// ModelRecap asks a Model to summarize. Instruction defaults to a
// concise, faithful summary prompt.
type ModelRecap struct {
	Model       gotato.Model
	Instruction string
	Label       string
}

// Name implements RecapWriter.
func (m ModelRecap) Name() string {
	if m.Label != "" {
		return m.Label
	}
	return "model"
}

// WriteRecap implements RecapWriter.
func (m ModelRecap) WriteRecap(ctx context.Context, messages []gotato.Message) (gotato.Message, error) {
	if m.Model == nil {
		return gotato.Message{}, errors.New("modelctx: ModelRecap has no Model")
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
	if stream == nil {
		return gotato.Message{}, errors.New("modelctx: Model returned a nil stream")
	}
	defer func() { _ = stream.Close() }()
	var b strings.Builder
	completed := false
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
			completed = true
			break
		}
	}
	if !completed {
		return gotato.Message{}, errors.New("modelctx: model stream ended before ModelDone")
	}
	summary := strings.TrimSpace(b.String())
	if summary == "" {
		return gotato.Message{}, errors.New("modelctx: model returned an empty summary")
	}
	return gotato.UserMessage("Summary of earlier conversation:\n" + summary), nil
}

// CompactOptions controls Compact.
type CompactOptions struct {
	// Keep is the number of most recent Messages to keep in the tail. The cut
	// moves forward to a Turn boundary so a tool call keeps its result. With
	// Keep and Floor both 0, the tail is the latest Run's Turns, up to
	// SegmentSettings.TailTokens (20K tokens) and at most a quarter of Window,
	// or of Ceiling when there is no window.
	Keep int
	// Floor is the most tokens the kept tail holds, as an alternative to Keep.
	// Keep wins when both are set.
	Floor int
	// MinTokens skips the compaction when the stretch since the last
	// Checkpoint is estimated below it. 0 always compacts.
	MinTokens int
	// Window is the Model's context window in tokens, 0 when unknown, and
	// Ceiling the ceiling in force, 0 for none. They cap the default tail.
	Window  int
	Ceiling int
	// Recap writes the Recap that ends the Segment; nil means the Segment has
	// none. A Recap that fails leaves the Segment without one.
	Recap RecapWriter
	// DropReasoning leaves the reasoning out of the Messages Compact keeps,
	// for a Model whose provider is sent only the current Round's reasoning
	// (see DropsReasoning). The Segment changes the prefix that reasoning
	// was written under, and Claude checks that prefix: sent again, the
	// reasoning could be refused. Leave it false for a provider that needs
	// every reasoning artifact back, such as DeepSeek.
	DropReasoning bool
	// Now overrides the clock (tests).
	Now func() time.Time
	// Why says why the compaction runs, for its Events: on demand (the
	// default), past the ceiling, or before the Session leaves memory.
	Why gotato.CompactionWhy
	// SegmentSettings are the limits and the time zone of the Segment.
	SegmentSettings
}

// RoundReasoner is implemented by a Model that sends its provider only the
// reasoning of the current Round: the replies after the last user Message.
// The gateway's Client implements it, and returns true for a recognized
// Claude or OpenAI provider.
type RoundReasoner interface {
	RoundReasoningOnly() bool
}

// DropsReasoning reports whether model is a RoundReasoner whose provider is
// sent only the current Round's reasoning, so compaction should leave the
// reasoning out of the Messages it keeps. service.Runner sets DropReasoning
// from it for every Agent.
func DropsReasoning(model gotato.Model) bool {
	reasoner, ok := model.(RoundReasoner)
	return ok && reasoner.RoundReasoningOnly()
}

// Why a compaction closed nothing, in Result.Reason.
const (
	// ReasonBelowMinTokens: the stretch since the last Checkpoint is below
	// CompactOptions.MinTokens.
	ReasonBelowMinTokens = "below_min_tokens"
	// ReasonNothingToClose: the kept tail covers the whole stretch.
	ReasonNothingToClose = "nothing_to_close"
)

// Result reports a Compact call. Compaction is present only when a Segment
// was closed (Replaced is true); Reason says why not otherwise.
type Result struct {
	SessionID      string              `json:"session_id"`
	Replaced       bool                `json:"replaced"`
	Reason         string              `json:"reason,omitempty"`
	MessagesBefore int                 `json:"messages_before"`
	MessagesAfter  int                 `json:"messages_after"`
	TokensBefore   int                 `json:"tokens_before"`
	TokensAfter    int                 `json:"tokens_after"`
	Compaction     *session.Compaction `json:"compaction,omitempty"`
}

// ErrOldSummary is what Compact returns for a Record whose last Checkpoint
// holds one summary written before Segments existed. Compact does not turn it
// into a Segment, and nothing migrates it. It carries gotato.ErrInvalidState,
// so a caller finds it with errors.Is(err, ErrOldSummary) or gotato.IsCode.
var ErrOldSummary = gotato.ErrorOf(gotato.ErrInvalidState, "modelctx: the Session's last Checkpoint holds one summary written before Segments; Compact does not compact it")

// closeSegment appends a Segment and its Checkpoint in one step. A test
// replaces it to make the write fail.
var closeSegment = (*session.Record).CloseSegmentAndCheckpoint

// causeOf names the code of a failure for the Event that reports it.
func causeOf(err error) gotato.ErrorCode {
	var runtime *gotato.RuntimeError
	switch {
	case errors.As(err, &runtime) && runtime != nil:
		return runtime.Code
	case errors.Is(err, context.DeadlineExceeded):
		return gotato.ErrDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return gotato.ErrCancelled
	}
	return gotato.ErrStoreFailure
}

// Compact closes everything since the last Checkpoint, less a kept tail, into
// a Segment built by code: the user's Messages and each Run's final reply word
// for word, one line per tool call, and the Recap opts.Recap writes, if any.
// It appends the Segment (a segment_closed Entry) and the Checkpoint that
// lists it (the compaction_ended Event) in one step, between a
// compaction_started and, when the write fails, a compaction_failed. The
// Messages it closes stay in the store, and a load starts after them. It must
// not run while a Run is using the Session (AutoCompact runs it at the
// sanctioned point).
//
// The kept tail starts at a Turn boundary, so a tool call is never separated
// from its result (see CompactOptions). The ToolSets the Segment used and the
// kept tail calls stay active; the others are switched off.
//
// A Recap that fails leaves the Segment without one (Compaction.Recap is
// "none"). A stretch below opts.MinTokens, or one the tail covers whole, is
// left as it is and returns a Result with Replaced false and a Reason. A
// Record whose last Checkpoint holds one summary written before Segments is
// refused with ErrOldSummary.
func Compact(ctx context.Context, s *session.Record, opts CompactOptions) (Result, error) {
	if s == nil {
		return Result{}, errors.New("modelctx: session is nil")
	}
	all := s.Messages()
	if len(s.Segments()) == 0 && len(all) > 0 && isSummary(all[0]) {
		return Result{}, ErrOldSummary
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	stretch := s.Tail()
	skipped := func(reason string) Result {
		tokens := EstimateTokens(all)
		return Result{SessionID: s.ID(), Reason: reason, MessagesBefore: len(all), MessagesAfter: len(all), TokensBefore: tokens, TokensAfter: tokens}
	}
	if opts.MinTokens > 0 && EstimateTokens(stretch) < opts.MinTokens {
		return skipped(ReasonBelowMinTokens), nil
	}
	cut := tailStart(stretch, opts)
	if cut <= 0 {
		return skipped(ReasonNothingToClose), nil
	}
	why := opts.Why
	if why == "" {
		why = gotato.CompactionOnDemand
	}
	header := func() gotato.Header { return gotato.NewHeader(gotato.SessionID(s.ID()), "", 0) }
	s.Emit(gotato.CompactionStarted{Header: header(), Why: why})

	closed := stretch[:cut]
	recapName, recapText := "none", ""
	if opts.Recap != nil {
		message, err := opts.Recap.WriteRecap(ctx, closed)
		if ctxErr := ctx.Err(); ctxErr != nil {
			s.Emit(gotato.CompactionFailed{Header: header(), Why: why, Cause: causeOf(ctxErr)})
			return Result{}, ctxErr
		}
		// A Recap that fails costs the Segment its Recap, not the compaction.
		if text := strings.TrimSpace(gotato.TextOf(message)); err == nil && text != "" {
			recapName, recapText = opts.Recap.Name(), text
		}
	}

	tail := make([]gotato.Message, 0, len(stretch)-cut)
	for _, message := range stretch[cut:] {
		if opts.DropReasoning {
			message = withoutReasoning(message)
		}
		tail = append(tail, message)
	}
	active := s.ActiveToolSets()
	segment := buildSegment(s.NextSegment(), closed, recapText, opts.SegmentSettings, active, s.Position)
	kept := keptToolSets(active, segment.ToolSets, tail)

	before, _ := json.Marshal(all)
	// What the Record holds after: the Segments before, the new one, the tail.
	after := slices.Concat(all[:len(all)-len(stretch)], segment.Messages, tail)
	encoded, _ := json.Marshal(after)
	var earlier []int
	for _, previous := range s.Segments() {
		earlier = append(earlier, previous.Number)
	}
	record := session.Compaction{
		At:               now().UTC(),
		ReplacedMessages: cut,
		FromMessageID:    closed[0].ID,
		ToMessageID:      closed[cut-1].ID,
		Recap:            recapName,
		Segment:          segment.Number,
		TokensBefore:     EstimateTokens(all),
		TokensAfter:      EstimateTokens(after),
		BytesBefore:      len(before),
		BytesAfter:       len(encoded),
		Ceiling:          opts.Ceiling,
		ToolSets:         kept,
	}
	s.SetActiveToolSets(kept)
	if _, err := closeSegment(s, why, segment, earlier, tail, &record); err != nil {
		s.SetActiveToolSets(active)
		s.Emit(gotato.CompactionFailed{Header: header(), Why: why, Cause: causeOf(err)})
		return Result{}, err
	}
	return Result{
		SessionID: s.ID(), Replaced: true, Compaction: &record,
		MessagesBefore: len(all), MessagesAfter: len(after),
		TokensBefore: record.TokensBefore, TokensAfter: record.TokensAfter,
	}, nil
}

// keptToolSets returns the active ToolSets the closed Segment used or the kept
// tail calls, in the order they were switched on. A ToolSet the Agent installed
// active is not among the Record's, and stays on whatever this returns.
func keptToolSets(active, used []string, tail []gotato.Message) []string {
	out := []string{}
	for _, name := range active {
		if slices.Contains(used, name) || len(toolSetsCalled(tail, []string{name})) > 0 {
			out = append(out, name)
		}
	}
	return out
}

// CompactPolicy is a token budget: when the Session's history is estimated
// above Ceiling at the start of a Run, it is compacted. A zero Ceiling
// disables the policy. Floor, when above 0, is the most tokens the kept tail
// holds; with none, the tail is the latest Run's Turns (CompactOptions).
// Window is the Model's context window, 0 when unknown. DropReasoning is
// CompactOptions.DropReasoning for every compaction the policy makes.
type CompactPolicy struct {
	Ceiling       int
	Floor         int
	Window        int
	Recap         RecapWriter
	DropReasoning bool
	Now           func() time.Time
	SegmentSettings
}

// AutoCompactor is a gotato.RunPreparer that applies a CompactPolicy to one
// Session. Install it with gotato.WithExtension next to WithTranscript(s).
type AutoCompactor struct {
	session *session.Record
	policy  CompactPolicy
	last    Result
	runs    int
}

// AutoCompact creates an AutoCompactor.
func AutoCompact(s *session.Record, policy CompactPolicy) *AutoCompactor {
	return &AutoCompactor{session: s, policy: policy}
}

// PrepareRun implements gotato.RunPreparer. A compaction helps a Run but is
// not a step it needs, so one that cannot be written is dropped (its Events
// say so) and the Run goes on. A Record the compaction refuses, or a Run
// stopped during the compaction, ends the Run.
func (a *AutoCompactor) PrepareRun(ctx context.Context, _ gotato.Transcript) error {
	if a.session == nil || !a.due(a.session.Messages()) {
		return nil
	}
	result, err := Compact(ctx, a.session, CompactOptions{
		Floor: a.policy.Floor, Window: a.policy.Window, Ceiling: a.policy.Ceiling,
		Recap: a.policy.Recap, DropReasoning: a.policy.DropReasoning, Now: a.policy.Now,
		Why: gotato.CompactionPastCeiling, SegmentSettings: a.policy.SegmentSettings,
	})
	if err != nil {
		if errors.Is(err, ErrOldSummary) || ctx.Err() != nil {
			return err
		}
		return nil
	}
	if result.Replaced {
		a.last = result
		a.runs++
	}
	return nil
}

// Last returns the most recent automatic compaction and how many happened.
func (a *AutoCompactor) Last() (Result, int) { return a.last, a.runs }

// Pending reports whether the next Run compacts the Session before its first
// Turn, because the Session's estimated tokens are past the ceiling.
func (a *AutoCompactor) Pending() bool {
	return a.session != nil && a.due(a.session.Messages())
}

// Ceiling returns the estimated-token budget past which a Run compacts first;
// 0 means automatic compaction is off.
func (a *AutoCompactor) Ceiling() int { return max(a.policy.Ceiling, 0) }

// due is the one test PrepareRun and Pending share.
func (a *AutoCompactor) due(messages []gotato.Message) bool {
	return a.policy.Ceiling > 0 && EstimateTokens(messages) > a.policy.Ceiling
}

// tailStart returns the index in stretch where the kept tail starts; the
// Messages before it are closed into a Segment. 0 means nothing is closed.
//
// With Keep, the tail is the last Keep Messages. With Floor, it is the most
// recent Messages within Floor tokens. With neither, it is the latest Run's
// Turns within the default budget: 20K tokens, and no more than a quarter of
// the window, or of the ceiling when no window is known. Every cut moves
// forward to a Turn boundary.
func tailStart(stretch []gotato.Message, opts CompactOptions) int {
	switch {
	case opts.Keep > 0:
		return turnCut(stretch, len(stretch)-opts.Keep)
	case opts.Floor > 0:
		return turnCut(stretch, withinBudget(stretch, opts.Floor, 0))
	}
	budget := orDefault(opts.TailTokens, DefaultTailTokens)
	base := opts.Window
	if base <= 0 {
		base = opts.Ceiling
	}
	if base > 0 {
		budget = min(budget, base/4)
	}
	return turnCut(stretch, withinBudget(stretch, budget, runStart(stretch)))
}

// withinBudget returns the lowest index, not below floor, from which the
// Messages fit in budget tokens. It is len(messages) when even the last does
// not fit.
func withinBudget(messages []gotato.Message, budget, floor int) int {
	total := 0
	start := len(messages)
	for i := len(messages) - 1; i >= floor; i-- {
		total += EstimateTokens(messages[i : i+1])
		if total > budget {
			break
		}
		start = i
	}
	return start
}

// runStart returns the index of the first Message of the latest Run. Without
// Run IDs it is the last user Message.
func runStart(messages []gotato.Message) int {
	var latest gotato.RunID
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].RunID != "" {
			latest = messages[i].RunID
			break
		}
	}
	if latest == "" {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == gotato.RoleUser {
				return i
			}
		}
		return 0
	}
	// Messages with no Run after the Run's last stay in the tail; one before
	// its first is not part of it.
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].RunID == latest {
			start = i
		} else if start < len(messages) {
			break
		}
	}
	return start
}

// turnCut returns the first index >= want that starts a Turn: a Message that
// is not a tool result, so a tool call keeps its result. It is 0 when want is
// 0 or less, and len(messages) when no Turn starts at or after want.
func turnCut(messages []gotato.Message, want int) int {
	if want <= 0 {
		return 0
	}
	for i := want; i < len(messages); i++ {
		if messages[i].Role != gotato.RoleToolResult {
			return i
		}
	}
	return len(messages)
}

// withoutReasoning returns message without its reasoning Parts.
func withoutReasoning(message gotato.Message) gotato.Message {
	reasoning := func(part gotato.ContentPart) bool { return part.Kind == gotato.ContentReasoning }
	if !slices.ContainsFunc(message.Parts, reasoning) {
		return message
	}
	message = message.Clone()
	message.Parts = slices.DeleteFunc(message.Parts, reasoning)
	return message
}

func isSummary(message gotato.Message) bool {
	for _, part := range message.Parts {
		if part.Metadata[MetadataCompaction] == "summary" {
			return true
		}
	}
	return false
}
