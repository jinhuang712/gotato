package modelctx

import (
	"slices"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// lastTurnStart returns the index where the latest Turn of run begins: the
// Run's last reply, so the next model call sees everything committed after it,
// or the Run's first Message when it has no reply yet. With no Messages of run
// it is the last Message that is not a tool result, so a tool call keeps its
// results.
func lastTurnStart(messages []gotato.Message, run gotato.RunID) int {
	if run != "" {
		first := -1
		for i, message := range slices.Backward(messages) {
			if message.RunID != run {
				continue
			}
			if message.Role == gotato.RoleAssistant {
				return i
			}
			first = i
		}
		if first >= 0 {
			return first
		}
	}
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
	for _, message := range slices.Backward(messages) {
		if id := message.RunID; id != "" && id != skip {
			latest = id
			break
		}
	}
	if latest == "" {
		for i, message := range slices.Backward(messages) {
			if message.Role == gotato.RoleUser {
				return i
			}
		}
		return 0
	}
	// Messages with no Run after the Run's last stay in the tail; one before
	// its first is not part of it.
	start := len(messages)
	for i, message := range slices.Backward(messages) {
		if message.RunID == latest {
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
