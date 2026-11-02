package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/text"
)

// CompactConfig is what one Compact call changes about the Compactor it runs.
// The numbers it sets (CompactPolicy) replace the Compactor's for this call
// when they are not zero, so an on-demand merge can name a Segment budget of
// its own.
type CompactConfig struct {
	CompactPolicy
	// KeepMessages is the number of most recent Messages to keep in the tail,
	// for this call: it replaces the Compactor's Cutter with
	// TailBudget{KeepMessages: n}. The cut moves forward to a Turn boundary so
	// a tool call keeps its result. 0 leaves the Cutter as it is.
	KeepMessages int
	// MinTokens skips the compaction when the stretch since the last
	// Checkpoint is estimated below it. 0 always compacts.
	MinTokens int
	// Recap replaces the Compactor's RecapWriter for this call; nil leaves it.
	Recap RecapWriter
}

// compactCall is one call of Compact: the Compactor and the things only a
// caller that runs a compaction inside a Run or a Session's life knows.
// AutoCompactor sets all of them; service sets the Recap context and the
// reason through CompactOption.
type compactCall struct {
	Compactor
	// minTokens skips the compaction when the stretch is estimated below it.
	minTokens int
	// context builds the request the next model call would send for the
	// Session as it is now, which a Recap written by a model sends as its
	// prefix. Without it, a ModelRecap fails and the Segment has no Recap.
	context func(context.Context) (gotato.ModelRequest, error)
	// keepLastTurn keeps at least the latest Turn of run in the tail,
	// whatever the Cutter says: the Run's last reply and everything after it
	// (its tool results, a notice, a steer, an interrupt), or on the Run's first
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

// Compact closes everything since the last Checkpoint, less a kept tail, into
// a Segment built by the Compactor's Builder (by default the user's Messages and
// each Run's final reply word for word, one line per tool call, and the Recap
// the Compactor's writer writes, if any). It appends the Segment (a
// segment_closed Entry) and the Checkpoint that lists it (the compaction_ended
// Event) in one step, between a compaction_started and, when the write fails,
// a compaction_failed. The Messages it closes stay in the store, and a load
// starts after them. It must not run while a Run is using the Session
// (AutoCompact runs it at the sanctioned point).
//
// The kept tail starts at a Turn boundary, so a tool call is never separated
// from its result (see Compactor). The loaded Tools the Segment called and
// the kept tail calls stay loaded; the others are unloaded, each with a
// tool_unloaded Event (why: compaction).
//
// A Recap that fails leaves the Segment without one (Compaction.Recap is
// "none" and Compaction.RecapError says why; the usage of a model call that
// was made is still recorded). A stretch below cfg.MinTokens, or one the tail
// covers whole, is left as it is and returns a Result with Replaced false and
// a Reason. A Compactor that cannot work as set (Compactor.Validate) is an
// error before anything is compacted. A part that fails or breaks the frame's
// rules (ErrBadPart) drops the compaction, with an error.
func Compact(ctx context.Context, store SegmentStore, c Compactor, cfg CompactConfig, opts ...CompactOption) (Result, error) {
	c.CompactPolicy = c.overriddenBy(cfg.CompactPolicy)
	if cfg.Recap != nil {
		c.Recap = cfg.Recap
	}
	if cfg.KeepMessages > 0 {
		c.Cut = TailBudget{KeepMessages: cfg.KeepMessages}
	}
	call := compactCall{Compactor: c, minTokens: cfg.MinTokens}
	for _, opt := range opts {
		opt(&call)
	}
	return runCompact(ctx, store, call)
}

// cloneAll copies Messages deeply, so a part cannot change what the frame keeps.
func cloneAll(messages []gotato.Message) []gotato.Message {
	out := make([]gotato.Message, len(messages))
	for i, message := range messages {
		out[i] = message.Clone()
	}
	return out
}

// contextOf returns the Messages the Context holds: the Segments' and the tail.
func contextOf(store SegmentStore, segments []gotato.Segment) []gotato.Message {
	return slices.Concat(flatten(segments), store.Tail())
}

// failCompaction publishes the compaction_failed Event of a compaction that was
// dropped, and returns err.
func failCompaction(store SegmentStore, why gotato.CompactionWhy, err error) error {
	store.Emit(gotato.CompactionFailed{Header: sessionHeader(store), Why: why, Cause: causeOf(err)})
	return err
}

// cutOf asks the Cutter where the tail starts and brings the answer within the
// frame's rules: inside the stretch, at a Turn boundary and, in the middle of a
// Run, no later than the Run's last Turn. It is 0 when nothing is closed.
func (c compactCall) cutOf(stretch []gotato.Message) (int, error) {
	info := CutInfo{WindowTokens: c.WindowTokens, CeilingTokens: c.EffectiveCeilingTokens(), CurrentRun: c.currentRun}
	cutter := c.cutter()
	raw, err := callPart("Cutter", func() (int, error) { return cutter.Cut(cloneAll(stretch), info), nil })
	if err != nil {
		return 0, err
	}
	if raw < 0 || raw > len(stretch) {
		return 0, fmt.Errorf("%w: the Cutter returned %d for a stretch of %d Messages", ErrBadPart, raw, len(stretch))
	}
	cut := turnCut(stretch, raw)
	if c.keepLastTurn {
		cut = min(cut, lastTurnStart(stretch, c.run))
	}
	return cut, nil
}

// segmentOf asks the Builder for Segment info.Number and makes it the frame's:
// it checks what the Builder returned (checkSegment), appends the Recap
// escaped, and stamps the number, the times, the source, the Message IDs and the
// loaded Tools the Segment called.
func (c compactCall) segmentOf(closed []gotato.Message, info SegmentInfo, loaded []string) (gotato.Segment, error) {
	builder := c.builder()
	built, err := callPart("Builder", func() (gotato.Segment, error) { return builder.Build(cloneAll(closed), info) })
	if err != nil {
		return gotato.Segment{}, err
	}
	if err := checkSegment(built, closed); err != nil {
		return gotato.Segment{}, err
	}
	messages := cloneAll(built.Messages)
	if info.Recap != "" {
		recap := taggedPart(gotato.TagRecap, numberAttr(info.Number), text.EscapeTags(info.Recap))
		if n := len(messages); n > 0 && messages[n-1].Role == gotato.RoleUser {
			messages[n-1].Parts = append(messages[n-1].Parts, recap)
		} else {
			messages = append(messages, gotato.Message{Role: gotato.RoleUser, Parts: []gotato.ContentPart{recap}})
		}
	}
	for i := range messages {
		messages[i].ID = gotato.MessageID(fmt.Sprintf("segment-%d-%d", info.Number, i+1))
	}
	_, _, source := stamps(closed, info.Position)
	return gotato.Segment{
		Number: info.Number, From: info.From, To: info.To, Source: source,
		Messages: messages, ToolCalls: slices.Clone(built.ToolCalls), Recap: info.Recap,
		LoadedTools: toolsCalled(closed, loaded),
	}, nil
}

// runCompact is Compact with the caller's options already applied.
func runCompact(ctx context.Context, store SegmentStore, call compactCall) (Result, error) {
	if isNilStore(store) {
		return Result{}, errors.New("modelctx: store is nil")
	}
	if err := call.Validate(); err != nil {
		return Result{}, err
	}
	segments := store.Segments()
	stretch := store.Tail()
	all := slices.Concat(flatten(segments), stretch)
	if call.minTokens > 0 && EstimateTokens(stretch) < call.minTokens {
		return skippedResult(store, all, CompactReasonBelowMinTokens), nil
	}
	why := call.why
	if why == "" {
		why = gotato.CompactionWhyOnDemand
	}
	cut, err := call.cutOf(stretch)
	if err != nil {
		store.Emit(gotato.CompactionStarted{Header: sessionHeader(store), Why: why})
		return Result{}, failCompaction(store, why, err)
	}
	if cut <= 0 {
		return skippedResult(store, all, CompactReasonNothingToClose), nil
	}
	store.Emit(gotato.CompactionStarted{Header: sessionHeader(store), Why: why})

	closed := stretch[:cut]
	recap, err := writeRecap(ctx, store, call, closed, why)
	if err != nil {
		return Result{}, err
	}

	tail := keptTail(stretch[cut:], call.DropReasoning)
	loaded := store.LoadedTools()
	info := SegmentInfo{Number: store.NextSegment(), Location: call.location(), Recap: recap.text, Position: store.Position}
	info.From, info.To, _ = stamps(closed, store.Position)
	segment, err := call.segmentOf(closed, info, loaded)
	if err != nil {
		return Result{}, failCompaction(store, why, err)
	}
	kept := keptTools(loaded, segment.LoadedTools, tail)

	before, _ := json.Marshal(all)
	// What the Record holds after: the Segments before, the new one, the tail.
	after := slices.Concat(flatten(segments), segment.Messages, tail)
	encoded, _ := json.Marshal(after)
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
		CeilingTokens:    call.EffectiveCeilingTokens(),
		LoadedTools:      kept,
	}
	if _, err := callStore("SegmentStore.CloseSegment", func() (gotato.Segment, error) {
		return store.CloseSegment(why, segment, tail, &record)
	}); err != nil {
		return Result{}, failCompaction(store, why, err)
	}
	for _, id := range loaded {
		if !slices.Contains(kept, id) {
			store.Emit(gotato.ToolUnloaded{Header: sessionHeader(store), ToolID: id, Why: gotato.ToolUnloadedWhyCompaction})
		}
	}
	result := Result{
		SessionID: store.ID(), Replaced: true, Compaction: &record,
		MessagesBefore: len(all), MessagesAfter: len(after),
		TokensBefore: record.TokensBefore, TokensAfter: record.TokensAfter,
	}
	return mergeAfterClose(ctx, store, call.Compactor, why, result)
}

