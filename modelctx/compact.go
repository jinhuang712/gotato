package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// CompactConfig controls Compact.
type CompactConfig struct {
	// CompactPolicy holds what a Compact and an automatic compaction share:
	// the window and the ceiling, which cap the tail that TailTokens sizes,
	// the Recap writer (nil means the Segment has none; a Recap that fails
	// leaves the Segment without one), DropReasoning, the Segments' budget
	// and the SegmentSettings. Compact reads the ceiling in force
	// (EffectiveCeilingTokens).
	CompactPolicy
	// KeepMessages is the number of most recent Messages to keep in the tail.
	// The cut moves forward to a Turn boundary so a tool call keeps its
	// result. With KeepMessages 0, the tail is the latest Run's Turns, up to
	// SegmentSettings.TailTokens (20K tokens by default) and at most a quarter
	// of WindowTokens, or of the ceiling in force when there is no window.
	KeepMessages int
	// MinTokens skips the compaction when the stretch since the last
	// Checkpoint is estimated below it. 0 always compacts.
	MinTokens int
}

// compactCall is one call of Compact: the CompactConfig and the four things
// only a caller that runs a compaction inside a Run or a Session's life knows.
// AutoCompactor sets all four; service sets the first and the last through
// CompactOption.
type compactCall struct {
	CompactConfig
	// context builds the request the next model call would send for the
	// Session as it is now, which a Recap written by a model sends as its
	// prefix. Without it, a ModelRecap fails and the Segment has no Recap.
	context func(context.Context) (gotato.ModelRequest, error)
	// keepLastTurn keeps at least the latest Turn of run in the tail,
	// whatever the budget: the Run's last reply and everything after it (its
	// tool results, a notice, a steer, an interrupt), or on the Run's first
	// Turn all of its Messages, the prompt among them. A compaction in the
	// middle of a Run needs it, since the model call that follows continues
	// from that Turn.
	keepLastTurn bool
	// run is the Run in flight, whose latest Turn keepLastTurn keeps.
	run gotato.RunID
	// currentRun is the Run that is starting. Its Messages are not "the latest
	// Run" when the default tail is chosen, so the Run before it stays as it
	// was, and the new Run's Messages follow in the tail.
	currentRun gotato.RunID
	// why says why the compaction runs, for its Events: on demand (the
	// default), past the ceiling, on an overflow, or before the Session
	// leaves memory.
	why gotato.CompactionWhy
}

// CompactOption sets something about a Compact call that the CompactConfig
// does not hold: what only the caller that knows the Session's life can say.
type CompactOption func(*compactCall)

// WithRecapContext gives Compact the func that builds the request the next
// model call would send for the Session as it is now, which a Recap written
// by a model (ModelRecap) sends as its prefix, so the provider's cache is
// read. service.Runner sets it. Without it, a ModelRecap fails and the
// Segment has no Recap (Compaction.RecapError says so).
func WithRecapContext(build func(context.Context) (gotato.ModelRequest, error)) CompactOption {
	return func(c *compactCall) { c.context = build }
}

// WithCompactionWhy says why the compaction runs, for its Events: on demand
// (the default), past the ceiling, on an overflow, or before the Session
// leaves memory.
func WithCompactionWhy(why gotato.CompactionWhy) CompactOption {
	return func(c *compactCall) { c.why = why }
}

// CompactReason says why a compaction closed nothing, in Result.Reason.
type CompactReason string

const (
	// CompactReasonBelowMinTokens means the stretch since the last Checkpoint is
	// below CompactConfig.MinTokens.
	CompactReasonBelowMinTokens CompactReason = "below_min_tokens"
	// CompactReasonNothingToClose means the kept tail covers the whole stretch.
	CompactReasonNothingToClose CompactReason = "nothing_to_close"
)

