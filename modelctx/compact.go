package modelctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
)

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
	// Now overrides the clock (tests).
	Now func() time.Time
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
// "none"; the usage of a model call that was made is still recorded). A stretch below opts.MinTokens, or one the tail covers whole, is
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
	var recapUsage *gotato.Usage
	if opts.Recap != nil {
		request := RecapRequest{Segment: s.NextSegment(), Closed: closed, From: stretchStart(closed, s.Position), Location: opts.Location}
		var contextErr error
		if opts.Context != nil {
			var built gotato.ModelRequest
			if built, contextErr = opts.Context(ctx); contextErr == nil {
				request.Context = &built
			}
		}
		var message gotato.Message
		var err error
		if contextErr == nil {
			message, err = opts.Recap.WriteRecap(ctx, request)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			s.Emit(gotato.CompactionFailed{Header: header(), Why: why, Cause: causeOf(ctxErr)})
			return Result{}, ctxErr
		}
		if message.Usage != (gotato.Usage{}) {
			usage := message.Usage
			recapUsage = &usage
		}
		// A Recap that fails costs the Segment its Recap, not the compaction.
		if text := strings.TrimSpace(gotato.TextOf(message)); err == nil && contextErr == nil && text != "" {
			recapName, recapText = opts.Recap.Name(), text
		}
	}

	tail := make([]gotato.Message, 0, len(stretch)-cut)
	for _, message := range stretch[cut:] {
		if opts.DropReasoning {
			message = withoutReasoning(message)
		}
		// The usage a reply reported was for the request that Context no
		// longer is, so it no longer counts toward the ceiling.
		message.Usage = gotato.Usage{}
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
		RecapUsage:       recapUsage,
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

// DefaultReserveTokens is the least room the default ceiling leaves in the
// window, for a reply and a Recap.
const DefaultReserveTokens = 16000

// DefaultCeiling is the ceiling a model's window gives: the window less the
// larger of a tenth of it and DefaultReserveTokens, 180K for a 200K window. It
// is 0, no ceiling, when the window is unknown or too small to leave any.
func DefaultCeiling(window int) int {
	if window <= 0 {
		return 0
	}
	return max(window-max(window/10, DefaultReserveTokens), 0)
}

// CompactPolicy says when a Session compacts by itself. Past Ceiling, estimated
// in tokens, the Session is compacted before the model call that would send
// more; a request the provider refuses as too long is compacted once and sent
// again (AutoCompactor).
//
// Ceiling above 0 is the ceiling in force. At 0 it is DefaultCeiling(Window),
// so a model whose window is known is compacted past it by default, and with no
// window there is no ceiling. A negative Ceiling turns the ceiling off.
// Window is the Model's context window, 0 when unknown. Floor, when above 0, is
// the most tokens the kept tail holds; with none, the tail is the latest Run's
// Turns (CompactOptions). DropReasoning is CompactOptions.DropReasoning for
// every compaction the policy makes.
type CompactPolicy struct {
	Ceiling       int
	Floor         int
	Window        int
	Recap         RecapWriter
	DropReasoning bool
	Now           func() time.Time
	SegmentSettings
}

// EffectiveCeiling is the ceiling in force: Ceiling when set, else the one the
// window gives, else 0 for none.
func (p CompactPolicy) EffectiveCeiling() int {
	switch {
	case p.Ceiling > 0:
		return p.Ceiling
	case p.Ceiling < 0:
		return 0
	}
	return DefaultCeiling(p.Window)
}

// AutoCompactor is a gotato.TurnPreparer that applies a CompactPolicy to one
// Session. Install it with gotato.WithExtension next to WithTranscript(s).
//
// Before every model call it compacts the Session when its size is past the
// ceiling (why: past_ceiling), and when the provider refused the request as too
// long (why: overflow). A Turn compacts at most once, whatever the trigger. A
// Turn that is still past the ceiling after it ends the Run with
// ErrLimitExceeded; a Turn whose request is still refused ends it with
// ErrContextOverflow.
type AutoCompactor struct {
	session *session.Record
	policy  CompactPolicy
	last    Result
	runs    int
	// turn is the Turn that has compacted, if any.
	turn      turnKey
	compacted bool
}

type turnKey struct {
	run  gotato.RunID
	turn gotato.TurnNumber
}

// AutoCompact creates an AutoCompactor.
func AutoCompact(s *session.Record, policy CompactPolicy) *AutoCompactor {
	return &AutoCompactor{session: s, policy: policy}
}

// PrepareTurn implements gotato.TurnPreparer. A compaction helps a Run but is
// not a step it needs, so one that cannot be written is dropped (its Events
// say so) and the Run goes on, unless the Context is still past the ceiling.
// A Record the compaction refuses, or a Run stopped during the compaction,
// ends the Run.
func (a *AutoCompactor) PrepareTurn(ctx context.Context, pending gotato.PendingTurn) (gotato.TurnPrepared, error) {
	if a.session == nil {
		return gotato.TurnPrepared{}, nil
	}
	if key := (turnKey{pending.RunID, pending.Turn}); key != a.turn {
		a.turn, a.compacted = key, false
	}
	if pending.Overflow {
		if a.compacted {
			// The Turn compacted already: the refusal stands.
			return gotato.TurnPrepared{}, nil
		}
		prepared, _, err := a.compact(ctx, pending, gotato.CompactionOverflow)
		return prepared, err
	}
	ceiling := a.Ceiling()
	overhead := requestOverhead(pending.Request)
	if ceiling <= 0 || a.size(a.session.Messages(), overhead) <= ceiling {
		return gotato.TurnPrepared{}, nil
	}
	prepared, dropped, err := a.compact(ctx, pending, gotato.CompactionPastCeiling)
	if err != nil {
		return prepared, err
	}
	// After a compaction the old usage no longer counts: the size is
	// estimated afresh from what the Context is now.
	if fresh := EstimateTokens(a.session.Messages()) + overhead; fresh > ceiling {
		why := "the Context is still past the ceiling after its compaction"
		switch {
		case dropped:
			why = "the Context is past the ceiling and its compaction failed"
		case !prepared.Changed:
			why = "the Context is past the ceiling and there is nothing to compact"
		}
		return prepared, gotato.ErrorOf(gotato.ErrLimitExceeded, fmt.Sprintf("%s (about %d tokens, ceiling %d)", why, fresh, ceiling))
	}
	return prepared, nil
}

// compact closes a Segment before the model call, once per Turn. dropped says
// the compaction was attempted and could not be written.
func (a *AutoCompactor) compact(ctx context.Context, pending gotato.PendingTurn, why gotato.CompactionWhy) (prepared gotato.TurnPrepared, dropped bool, err error) {
	// What the Run has staged is written first, so the compaction's Events
	// follow it.
	if pending.Flush != nil {
		if err := pending.Flush(ctx); err != nil {
			return prepared, false, err
		}
	}
	a.compacted = true
	options := CompactOptions{
		Floor: a.policy.Floor, Window: a.policy.Window, Ceiling: a.Ceiling(),
		Recap: a.policy.Recap, DropReasoning: a.policy.DropReasoning, Now: a.policy.Now,
		Why: why, SegmentSettings: a.policy.SegmentSettings, KeepLastTurn: true,
		// A Recap read from the request the Run is about to send is read from
		// the provider's cache.
		Context: func(context.Context) (gotato.ModelRequest, error) { return pending.Request, nil },
	}
	if pending.Turn == 1 {
		options.CurrentRun = pending.RunID
	}
	result, err := Compact(ctx, a.session, options)
	if err != nil {
		if errors.Is(err, ErrOldSummary) || ctx.Err() != nil {
			return prepared, false, err
		}
		return prepared, true, nil
	}
	if result.Replaced {
		a.last = result
		a.runs++
	}
	prepared.Changed = result.Replaced
	return prepared, false, nil
}

// Last returns the most recent automatic compaction and how many happened.
func (a *AutoCompactor) Last() (Result, int) { return a.last, a.runs }

// Pending reports whether the next Run compacts the Session before it sends
// its first request, because the Session's size is past the ceiling. It counts
// the Messages alone; PendingFor counts the request as the Loop does.
func (a *AutoCompactor) Pending() bool {
	return a.PendingFor(gotato.ModelRequest{})
}

// PendingFor is Pending for the request the next model call would send, whose
// instruction and tools count toward the size as they do before a model call.
func (a *AutoCompactor) PendingFor(request gotato.ModelRequest) bool {
	ceiling := a.Ceiling()
	return a.session != nil && ceiling > 0 && a.size(a.session.Messages(), requestOverhead(request)) > ceiling
}

// Ceiling returns the estimated-token size past which a Session compacts first
// (CompactPolicy.EffectiveCeiling); 0 means this trigger is off.
func (a *AutoCompactor) Ceiling() int { return a.policy.EffectiveCeiling() }

// size is the Context's size in tokens: the input and output tokens the last
// model call reported, plus an estimate of what came after it. With no reported
// usage it is an estimate of everything, and overhead, the instruction and the
// tools, which a reported input count already holds. A compaction clears the
// usage of the Messages it keeps, so after it the size is estimated afresh.
func (a *AutoCompactor) size(messages []gotato.Message, overhead int) int {
	for i := len(messages) - 1; i >= 0; i-- {
		usage := messages[i].Usage
		if messages[i].Role == gotato.RoleAssistant && usage.InputTokens+usage.OutputTokens > 0 {
			return int(usage.InputTokens+usage.OutputTokens) + EstimateTokens(messages[i+1:])
		}
	}
	return EstimateTokens(messages) + overhead
}

// requestOverhead estimates the tokens of the request that are not Messages:
// the instruction and the tool definitions.
func requestOverhead(request gotato.ModelRequest) int {
	tools, _ := json.Marshal(request.Tools)
	return (len(request.SystemInstructions) + len(tools) + 3) / 4
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
	cut := 0
	switch {
	case opts.Keep > 0:
		cut = turnCut(stretch, len(stretch)-opts.Keep)
	case opts.Floor > 0:
		cut = turnCut(stretch, withinBudget(stretch, opts.Floor, 0))
	default:
		budget := orDefault(opts.TailTokens, DefaultTailTokens)
		base := opts.Window
		if base <= 0 {
			base = opts.Ceiling
		}
		if base > 0 {
			budget = min(budget, base/4)
		}
		cut = turnCut(stretch, withinBudget(stretch, budget, runStart(stretch, opts.CurrentRun)))
	}
	if opts.KeepLastTurn {
		cut = min(cut, lastTurnStart(stretch))
	}
	return cut
}

// lastTurnStart returns the index where the latest Turn begins: the last
// Message that is not a tool result, so a tool call keeps its results.
func lastTurnStart(messages []gotato.Message) int {
	i := len(messages) - 1
	for i > 0 && messages[i].Role == gotato.RoleToolResult {
		i--
	}
	return max(i, 0)
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

// runStart returns the index of the first Message of the latest Run, not
// counting the Run skip, the one that is starting. Without Run IDs it is the
// last user Message.
func runStart(messages []gotato.Message, skip gotato.RunID) int {
	var latest gotato.RunID
	for i := len(messages) - 1; i >= 0; i-- {
		if id := messages[i].RunID; id != "" && id != skip {
			latest = id
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

// stretchStart returns when the earliest of the Messages was committed, or the
// zero time when no position says.
func stretchStart(messages []gotato.Message, position func(gotato.MessageID) (gotato.MessagePosition, bool)) time.Time {
	var from time.Time
	for _, message := range messages {
		if p, ok := position(message.ID); ok && !p.Time.IsZero() && (from.IsZero() || p.Time.Before(from)) {
			from = p.Time.UTC()
		}
	}
	return from
}
