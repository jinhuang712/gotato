package reftasks_test

import (
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
)

func TestToolErrorTaskRecoversFromBothKindsOfFailure(t *testing.T) {
	s, answers := play(t, reftasks.ToolErrorTask())
	var statuses []string
	for _, message := range s.Messages() {
		if result := message.ToolResult; result != nil {
			executed := "refused"
			if result.Executed {
				executed = "ran"
			}
			statuses = append(statuses, string(result.Status)+"/"+executed)
		}
	}
	if got := strings.Join(statuses, ","); got != "failed/refused,failed/ran,ok/ran" {
		t.Fatalf("tool results = %s", got)
	}
	if answers[0] != "Order 42 is shipped." {
		t.Fatalf("answer = %q", answers[0])
	}
}

func TestOrderHistoryHasTheRequestedSize(t *testing.T) {
	for _, size := range []int{2, 10, 100, 1000} {
		history := reftasks.OrderHistory(size)
		if len(history) != size {
			t.Fatalf("OrderHistory(%d) has %d messages", size, len(history))
		}
		if last := history[len(history)-1]; last.Role != gotato.RoleAssistant || len(last.ToolCalls) != 0 {
			t.Fatalf("OrderHistory(%d) ends with %+v, want an answer", size, last)
		}
	}
}

func TestConversationCheckFailsOnAWrongAnswer(t *testing.T) {
	task := reftasks.ConversationTask()
	answers := make([]string, 10)
	answers[9] = "You asked about orders 42 and 17."
	if err := task.Check(answers); err == nil {
		t.Fatal("a last answer without order 8 must fail the check")
	}
}