// Result reports a Compact call. Compaction is present only when a Segment
// was closed (Replaced is true); Reason says why not otherwise.
type Result struct {
	SessionID      string             `json:"session_id"`
	Replaced       bool               `json:"replaced"`
	Reason         CompactReason      `json:"reason,omitempty"`
	MessagesBefore int                `json:"messages_before"`
	MessagesAfter  int                `json:"messages_after"`
	TokensBefore   int                `json:"tokens_before"`
	TokensAfter    int                `json:"tokens_after"`
	Compaction     *gotato.Compaction `json:"compaction,omitempty"`
	// Merge reports the merge of the oldest Segments that followed the Segment's
	// closing, when the Segments passed their budget: merged, or why not
	// (MergeReasonRecapFailed, MergeReasonWriteFailed). It is absent when the
	// Segments fit their budget.
	Merge *MergeResult `json:"merge,omitempty"`
}

// closeSegment appends a Segment and its Checkpoint in one step. A test
// replaces it to make the write fail.
var closeSegment = (*session.Record).CloseSegmentAndCheckpoint

// Compact closes everything since the last Checkpoint, less a kept tail, into
// a Segment built by code: the user's Messages and each Run's final reply word
// for word, one line per tool call, and the Recap cfg.Recap writes, if any.
// It appends the Segment (a segment_closed Entry) and the Checkpoint that
// lists it (the compaction_ended Event) in one step, between a
// compaction_started and, when the write fails, a compaction_failed. The
// Messages it closes stay in the store, and a load starts after them. It must
// not run while a Run is using the Session (AutoCompact runs it at the
// sanctioned point).
//
// The kept tail starts at a Turn boundary, so a tool call is never separated
// from its result (see CompactConfig). The loaded Tools the Segment called and
// the kept tail calls stay loaded; the others are unloaded, each with a
// tool_unloaded Event (why: compaction).
//
// A Recap that fails leaves the Segment without one (Compaction.Recap is
// "none" and Compaction.RecapError says why; the usage of a model call that
// was made is still recorded). A stretch below cfg.MinTokens, or one the tail
// covers whole, is left as it is and returns a Result with Replaced false and
// a Reason. A cfg.Recap that cannot work as set (CompactPolicy.Validate) is an
// error before anything is compacted.
func Compact(ctx context.Context, s *session.Record, cfg CompactConfig, opts ...CompactOption) (Result, error) {
	call := compactCall{CompactConfig: cfg}
	for _, opt := range opts {
		opt(&call)
	}
	return runCompact(ctx, s, call)
}

// runCompact is Compact with the caller's options already applied.
func runCompact(ctx context.Context, s *session.Record, call compactCall) (Result, error) {
	if s == nil {
		return Result{}, errors.New("modelctx: session is nil")
	}
	if err := call.Validate(); err != nil {
		return Result{}, err
	}
	cfg := call.CompactConfig
	all := s.Messages()
	stretch := s.Tail()
	if cfg.MinTokens > 0 && EstimateTokens(stretch) < cfg.MinTokens {
		return skippedResult(s, all, CompactReasonBelowMinTokens), nil
	}
	cut := tailStart(stretch, call)
	if cut <= 0 {
		return skippedResult(s, all, CompactReasonNothingToClose), nil
	}
	why := call.why
	if why == "" {
		why = gotato.CompactionWhyOnDemand
	}
	s.Emit(gotato.CompactionStarted{Header: sessionHeader(s), Why: why})

	closed := stretch[:cut]
	recap, err := writeRecap(ctx, s, call, closed, why)
	if err != nil {
		return Result{}, err
	}

	tail := keptTail(stretch[cut:], cfg.DropReasoning)
	loaded := s.LoadedTools()
	segment := buildSegment(s.NextSegment(), closed, recap.text, cfg.SegmentSettings, loaded, s.Position)
	kept := keptTools(loaded, segment.LoadedTools, tail)

	before, _ := json.Marshal(all)
	// What the Record holds after: the Segments before, the new one, the tail.
	after := slices.Concat(all[:len(all)-len(stretch)], segment.Messages, tail)
	encoded, _ := json.Marshal(after)
	var earlier []int
	for _, previous := range s.Segments() {
		earlier = append(earlier, previous.Number)
	}
	record := gotato.Compaction{
		At:               time.Now().UTC(),
		ReplacedMessages: cut,
		FromMessageID:    closed[0].ID,
		ToMessageID:      closed[cut-1].ID,
		Recap:            recap.name,
		RecapError:       recap.failure,
		RecapUsage:       recap.usage,
		Segment:          segment.Number,
		TokensBefore:     EstimateTokens(all),
		TokensAfter:      EstimateTokens(after),
		BytesBefore:      len(before),
		BytesAfter:       len(encoded),
		CeilingTokens:    cfg.EffectiveCeilingTokens(),
		LoadedTools:      kept,
	}
	s.SetLoadedTools(kept)
	if _, err := closeSegment(s, why, segment, earlier, tail, &record); err != nil {
		s.SetLoadedTools(loaded)
		s.Emit(gotato.CompactionFailed{Header: sessionHeader(s), Why: why, Cause: causeOf(err)})
		return Result{}, err
	}
	for _, id := range loaded {
		if !slices.Contains(kept, id) {
			s.Emit(gotato.ToolUnloaded{Header: sessionHeader(s), ToolID: id, Why: gotato.ToolUnloadedWhyCompaction})
		}
	}
	result := Result{
		SessionID: s.ID(), Replaced: true, Compaction: &record,
		MessagesBefore: len(all), MessagesAfter: len(after),
		TokensBefore: record.TokensBefore, TokensAfter: record.TokensAfter,
	}
	return mergeAfterClose(ctx, s, cfg, why, result)
}

