package testkit

import (
	"strconv"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// NewRecord creates a Session Record pre-populated with alternating user and
// assistant text Messages, e.g. NewRecord("hi", "hello", "how are you?").
func NewRecord(turns ...string) *session.Record {
	s := session.New()
	for i, text := range turns {
		var message gotato.Message
		if i%2 == 0 {
			message = gotato.UserMessage(text)
		} else {
			message = gotato.AssistantMessage(text)
			message.StopReason = gotato.StopReasonEndTurn
		}
		message.ID = gotato.MessageID("fixture-" + strconv.Itoa(i+1))
		_ = s.Append(message)
	}
	return s
}
