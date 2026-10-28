package testkit_test

import (
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestSessionFixture(t *testing.T) {
	s := testkit.NewRecord("q", "a", "q2")
	messages := s.Messages()
	if len(messages) != 3 || messages[1].Role != gotato.RoleAssistant || messages[2].ID != "fixture-3" {
		t.Fatalf("fixture = %+v", messages)
	}
}