// mergeAfterClose merges the oldest Segments, in a Checkpoint of their own,
// if the Segment just closed took them past their budget. A merge that fails
// leaves the Segment just closed in place, and the Segments unmerged.
func mergeAfterClose(ctx context.Context, s *session.Record, cfg CompactConfig, why gotato.CompactionWhy, result Result) (Result, error) {
	merge, err := mergeOldest(ctx, s, MergeConfig{
		BudgetTokens: cfg.SegmentBudgetTokens, CeilingTokens: cfg.EffectiveCeilingTokens(), Recap: cfg.Recap, Why: why, Location: cfg.Location,
	})
	if err != nil {
		return result, err
	}
	if merge.Merged || merge.Error != "" {
		result.Merge = &merge
		merged := s.Messages()
		result.MessagesAfter, result.TokensAfter = len(merged), EstimateTokens(merged)
	}
	return result, nil
}

// skippedResult is the Result of a Compact that closes nothing, for reason.
func skippedResult(s *session.Record, all []gotato.Message, reason CompactReason) Result {
	tokens := EstimateTokens(all)
	return Result{SessionID: s.ID(), Reason: reason, MessagesBefore: len(all), MessagesAfter: len(all), TokensBefore: tokens, TokensAfter: tokens}
}

// recapSkippedOverflow is the Compaction.RecapError of a compaction that ran
// because the provider refused a request as too long, and whose Recap needs the
// Context: that request would be refused again, so the writer is not asked.
const recapSkippedOverflow = "skipped_overflow"

// recapEmpty is the Compaction.RecapError of a writer that returned no text.
const recapEmpty = "empty_recap"

// recapOutcome is what writeRecap found: the writer's name and the text,
// "none" and empty when there is no Recap, the usage of the model call if one
// was made, and failure, the reason a Recap that was asked for is missing
// (empty when there is no writer).
type recapOutcome struct {
	name, text, failure string
	usage               *gotato.Usage
}

