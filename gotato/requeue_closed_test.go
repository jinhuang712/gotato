package gotato_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// Requeue into a Session that has closed must hand every message back as
// rest, so the host can report it, instead of holding it where nothing will
// take it.
func TestRequeueIntoAClosedSessionReturnsEveryMessage(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := []gotato.Queued{
		{Message: gotato.UserMessage("steer"), Source: gotato.MessageSourceSteer},
		{Message: gotato.UserMessage("follow"), Source: gotato.MessageSourceFollowUp},
	}
	if rest := s.Requeue(in); len(rest) != len(in) {
		t.Fatalf("Requeue into a closed Session returned %d of %d messages as rest", len(rest), len(in))
	}
}
