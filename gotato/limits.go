package gotato

import (
	"context"
	"time"
)

// CoreLimits bounds what a Session accepts and how long it waits. A zero
// field means its default (see DefaultLimits). The Message and Transcript limits
// are in bytes and counts of the committed Transcript; the deadlines apply to
// one model call and one Tool call.
type CoreLimits struct {
	MaxMessages uint32
	// MaxMessageBytes bounds one Message. A Model's reply is held to it while
	// it streams: the call is stopped, with limit_exceeded, before the reply
	// grows past it.
	MaxMessageBytes     uint64
	MaxTranscriptBytes  uint64
	MaxSteerMessages    uint32
	MaxFollowUpMessages uint32
	// MaxNotices bounds the notices that wait for the next model call
	// (Session.Notice). Gotato's own notices are not
	// queued and not bounded by it.
	MaxNotices uint32
	// MaxTruncatedContinuations bounds, per Run, how many Turns that stopped
	// at the Model's output limit without a Tool Call are continued with
	// the continue notice instead of settling the Run on a cut-off
	// Message. Zero (the default) settles as before.
	MaxTruncatedContinuations uint32
	// MaxParallelTools bounds the workers one Turn may run at once. One (the
	// default) keeps a batch sequential.
	MaxParallelTools uint32
	// ModelCallDeadline and ToolCallDeadline end one Model call or one Tool
	// call that hangs. A Tool call past its deadline fails with
	// deadline_exceeded and the Run goes on, even when the Tool ignores its
	// ctx. Zero is the default, 2 minutes; a negative value means no
	// deadline, so only the Run's ctx ends the call.
	ModelCallDeadline time.Duration
	ToolCallDeadline  time.Duration
}

// TruncatedContinuationText is the text of the continue notice a Run goes on
// with after a Turn stopped at the output limit without a Tool Call (see
// CoreLimits.MaxTruncatedContinuations).
const TruncatedContinuationText = "Your last reply was cut off at the output limit before you acted. Continue from where you stopped: keep any reasoning short and take the next step with a tool call, or give your final answer."

// DefaultLimits returns the limits a Session applies. A zero field of
// CoreLimits always means its default, with or without WithLimits, so
// WithLimits(CoreLimits{MaxParallelTools: 4}) changes that one field and
// keeps the rest. DefaultLimits is for reading the defaults.
//
// The core counts no Turns or Tool calls and sets no deadline on a Run: a Run
// ends by the Model's final reply, by its ctx, by a TurnStopper or by the
// compaction ceiling. A team that wants a Turn or call cap writes a
// TurnStopper; a Run's deadline is the ctx the caller passes.
//
// The closing results a failed Run commits for the Tool calls it left without
// a result are exempt from MaxMessages, MaxMessageBytes and
// MaxTranscriptBytes: the Run may be ending on one of those limits, and a Tool
// call without a result would make every later request invalid.
func DefaultLimits() CoreLimits { return defaultLimits() }

func defaultLimits() CoreLimits {
	return CoreLimits{
		MaxMessages:         10000,
		MaxMessageBytes:     4 << 20,
		MaxTranscriptBytes:  32 << 20,
		MaxSteerMessages:    8,
		MaxFollowUpMessages: 4,
		MaxNotices:          16,
		MaxParallelTools:    1,
		ModelCallDeadline:   2 * time.Minute,
		ToolCallDeadline:    2 * time.Minute,
	}
}

// A Tool's progress reports are bounded per call: past either cap they are
// dropped, and the call goes on.
const (
	maxToolProgressBytes   = 1 << 20
	maxToolProgressUpdates = 1000
)

// withDefaults returns l with every zero field set to its default.
// MaxTruncatedContinuations is the one field whose default is zero.
func (l CoreLimits) withDefaults() CoreLimits {
	d := defaultLimits()
	l.MaxMessages = orDefault(l.MaxMessages, d.MaxMessages)
	l.MaxMessageBytes = orDefault(l.MaxMessageBytes, d.MaxMessageBytes)
	l.MaxTranscriptBytes = orDefault(l.MaxTranscriptBytes, d.MaxTranscriptBytes)
	l.MaxSteerMessages = orDefault(l.MaxSteerMessages, d.MaxSteerMessages)
	l.MaxFollowUpMessages = orDefault(l.MaxFollowUpMessages, d.MaxFollowUpMessages)
	l.MaxNotices = orDefault(l.MaxNotices, d.MaxNotices)
	l.MaxParallelTools = orDefault(l.MaxParallelTools, d.MaxParallelTools)
	l.ModelCallDeadline = orDefault(l.ModelCallDeadline, d.ModelCallDeadline)
	l.ToolCallDeadline = orDefault(l.ToolCallDeadline, d.ToolCallDeadline)
	return l
}

func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// millis returns d in milliseconds, with its sub-millisecond part, as every
// ...MS field of an Event or a result carries it.
func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// boundedContext derives a context from parent that ends after duration, or
// only when canceled when duration is not positive.
func boundedContext(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if duration > 0 {
		return context.WithTimeout(parent, duration)
	}
	return context.WithCancel(parent)
}

// controlCapacity turns a configured control-message limit into the limit of a
// control queue.
func controlCapacity(limit uint32) int {
	if limit > maxControlCapacity {
		return maxControlCapacity
	}
	return int(limit)
}

// maxControlCapacity bounds the control queues, whatever the
// configured limit.
const maxControlCapacity = 1024

// limitExceededUint32 reports whether value is over limit.
func limitExceededUint32(limit, value uint32) bool {
	return value > limit
}

// limitExceededUint64 reports whether value is over limit.
func limitExceededUint64(limit, value uint64) bool {
	return value > limit
}