// mergeAfterClose merges the oldest Segments, in a Checkpoint of their own,
// if the Segment just closed took them past their budget. A merge that fails
// leaves the Segment just closed in place, and the Segments unmerged.
func mergeAfterClose(ctx context.Context, store SegmentStore, c Compactor, why gotato.CompactionWhy, result Result) (Result, error) {
	merge, err := mergeOldest(ctx, store, c, c.effectiveBudget(), why)
	if err != nil {
		return result, err
	}
	if merge.Merged || merge.Error != "" {
		result.Merge = &merge
		merged := contextOf(store, store.Segments())
		result.MessagesAfter, result.TokensAfter = len(merged), EstimateTokens(merged)
	}
	return result, nil
}

// skippedResult is the Result of a Compact that closes nothing, for reason.
func skippedResult(store SegmentStore, all []gotato.Message, reason CompactReason) Result {
	tokens := EstimateTokens(all)
	return Result{SessionID: store.ID(), Reason: reason, MessagesBefore: len(all), MessagesAfter: len(all), TokensBefore: tokens, TokensAfter: tokens}
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
func writeRecap(ctx context.Context, store SegmentStore, call compactCall, closed []gotato.Message, why gotato.CompactionWhy) (recapOutcome, error) {
	out := recapOutcome{name: "none"}
	if call.Recap == nil {
		return out, nil
	}
	request := RecapRequest{Segment: store.NextSegment(), Closed: cloneAll(closed), From: stretchStart(closed, store.Position), Location: call.Location}
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
		store.Emit(gotato.CompactionFailed{Header: sessionHeader(store), Why: why, Cause: causeOf(ctxErr)})
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
