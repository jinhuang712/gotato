package gotato

import "time"

type CoreLimits struct {
	MaxMessages            uint32
	MaxMessageBytes        uint64
	MaxTranscriptBytes     uint64
	MaxToolProgressBytes   uint64
	MaxToolProgressUpdates uint32
	MaxSteerMessages       uint32
	MaxFollowUpMessages    uint32
	// MaxTruncatedContinuations bounds, per Run, how many Turns that stopped
	// at the Model's output limit without a Tool Call are continued with
	// TruncatedContinuationText instead of settling the Run on a cut-off
	// Message. Zero (the default) settles as before.
	MaxTruncatedContinuations uint32
	// MaxParallelTools bounds the workers one Turn may run at once. Zero or
	// one keeps a batch sequential.
	MaxParallelTools  uint32
	ModelCallDeadline time.Duration
	ToolCallDeadline  time.Duration
}

// TruncatedContinuationText is the user Message a Run continues with after a
// Turn stopped at the output limit without a Tool Call (see
// CoreLimits.MaxTruncatedContinuations).
const TruncatedContinuationText = "Your last reply was cut off at the output limit before you acted. Continue from where you stopped: keep any reasoning short and take the next step with a tool call, or give your final answer."

// DefaultLimits returns the limits NewSession applies when WithLimits is not
// used. Adjust a copy and pass it to WithLimits to override some fields.
//
// A zero field is interpreted per field, not uniformly. Once WithLimits makes
// the limits explicit, a zero MaxMessages, MaxMessageBytes or
// MaxTranscriptBytes admits no work of that kind, and a zero MaxToolProgressBytes or MaxToolProgressUpdates
// suppresses Tool progress reports. MaxParallelTools zero or one keeps a batch sequential, and
// MaxSteerMessages or MaxFollowUpMessages zero leaves that control channel
// unbuffered. A zero deadline disables that deadline.
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
		MaxMessages:            10000,
		MaxMessageBytes:        4 << 20,
		MaxTranscriptBytes:     32 << 20,
		MaxToolProgressBytes:   1 << 20,
		MaxToolProgressUpdates: 1000,
		MaxSteerMessages:       8,
		MaxFollowUpMessages:    4,
		MaxParallelTools:       1,
		ModelCallDeadline:      2 * time.Minute,
		ToolCallDeadline:       2 * time.Minute,
	}
}
