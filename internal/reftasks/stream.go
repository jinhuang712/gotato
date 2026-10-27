package reftasks

import (
	"context"
	"io"

	"github.com/jinhuang712/gotato/gotato"
)

// stream plays a Script event by event.
type stream struct {
	events []gotato.ModelEvent
	index  int
}

func (s *stream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if err := ctx.Err(); err != nil {
		return gotato.ModelEvent{}, err
	}
	if s.index >= len(s.events) {
		return gotato.ModelEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error { return nil }

// promptText returns the first text part of a Message: the user's prompt.
func promptText(message gotato.Message) string {
	for _, part := range message.Parts {
		if part.Kind == gotato.ContentText {
			return part.Text
		}
	}
	return ""
}
