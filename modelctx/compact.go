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
	// KeepMessages is the number of most recent Messages to keep in the tail.
	// The cut moves forward to a Turn boundary so a tool call keeps its
	// result. With KeepMessages 0, the tail is the latest Run's Turns, up to
	// SegmentSettings.TailTokens (20K tokens by default) and at most a quarter
	// of WindowTokens, or of CeilingTokens when there is no window.
	KeepMessages int
	// MinTokens skips the compaction when the stretch since the last
	// Checkpoint is estimated below it. 0 always compacts.
	MinTokens int
	// WindowTokens is the Model's context window, 0 when unknown, and
	// CeilingTokens the ceiling in force, 0 for none. They cap the tail
	// that TailTokens sizes.
	WindowTokens  int
	CeilingTokens int
	// Recap writes the Recap that ends the Segment; nil means the Segment has
	// none. A Recap that fails leaves the Segment without one.
	Recap RecapWriter
	// Context builds the request the next model call would send for the
	// Session as it is now, which a Recap written by a model sends as its
	// prefix. service.Runner sets it. Without it, a ModelRecap fails and the
	// Segment has no Recap.
	Context func(context.Context) (gotato.ModelRequest, error)
	// DropReasoning leaves the reasoning out of the Messages Compact keeps,
	// for a Model whose provider is sent only the current Round's reasoning
	// (see DropsReasoning). The Segment changes the prefix that reasoning
	// was written under, and Claude checks that prefix: sent again, the
	// reasoning could be refused. Leave it false for a provider that needs
	// every reasoning artifact back, such as DeepSeek.
	DropReasoning bool
	// KeepLastTurn keeps at least the latest Turn in the tail, whatever the
	// budget: the last assistant Message with the results of its tool calls,
	// or the last Message when it is not a result. A compaction in the middle
	// of a Run needs it, since the model call that follows continues from
	// that Turn.
	KeepLastTurn bool
	// CurrentRun is the Run that is starting. Its Messages are not "the latest
	// Run" when the default tail is chosen, so the Run before it stays as it
	// was, and the new Run's Messages follow in the tail.
	CurrentRun gotato.RunID
	// Why says why the compaction runs, for its Events: on demand (the
	// default), past the ceiling, on an overflow, or before the Session
	// leaves memory.
	Why gotato.CompactionWhy
	// SegmentBudgetTokens is the most the Segments together may hold, in
	// estimated tokens, before the oldest are merged right after the
	// Segment closes (MergeSegments). 0 means SegmentsBudget(CeilingTokens):
	// half the ceiling, or DefaultSegmentsBudgetTokens with none.
	SegmentBudgetTokens int
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

// causeOf names the code of a failure for the Event that reports it.
func causeOf(err error) gotato.ErrorCode {
	var runtime *gotato.RuntimeError
	switch {
	case errors.As(err, &runtime) && runtime != nil:
		return runtime.Code
	case errors.Is(err, context.DeadlineExceeded):
		return gotato.CodeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return gotato.CodeCanceled
	}
	return gotato.CodeStoreFailure
}

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
// "none"; the usage of a model call that was made is still recorded). A stretch
// below cfg.MinTokens, or one the tail covers whole, is left as it is and
// returns a Result with Replaced false and a Reason.
func Compact(ctx context.Context, s *session.Record, cfg CompactConfig) (Result, error) {
	if s == nil {
		return Result{}, errors.New("modelctx: session is nil")
	}
	all := s.Messages()
	stretch := s.Tail()
	if cfg.MinTokens > 0 && EstimateTokens(stretch) < cfg.MinTokens {
		return skippedResult(s, all, CompactReasonBelowMinTokens), nil
	}
	cut := tailStart(stretch, cfg)
	if cut <= 0 {
		return skippedResult(s, all, CompactReasonNothingToClose), nil
	}
	why := cfg.Why
	if why == "" {
		why = gotato.CompactionWhyOnDemand
	}
	s.Emit(gotato.CompactionStarted{Header: sessionHeader(s), Why: why})

	closed := stretch[:cut]
	recapName, recapText, recapUsage, err := writeRecap(ctx, s, cfg, closed, why)
	if err != nil {
		return Result{}, err
	}

	tail := keptTail(stretch[cut:], cfg.DropReasoning)
	loaded := s.LoadedTools()
	segment := buildSegment(s.NextSegment(), closed, recapText, cfg.SegmentSettings, loaded, s.Position)
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
		Recap:            recapName,
		RecapUsage:       recapUsage,
		Segment:          segment.Number,
		TokensBefore:     EstimateTokens(all),
		TokensAfter:      EstimateTokens(after),
		BytesBefore:      len(before),
		BytesAfter:       len(encoded),
		CeilingTokens:    cfg.CeilingTokens,
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
	// The Segments may now pass their budget: the oldest merge, in a
	// Checkpoint of their own. A merge that fails leaves the Segment just
	// closed in place, and the Segments unmerged.
	merge, err := MergeSegments(ctx, s, MergeConfig{
		BudgetTokens: cfg.SegmentBudgetTokens, CeilingTokens: cfg.CeilingTokens, Recap: cfg.Recap, Why: why, Location: cfg.Location,
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

// sessionHeader is the Header of an Event about the Session itself.
func sessionHeader(s *session.Record) gotato.Header {
	return gotato.NewHeader(gotato.SessionID(s.ID()), "", 0)
}

// writeRecap asks cfg.Recap for the Recap of the closed Messages. It returns
// the Recap writer's name and the text, "none" and empty when there is no
// Recap, and the usage of the model call if one was made. A Recap that fails
// costs the Segment its Recap, not the compaction; only a canceled ctx is an
// error, after a compaction_failed Event.
func writeRecap(ctx context.Context, s *session.Record, cfg CompactConfig, closed []gotato.Message, why gotato.CompactionWhy) (name, recap string, usage *gotato.Usage, err error) {
	name = "none"
	if cfg.Recap == nil {
		return name, "", nil, nil
	}
	request := RecapRequest{Segment: s.NextSegment(), Closed: closed, From: stretchStart(closed, s.Position), Location: cfg.Location}
	var contextErr error
	if cfg.Context != nil {
		var built gotato.ModelRequest
		if built, contextErr = cfg.Context(ctx); contextErr == nil {
			request.Context = &built
		}
	}
	var message gotato.Message
	var writeErr error
	if contextErr == nil {
		message, writeErr = cfg.Recap.WriteRecap(ctx, request)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		s.Emit(gotato.CompactionFailed{Header: sessionHeader(s), Why: why, Cause: causeOf(ctxErr)})
		return "", "", nil, ctxErr
	}
	if message.Usage != (gotato.Usage{}) {
		u := message.Usage
		usage = &u
	}
	if text := strings.TrimSpace(gotato.TextOf(message)); writeErr == nil && contextErr == nil && text != "" {
		name, recap = cfg.Recap.Name(), text
	}
	return name, recap, usage, nil
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
