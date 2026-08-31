package gotato

import "time"

type CoreLimits struct {
	MaxTurns               uint32
	MaxMessages            uint32
	MaxMessageBytes        uint64
	MaxTranscriptBytes     uint64
	MaxToolCalls           uint32
	MaxToolResultBytes     uint64
	MaxToolProgressBytes   uint64
	MaxToolProgressUpdates uint32
	RunDeadline            time.Duration
	ModelCallDeadline      time.Duration
	ToolCallDeadline       time.Duration
}

func defaultLimits() CoreLimits {
	return CoreLimits{
		MaxTurns:               32,
		MaxMessages:            1000,
		MaxMessageBytes:        4 << 20,
		MaxTranscriptBytes:     32 << 20,
		MaxToolCalls:           128,
		MaxToolResultBytes:     4 << 20,
		MaxToolProgressBytes:   1 << 20,
		MaxToolProgressUpdates: 1000,
		RunDeadline:            5 * time.Minute,
		ModelCallDeadline:      2 * time.Minute,
		ToolCallDeadline:       2 * time.Minute,
	}
}
