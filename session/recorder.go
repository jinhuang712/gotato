package session

import (
	"context"
	"fmt"

	gotato "github.com/jinhuang712/gotato"
)

// Recorder is an EventObserver Extension that writes what happened into a
// Session: Run records, usage, and the Event stream itself. Install it with
// gotato.WithExtension(session.Record(s)) next to gotato.WithTranscript(s).
//
// It is advisory: a Recorder failure never settles a Run.
type Recorder struct {
	session *Session
	pending map[gotato.RunID]*runProgress
}

type runProgress struct {
	turns     uint32
	toolCalls uint32
	usage     gotato.Usage
}

// Record creates a Recorder for s.
func Record(s *Session) *Recorder {
	return &Recorder{session: s, pending: map[gotato.RunID]*runProgress{}}
}

// Advisory implements gotato.AdvisoryExtension.
func (r *Recorder) Advisory() bool { return true }

// Observe implements gotato.EventObserver. Events are delivered by the Agent
// goroutine in order, so no locking is needed on the pending map: one
// Recorder belongs to one Agent, and sharing it across Agents is not
// supported.
func (r *Recorder) Observe(_ context.Context, event gotato.Event) error {
	r.session.RecordEvent(event)
	switch event.Kind {
	case gotato.EventAgentStart:
		r.pending[event.RunID] = &runProgress{}
		r.session.RecordRunStart(event.RunID, event.AgentID, event.Timestamp)
	case gotato.EventTurnStart:
		if p := r.pending[event.RunID]; p != nil {
			p.turns++
		}
	case gotato.EventToolExecutionStart:
		if p := r.pending[event.RunID]; p != nil {
			p.toolCalls++
		}
	case gotato.EventTurnEnd:
		if p := r.pending[event.RunID]; p != nil {
			if summary, ok := event.Payload["summary"].(map[string]any); ok {
				p.usage.InputTokens += toUint64(summary["input_tokens"])
				p.usage.OutputTokens += toUint64(summary["output_tokens"])
				p.usage.TotalTokens += toUint64(summary["total_tokens"])
			}
		}
	case gotato.EventAgentEnd:
		p := r.pending[event.RunID]
		if p == nil {
			p = &runProgress{}
		}
		delete(r.pending, event.RunID)
		status := gotato.RunStatus(fmt.Sprint(event.Payload["status"]))
		var errText, stopReason string
		if code, ok := event.Payload["error"]; ok && code != nil {
			errText = fmt.Sprint(code)
		}
		if reason, ok := event.Payload["stop_reason"]; ok && reason != nil {
			stopReason = fmt.Sprint(reason)
		}
		r.session.RecordRunEnd(event.RunID, status, errText, stopReason, p.turns, p.toolCalls, p.usage, event.Timestamp)
	}
	return nil
}

func toUint64(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case uint32:
		return uint64(typed)
	case int:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case int64:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case float64:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	default:
		return 0
	}
}