// recapCause names a Recap writer's failure for Compaction.RecapError: its
// code, or model_failure for an error that has none.
func recapCause(err error) string {
	var runtime *gotato.RuntimeError
	switch {
	case errors.As(err, &runtime) && runtime != nil:
		return string(runtime.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return string(gotato.CodeDeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return string(gotato.CodeCanceled)
	}
	return string(gotato.CodeModelFailure)
}

// writeRecap asks the Recap writer for the Recap of the closed Messages. A
// Recap that fails costs the Segment its Recap, not the compaction, and the
// outcome says why; only a canceled ctx is an error, after a
// compaction_failed Event.
func writeRecap(ctx context.Context, s *session.Record, call compactCall, closed []gotato.Message, why gotato.CompactionWhy) (recapOutcome, error) {
	out := recapOutcome{name: "none"}
	if call.Recap == nil {
		return out, nil
	}
	request := RecapRequest{Segment: s.NextSegment(), Closed: closed, From: stretchStart(closed, s.Position), Location: call.Location}
	var contextErr error
	if call.context != nil {
		var built gotato.ModelRequest
		if built, contextErr = guardedCall(ctx, call.context); contextErr == nil {
			request.Context = &built
		}
	}
	var message gotato.Message
	var writeErr error
	if contextErr == nil {
		message, writeErr = guardedCall2(ctx, call.Recap.WriteRecap, request)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		s.Emit(gotato.CompactionFailed{Header: sessionHeader(s), Why: why, Cause: causeOf(ctxErr)})
		return recapOutcome{}, ctxErr
	}
	if message.Usage != (gotato.Usage{}) {
		u := message.Usage
		out.usage = &u
	}
	text := strings.TrimSpace(gotato.TextOf(message))
	switch {
	case contextErr != nil:
		out.failure = recapCause(contextErr)
	case errors.Is(writeErr, ErrNoRecapContext) && why == gotato.CompactionWhyOverflow:
		out.failure = recapSkippedOverflow
	case writeErr != nil:
		out.failure = recapCause(writeErr)
	case text == "":
		out.failure = recapEmpty
	default:
		out.name, out.text = call.Recap.Name(), text
	}
	return out, nil
}

// keptTail copies the Messages the Context keeps whole after a compaction.
func keptTail(messages []gotato.Message, dropReasoning bool) []gotato.Message {
	tail := make([]gotato.Message, 0, len(messages))
	for _, message := range messages {
		if dropReasoning {
			message = withoutReasoning(message)
		}
		// The usage a reply reported was for the request that Context no
		// longer is, so it no longer counts toward the ceiling.
		message.Usage = gotato.Usage{}
		tail = append(tail, message)
	}
	return tail
}

// keptTools returns the loaded Tools the closed Segment called or the kept
// tail calls, in the order they were loaded. A Tool of a ToolSet the Agent
// installed active is not among the Record's, and stays visible whatever this
// returns.
func keptTools(loaded, used []string, tail []gotato.Message) []string {
	out := []string{}
	for _, id := range loaded {
		if slices.Contains(used, id) || len(toolsCalled(tail, []string{id})) > 0 {
			out = append(out, id)
		}
	}
	return out
}

// withoutReasoning returns message without its reasoning Parts.
func withoutReasoning(message gotato.Message) gotato.Message {
	reasoning := func(part gotato.ContentPart) bool { return part.Kind == gotato.ContentKindReasoning }
	if !slices.ContainsFunc(message.Parts, reasoning) {
		return message
	}
	message = message.Clone()
	message.Parts = slices.DeleteFunc(message.Parts, reasoning)
	return message
}

// tailStart returns the index in stretch where the kept tail starts; the
// Messages before it are closed into a Segment. 0 means nothing is closed.
//
// With KeepMessages, the tail is the last KeepMessages Messages. Without, it
// is the latest Run's Turns within TailTokens (20K tokens by default), and no
// more than a quarter of the window, or of the ceiling when no window is
// known. Every cut moves forward to a Turn boundary.
func tailStart(stretch []gotato.Message, cfg compactCall) int {
	cut := 0
	switch {
	case cfg.KeepMessages > 0:
		cut = turnCut(stretch, len(stretch)-cfg.KeepMessages)
	default:
		budget := orDefault(cfg.TailTokens, defaultTailTokens)
		base := cfg.WindowTokens
		if base <= 0 {
			base = cfg.EffectiveCeilingTokens()
		}
		if base > 0 {
			budget = min(budget, base/4)
		}
		cut = turnCut(stretch, withinBudget(stretch, budget, runStart(stretch, cfg.currentRun)))
	}
	if cfg.keepLastTurn {
		cut = min(cut, lastTurnStart(stretch, cfg.run))
	}
	return cut
}
